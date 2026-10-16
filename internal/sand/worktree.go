package sand

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// worktree is a checkout and its current branch.
type worktree struct {
	dir, branch string
}

// selectedWorktree keeps every phase of a command on the same sandbox branch.
var selectedWorktree *worktree

func worktreeArg() string {
	if selectedWorktree == nil {
		return ""
	}
	return " --worktree " + shellQuote(selectedWorktree.dir)
}

func worktreeFlag(c *cobra.Command) {
	var selector string
	c.Flags().StringVar(&selector, "worktree", "", "sandbox worktree path; use its branch and checkout")
	if c.Flags().Lookup("repo-dir") != nil {
		c.MarkFlagsMutuallyExclusive("worktree", "repo-dir")
	}
	run := c.RunE
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("worktree") {
			return run(cmd, args)
		}
		if strings.TrimSpace(selector) == "" {
			return fmt.Errorf("--worktree requires a sandbox path")
		}
		wt, err := resolveWorktree(selector)
		if err != nil {
			return err
		}
		previous := selectedWorktree
		selectedWorktree = &wt
		defer func() { selectedWorktree = previous }()
		return run(cmd, args)
	}
}

// The NUL format preserves spaces, quotes and newlines in registered paths.
func parseWorktrees(raw string) []worktree {
	var all []worktree
	var wt worktree
	for _, field := range strings.Split(raw, "\x00") {
		switch {
		case field == "":
			if wt.dir != "" {
				all = append(all, wt)
				wt = worktree{}
			}
		case strings.HasPrefix(field, "worktree "):
			wt.dir = strings.TrimPrefix(field, "worktree ")
		case strings.HasPrefix(field, "branch refs/heads/"):
			wt.branch = strings.TrimPrefix(field, "branch refs/heads/")
		}
	}
	return all
}

func resolveWorktree(dir string) (worktree, error) {
	cfg, err := Resolve(flagHost, flagRemoteDir)
	if err != nil {
		return worktree{}, err
	}
	remote := "cd -- " + remoteQuote(dir) + " && git rev-parse --show-toplevel && git symbolic-ref --quiet --short HEAD"
	out, err := exec.Command(sshBin(), cfg.Host, remote).CombinedOutput()
	if err != nil {
		return worktree{}, fmt.Errorf("reading worktree %s:%s: require an existing checkout with an attached branch: %w (%s)",
			cfg.Host, dir, err, strings.TrimSpace(string(out)))
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "/") || lines[1] == "" {
		return worktree{}, fmt.Errorf("invalid worktree path or branch from %s:%s", cfg.Host, dir)
	}
	return worktree{dir: lines[0], branch: lines[1]}, nil
}

// sameDir reports whether two paths name the same directory, symlinks out of the way.
func sameDir(a, b string) bool {
	if resolved, err := filepath.EvalSymlinks(a); err == nil {
		a = resolved
	}
	if resolved, err := filepath.EvalSymlinks(b); err == nil {
		b = resolved
	}
	return a == b
}

// checkSecondCheckout refuses to switch to branch when another worktree of this repository has
// it checked out: a ref has exactly one checkout, and git's own refusal arrives after the import
// has printed, under a message that names an untracked file as the usual cause. signWorktree
// routes a named branch to the worktree holding it, so this fires only when that routing did
// not run or a worktree appeared mid-run; the refusal names the worktree either way.
func checkSecondCheckout(g gitCmd, branch string) error {
	raw, err := g.capture("worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil // the switch's own error is the answer then
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	for _, wt := range parseWorktrees(raw) {
		if wt.branch == branch && !sameDir(wt.dir, cwd) {
			return fmt.Errorf("%s is already checked out in the worktree at %s, and a ref has exactly one\n"+
				"checkout, so this run cannot switch to it here. Run this from %s, or free the branch:\n"+
				"  git -C %s switch --detach\nnothing was rewritten", branch, wt.dir, wt.dir, wt.dir)
		}
	}
	return nil
}

func checkWorktreeTarget(t *Target) error {
	if selectedWorktree == nil {
		return nil
	}
	if t == nil {
		return fmt.Errorf("no PR for worktree %s", selectedWorktree.dir)
	}
	if err := t.LoadURL(); err != nil {
		return err
	}
	if t.Branch != selectedWorktree.branch {
		return fmt.Errorf("PR %s#%d uses branch %q, but worktree %s is on %q",
			t.Slug(), t.Number, t.Branch, selectedWorktree.dir, selectedWorktree.branch)
	}
	repo, err := currentRepo()
	if err != nil {
		return err
	}
	if t.Slug() != repo.Slug() {
		return fmt.Errorf("PR repository %s differs from this checkout's repository %s", t.Slug(), repo.Slug())
	}
	return nil
}

// signWorktree signs in the checkout that has the branch: the local worktree holding it, or a
// temporary one for a selected sandbox worktree the Mac does not have out. A branch no worktree
// holds is imported and signed in the current checkout, as signing always has. Other checkouts
// keep their branches and uncommitted files.
func signWorktree(o SignOpts) (SignResult, error) {
	if selectedWorktree != nil {
		if o.Branch != "" && o.Branch != selectedWorktree.branch {
			return SignResult{}, fmt.Errorf("branch %q does not match worktree %s on branch %q", o.Branch, selectedWorktree.dir, selectedWorktree.branch)
		}
		o.Branch = selectedWorktree.branch
	} else if o.Branch == "" {
		return Sign(o) // the current branch is held by this checkout by definition
	}
	g := gitCmd{out: o.Out}
	raw, err := g.capture("worktree", "list", "--porcelain", "-z")
	if err != nil {
		return SignResult{}, err
	}
	local := ""
	for _, wt := range parseWorktrees(raw) {
		if wt.branch == o.Branch {
			local = wt.dir
			break
		}
	}
	if local == "" {
		if selectedWorktree == nil {
			// Nobody has the branch out: the import switches this checkout onto it.
			return Sign(o)
		}
		tmp, err := os.MkdirTemp("", "sand-worktree-")
		if err != nil {
			return SignResult{}, err
		}
		local = filepath.Join(tmp, "checkout")
		if err := g.run("worktree", "add", "--quiet", "--detach", local, "HEAD"); err != nil {
			os.Remove(tmp)
			return SignResult{}, err
		}
		defer func() {
			if err := g.run("worktree", "remove", local); err != nil {
				warn(fmt.Sprintf("temporary signing worktree retained at %s: %v", local, err))
				return
			}
			os.Remove(tmp)
		}()
	}
	before, err := os.Getwd()
	if err != nil {
		return SignResult{}, err
	}
	if !sameDir(local, before) {
		fmt.Fprintf(o.Out, "Signing %s in %s, the worktree that has it checked out.\n", o.Branch, local)
	}
	if err := os.Chdir(local); err != nil {
		return SignResult{}, err
	}
	defer func() {
		if err := os.Chdir(before); err != nil {
			warn(fmt.Sprintf("could not return to %s: %v", before, err))
		}
	}()
	return Sign(o)
}
