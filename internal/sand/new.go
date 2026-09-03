package sand

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var branchWord = regexp.MustCompile(`[^a-z0-9]+`)

func (t Target) issuePath(base string) string {
	return path.Join(base, segment(t.Owner), segment(t.Repo), fmt.Sprintf("issue-%d", t.Number))
}

// issueBranch is the one branch name sand both writes and reads: `new` creates it, and `up`
// takes the issue number back out of it when there is no PR yet. The prefix is config
// (branch_prefix, defaulting to $USER) rather than the `guy/` it was compiled with, because a
// compiled-in one names a coworker's branch after somebody else, and then their own `sand up`
// cannot find the issue in a branch they named themselves.
func issueBranch(prefix string, number int, title string) string {
	slug := strings.Trim(branchWord.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if slug == "" {
		slug = "issue"
	}
	return fmt.Sprintf("%s%d-%s", branchPrefix(prefix), number, slug)
}

func issueNumberFromBranch(prefix, branch string) (int, bool) {
	rest, ok := strings.CutPrefix(branch, branchPrefix(prefix))
	if !ok {
		return 0, false
	}
	n, title, ok := strings.Cut(rest, "-")
	if !ok || title == "" {
		return 0, false
	}
	number, err := strconv.Atoi(n)
	return number, err == nil && number > 0
}

// branchPrefix is the prefix as it appears in a branch name: with its slash, and empty when
// there is none, which is what an unset config on a machine with no $USER comes to. A branch
// of plain `<issue>-<title>` is a fine answer there; inventing a name is not.
func branchPrefix(prefix string) string {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return ""
	}
	return prefix + "/"
}

func runNew(args []string) error {
	number, err := strconv.Atoi(args[0])
	if err != nil || number < 1 {
		return fmt.Errorf("issue must be a positive number, got %q", args[0])
	}
	cfg, err := Resolve(flagHost, flagRemoteDir)
	if err != nil {
		return err
	}
	issue, err := fetchIssue(number)
	if err != nil {
		return err
	}
	branch := issueBranch(cfg.BranchPrefix, number, issue.Title)
	target := Target{Owner: issue.Owner, Repo: issue.Repo, Number: number}
	remotePath := target.issuePath(cfg.RemoteDir)

	if flagDryRun {
		fmt.Printf("dry run: would create %s on this Mac and in %s:~/projects/%s\n", branch, cfg.Host, issue.Repo)
		fmt.Printf("dry run: would write %s:%s/issue.md\n", cfg.Host, remotePath)
		return nil
	}

	g := gitCmd{out: os.Stdout}
	if dirty, _ := g.capture("status", "--porcelain"); dirty != "" {
		return fmt.Errorf("Mac checkout has uncommitted changes")
	}
	if err := g.run("fetch", flagRemote); err != nil {
		return err
	}
	if g.refExists("refs/heads/" + branch) {
		return fmt.Errorf("branch %s already exists on the Mac", branch)
	}

	dir, err := os.MkdirTemp("", "sand-new-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	body := fmt.Sprintf("# %s\n\n%s\n\n%s\n", issue.Title, issue.URL, strings.TrimSpace(issue.Body))
	if err := os.WriteFile(filepath.Join(dir, "issue.md"), []byte(body), 0o644); err != nil {
		return err
	}
	if err := sendDir(cfg.Host, dir, remotePath); err != nil {
		return err
	}

	repoDir := checkoutDir(Target{Repo: issue.Repo})
	remote := fmt.Sprintf("cd %s && test -z \"$(git status --porcelain)\" && git fetch %s && ! git show-ref --verify --quiet %s && git switch -c %s %s",
		remoteQuote(repoDir), shellQuote(flagRemote), shellQuote("refs/heads/"+branch),
		shellQuote(branch), shellQuote(flagRemote+"/"+flagBase))
	cmd := exec.Command(sshBin(), cfg.Host, remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("creating %s in %s:%s: %w (%s)", branch, cfg.Host, repoDir, err, strings.TrimSpace(string(out)))
	}
	if err := g.run("switch", "-c", branch, flagRemote+"/"+flagBase); err != nil {
		return err
	}
	// Nobody starts an agent for an issue: a person ssh's in and asks one. So this is the
	// command that has to leave the skill current there, or that agent reads issue.md with no
	// idea what the ring is, and pushes.
	if err := ensureRemoteSkill(cfg, os.Stdout); err != nil {
		return err
	}
	fmt.Printf("%s #%d → %s:%s\nbranch: %s\n", issue.Slug(), issue.Number, cfg.Host, remotePath, branch)
	return nil
}

func setupUp(args []string) (Config, Target, bool, error) {
	cfg, err := Resolve(flagHost, flagRemoteDir)
	if err != nil {
		return cfg, Target{}, false, err
	}
	arg, err := prArg(args)
	if err != nil {
		return cfg, Target{}, false, err
	}
	if arg != "" {
		target, err := ResolveTarget(arg)
		if err != nil {
			return cfg, Target{}, false, err
		}
		if err := target.LoadURL(); err != nil {
			return cfg, Target{}, false, err
		}
		return cfg, target, false, nil
	}
	target, found, err := currentBranchPR()
	if err != nil {
		return cfg, Target{}, false, err
	}
	if found {
		return cfg, target, false, nil
	}

	branch := target.Branch
	number, ok := issueNumberFromBranch(cfg.BranchPrefix, branch)
	if !ok {
		return cfg, Target{}, false, fmt.Errorf("no open PR for branch %q in %s and its name does not identify an issue "+
			"(want %s<issue>-<title>; `sand config set branch_prefix <yours>` if that prefix is not yours)",
			branch, target.Slug(), branchPrefix(cfg.BranchPrefix))
	}
	issue, issueErr := fetchIssue(number)
	if issueErr != nil {
		return cfg, Target{}, false, issueErr
	}
	return cfg, Target{Owner: issue.Owner, Repo: issue.Repo, Number: number, Title: issue.Title, URL: issue.URL, Branch: branch}, true, nil
}

func loadPRDescription(cfg Config, target Target) ([]byte, error) {
	dir, err := fetchDir(cfg.Host, target.issuePath(cfg.RemoteDir))
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	body, err := os.ReadFile(filepath.Join(dir, "pr-description.md"))
	if err != nil || strings.TrimSpace(string(body)) == "" {
		return nil, fmt.Errorf("%s:%s/pr-description.md is missing or empty; have the sandbox agent write the PR description first", cfg.Host, target.issuePath(cfg.RemoteDir))
	}
	return body, nil
}

func createPullRequest(target Target, description []byte) (Target, error) {
	f, err := os.CreateTemp("", "sand-pr-description-*.md")
	if err != nil {
		return Target{}, err
	}
	body := f.Name()
	defer os.Remove(body)
	if _, err := f.Write(description); err != nil {
		f.Close()
		return Target{}, err
	}
	if err := f.Close(); err != nil {
		return Target{}, err
	}
	url, err := gh("pr", "create", "--repo", target.Slug(), "--head", target.Branch, "--base", flagBase,
		"--title", target.Title, "--body-file", body)
	if err != nil {
		return Target{}, err
	}
	created, err := ResolveTarget(strings.TrimSpace(url))
	if err != nil {
		return Target{}, err
	}
	if err := created.LoadURL(); err != nil {
		return Target{}, err
	}
	return created, nil
}
