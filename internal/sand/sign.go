package sand

// Signing happens on the Mac because the keys are there and never on the box, so commits
// arrive unsigned and have to be rewritten in place afterwards. The rewrite is
// `git commit-tree -S` under filter-branch rather than a rebase: it replays the exact
// trees with rewritten parents, so merge commits survive and no content conflict is
// possible. Everything before the rewrite is a refusal check, and everything after it is
// verification, because the failure mode to avoid is force-pushing a branch that lost
// commits or gained unsigned ones.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// aifBin is the command that imports a branch from the box. SAND_AIF overrides it, which
// is how the tests put a branch in place without the real tool.
func aifBin() string {
	if v := os.Getenv("SAND_AIF"); v != "" {
		return v
	}
	return "aif"
}

// protectedBranches never get rewritten, whatever the caller says.
var protectedBranches = map[string]bool{
	"main": true, "master": true, "develop": true, "trunk": true,
	"origin/main": true, "origin/master": true,
}

// SignOpts is one `sand sign` run. In and Out carry the prompts, so a test can answer them.
type SignOpts struct {
	Branch string
	Remote string
	Base   string
	Yes    bool // don't ask before rewriting
	Push   bool // push after verifying, without asking
	In     io.Reader
	Out    io.Writer
}

// Sign imports the branch, signs every commit it adds over the base, verifies the result
// and offers to push. It returns nil having done nothing when there is nothing to sign.
func Sign(o SignOpts) error {
	g := gitCmd{out: o.Out}
	var answers *bufio.Reader
	if o.In != nil {
		answers = bufio.NewReader(o.In)
	}

	gitDir, err := g.capture("rev-parse", "--absolute-git-dir")
	if err != nil {
		return fmt.Errorf("run inside a git repository")
	}

	branch := o.Branch
	if branch == "" {
		if branch, err = g.capture("branch", "--show-current"); err != nil || branch == "" {
			return fmt.Errorf("detached HEAD: name the branch to sign")
		}
	}
	if protectedBranches[branch] || strings.HasPrefix(branch, "release/") {
		return fmt.Errorf("refusing to rewrite protected branch %s", branch)
	}
	if err := checkInProgress(gitDir); err != nil {
		return err
	}
	if err := g.checkClean(); err != nil {
		return err
	}

	// A missing aif is not a reason to reach for some other way of moving the branch: the
	// wrong branch signed with the right key is worse than a clear stop.
	if _, err := exec.LookPath(aifBin()); err != nil {
		return fmt.Errorf("%s is required to import the branch and is not on PATH", aifBin())
	}

	base := o.Remote + "/" + o.Base
	if err := g.stream(exec.Command(aifBin(), branch)); err != nil {
		return fmt.Errorf("%s %s: %w", aifBin(), branch, err)
	}
	if err := g.run("fetch", o.Remote); err != nil {
		return err
	}
	if _, err := g.capture("rev-parse", "--verify", "--quiet", base); err != nil {
		return fmt.Errorf("base ref not found: %s", base)
	}
	if err := g.run("switch", branch); err != nil {
		return err
	}
	if _, err := g.capture("merge-base", base, "HEAD"); err != nil {
		return fmt.Errorf("no common history between %s and %s; refusing to rewrite", branch, base)
	}

	head, err := g.capture("rev-parse", "HEAD")
	if err != nil {
		return err
	}
	count, err := g.countUnique(head, base)
	if err != nil {
		return err
	}
	if count == 0 {
		fmt.Fprintf(o.Out, "No commits unique to %s; nothing to sign.\n", branch)
		return nil
	}

	backup := fmt.Sprintf("%s-before-signing-%s", strings.ReplaceAll(branch, "/", "-"), time.Now().Format("20060102150405"))
	if err := g.run("branch", backup, head); err != nil {
		return err
	}
	restore := fmt.Sprintf("restore with: git switch %s && git reset --hard %s", branch, backup)

	baseShort, _ := g.capture("rev-parse", "--short", base)
	fmt.Fprintf(o.Out, "About to sign exactly %d commit(s) unique to %s\n", count, branch)
	fmt.Fprintf(o.Out, "Comparison base: %s (%s)\n", base, baseShort)
	fmt.Fprintf(o.Out, "Commit topology, including merges, will be preserved.\nRecovery branch: %s\n\n", backup)
	if err := g.run("log", "--graph", "--oneline", "--decorate", head, "--not", base); err != nil {
		return err
	}
	if !o.Yes {
		if !confirm(answers, o.Out, "\nProceed?") {
			fmt.Fprintln(o.Out, "Cancelled. No changes made.")
			return nil
		}
	}

	// filter-branch supplies the original tree, the rewritten parents, the message and both
	// identities; -S is the only thing being added.
	rewrite := exec.Command("git", "filter-branch", "-f",
		"--commit-filter", `git commit-tree -S "$@"`,
		"--", branch, "--not", base)
	rewrite.Env = append(os.Environ(), "FILTER_BRANCH_SQUELCH_WARNING=1")
	if err := g.stream(rewrite); err != nil {
		return fmt.Errorf("signing rewrite failed, nothing was pushed: %w\n%s", err, restore)
	}
	if err := g.run("switch", branch); err != nil {
		return err
	}

	unsigned, err := g.unsignedCommits(branch, base)
	if err != nil {
		return err
	}
	if len(unsigned) > 0 {
		fmt.Fprintf(o.Out, "%d rewritten commit(s) have no signature:\n", len(unsigned))
		for _, c := range unsigned {
			_ = g.run("show", "-s", "--format=  %h %s", c)
		}
		return fmt.Errorf("nothing was pushed; %s", restore)
	}
	newCount, err := g.countUnique(branch, base)
	if err != nil {
		return err
	}
	if newCount != count {
		return fmt.Errorf("commit count changed from %d to %d, refusing to push; %s", count, newCount, restore)
	}

	fmt.Fprintf(o.Out, "\nVerified: all %d branch-unique commit(s) contain signatures.\n", newCount)
	if err := g.run("log", "--graph", "--format=%h  status=%G?  signer=%GS  %s", branch, "--not", base); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "\nRecovery branch retained as: %s\n", backup)

	push := o.Push
	if !push {
		push = confirm(answers, o.Out, fmt.Sprintf("Push %s to %s with --force-with-lease?", branch, o.Remote))
	}
	if !push {
		fmt.Fprintf(o.Out, "Not pushed. When ready:\n  git push --force-with-lease %s %s\n", o.Remote, branch)
		return nil
	}
	return g.run("push", "--force-with-lease", o.Remote, branch)
}

// checkInProgress refuses to touch a repository that is mid-operation: rewriting history
// under an unfinished rebase or merge loses whichever one you did not mean.
func checkInProgress(gitDir string) error {
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, d)); err == nil {
			return fmt.Errorf("a rebase is already in progress; finish it or `git rebase --abort` first")
		}
	}
	for _, f := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD"} {
		if _, err := os.Stat(filepath.Join(gitDir, f)); err == nil {
			return fmt.Errorf("a merge or cherry-pick is in progress; finish or abort it first")
		}
	}
	return nil
}

// confirm asks and defaults to no, including on EOF, so a non-interactive run never
// rewrites or pushes by accident. The reader is made once per run and reused: a fresh
// bufio.Reader per question reads ahead and eats the answer to the next one.
func confirm(in *bufio.Reader, out io.Writer, question string) bool {
	if in == nil {
		return false
	}
	fmt.Fprintf(out, "%s [y/N] ", question)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(out)
		return false
	}
	answer := strings.TrimSpace(line)
	return answer == "y" || answer == "Y"
}

// gitCmd runs git. Commands whose output belongs on screen stream to out; the ones read
// back are captured.
type gitCmd struct{ out io.Writer }

func (g gitCmd) capture(args ...string) (string, error) {
	c := exec.Command("git", args...)
	var stderr strings.Builder
	c.Stderr = &stderr
	b, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(b)), nil
}

func (g gitCmd) run(args ...string) error {
	return g.stream(exec.Command("git", args...))
}

func (g gitCmd) stream(c *exec.Cmd) error {
	c.Stdout, c.Stderr = g.out, g.out
	return c.Run()
}

// checkClean reports a working tree that would make the rewrite ambiguous.
func (g gitCmd) checkClean() error {
	for _, args := range [][]string{{"diff", "--quiet"}, {"diff", "--cached", "--quiet"}} {
		if err := exec.Command("git", args...).Run(); err != nil {
			return fmt.Errorf("working tree has uncommitted changes; commit or stash them first")
		}
	}
	return nil
}

// countUnique counts the commits rev adds over base, which is both what gets signed and
// what has to still be there afterwards.
func (g gitCmd) countUnique(rev, base string) (int, error) {
	s, err := g.capture("rev-list", "--count", rev, "--not", base)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(s)
}

// unsignedCommits lists the branch-unique commits with no signature header. The header is
// gpgsig for both OpenPGP and ssh signing, gpgsig-sha256 in a sha256 repository.
func (g gitCmd) unsignedCommits(branch, base string) ([]string, error) {
	list, err := g.capture("rev-list", branch, "--not", base)
	if err != nil {
		return nil, err
	}
	var unsigned []string
	for _, sha := range strings.Fields(list) {
		raw, err := g.capture("cat-file", "commit", sha)
		if err != nil {
			return nil, err
		}
		if !hasSignature(raw) {
			unsigned = append(unsigned, sha)
		}
	}
	return unsigned, nil
}

// hasSignature looks only at the commit header, so a message quoting "gpgsig" cannot pass
// for a signature.
func hasSignature(raw string) bool {
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			return false
		}
		if strings.HasPrefix(line, "gpgsig ") || strings.HasPrefix(line, "gpgsig-sha256 ") {
			return true
		}
	}
	return false
}
