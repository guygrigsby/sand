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
	DryRun bool // run the checks, show what would be signed, rewrite nothing
	In     io.Reader
	Out    io.Writer
}

// SignResult is what a run did. `up` reports it step by step, and a caller can tell an
// already-signed branch (nothing rewritten, nothing pushed) from one that just moved.
type SignResult struct {
	Branch    string
	Base      string // the remote/base it compared against
	Head      string // the branch tip afterwards
	Total     int    // commits unique to the branch
	Rewritten int    // of those, the ones this run signed
	Kept      int    // of those, the ones already signed, whose hashes did not move
	Pushed    bool
	Cancelled bool // the operator declined the rewrite, so nothing happened
}

// Sign imports the branch, signs the commits it adds over the base that are not signed
// already, verifies the result and offers to push. It returns having done nothing when there
// is nothing to sign.
//
// DryRun stops before the rewrite, so no history changes and nothing is pushed. It still
// imports and fetches: which commits would be signed is not knowable without them.
func Sign(o SignOpts) (SignResult, error) {
	g := gitCmd{out: o.Out}
	var res SignResult
	var answers *bufio.Reader
	if o.In != nil {
		answers = bufio.NewReader(o.In)
	}

	gitDir, err := g.capture("rev-parse", "--absolute-git-dir")
	if err != nil {
		return res, fmt.Errorf("run inside a git repository")
	}

	branch := o.Branch
	if branch == "" {
		if branch, err = g.capture("branch", "--show-current"); err != nil || branch == "" {
			return res, fmt.Errorf("detached HEAD: name the branch to sign")
		}
	}
	if protectedBranches[branch] || strings.HasPrefix(branch, "release/") {
		return res, fmt.Errorf("refusing to rewrite protected branch %s", branch)
	}
	if err := checkInProgress(gitDir); err != nil {
		return res, err
	}
	if err := g.checkClean(); err != nil {
		return res, err
	}

	// A missing aif is not a reason to reach for some other way of moving the branch: the
	// wrong branch signed with the right key is worse than a clear stop.
	if _, err := exec.LookPath(aifBin()); err != nil {
		return res, fmt.Errorf("%s is required to import the branch and is not on PATH", aifBin())
	}

	base := o.Remote + "/" + o.Base
	res.Branch, res.Base = branch, base
	if err := g.stream(exec.Command(aifBin(), branch)); err != nil {
		return res, fmt.Errorf("%s %s: %w", aifBin(), branch, err)
	}
	if err := g.run("fetch", o.Remote); err != nil {
		return res, err
	}
	if _, err := g.capture("rev-parse", "--verify", "--quiet", base); err != nil {
		return res, fmt.Errorf("base ref not found: %s", base)
	}
	if err := g.run("switch", branch); err != nil {
		return res, err
	}
	if _, err := g.capture("merge-base", base, "HEAD"); err != nil {
		return res, fmt.Errorf("no common history between %s and %s; refusing to rewrite", branch, base)
	}

	head, err := g.capture("rev-parse", "HEAD")
	if err != nil {
		return res, err
	}
	res.Head = head
	commits, err := g.branchCommits(head, base)
	if err != nil {
		return res, err
	}
	count := len(commits)
	res.Total = count
	if count == 0 {
		fmt.Fprintf(o.Out, "No commits unique to %s; nothing to sign.\n", branch)
		return res, nil
	}

	// Review is a loop, so most runs arrive at a branch that is already partly signed. Only
	// the unsigned commits and their descendants get re-created; everything below the oldest
	// unsigned commit keeps the hash a reply posted in an earlier round already quotes.
	dirty, clean := splitBySigning(commits)
	res.Rewritten, res.Kept = len(dirty), len(clean)

	baseShort, _ := g.capture("rev-parse", "--short", base)
	fmt.Fprintf(o.Out, "Branch: %s (%s)\n", branch, short(head))
	fmt.Fprintf(o.Out, "Comparison base: %s (%s)\n", base, baseShort)
	if len(dirty) == 0 {
		fmt.Fprintf(o.Out, "All %d commit(s) unique to %s are signed already; nothing to sign.\n", count, branch)
		return res, nil
	}
	fmt.Fprintf(o.Out, "About to sign %d of %d commit(s) unique to %s\n", len(dirty), count, branch)
	if len(clean) > 0 {
		fmt.Fprintf(o.Out, "%d already signed, keeping their hashes (%s)\n", len(clean), shortList(clean, 5))
	}
	fmt.Fprintf(o.Out, "Commit topology, including merges, will be preserved.\n\n")
	if err := g.run("log", "--graph", "--format=%h  %G?  %s", head, "--not", base); err != nil {
		return res, err
	}
	if o.DryRun {
		fmt.Fprintf(o.Out, "\ndry run: history not rewritten, nothing pushed to %s\n", o.Remote)
		return res, nil
	}

	// Two runs in the same second are normal in a review loop, and the second one must not
	// fail on the name the first one took.
	stem := fmt.Sprintf("%s-before-signing-%s", strings.ReplaceAll(branch, "/", "-"), time.Now().Format("20060102150405"))
	backup := stem
	for i := 2; g.refExists("refs/heads/" + backup); i++ {
		backup = fmt.Sprintf("%s-%d", stem, i)
	}
	if err := g.run("branch", backup, head); err != nil {
		return res, err
	}
	restore := fmt.Sprintf("restore with: git switch %s && git reset --hard %s", branch, backup)
	fmt.Fprintf(o.Out, "\nRecovery branch: %s\n", backup)

	if !o.Yes {
		if !confirm(answers, o.Out, "\nProceed?") {
			fmt.Fprintln(o.Out, "Cancelled. No changes made.")
			res.Rewritten, res.Cancelled = 0, true
			return res, nil
		}
	}

	// filter-branch supplies the original tree, the rewritten parents, the message and both
	// identities; -S is the only thing being added. The already-signed commits go in as
	// negative revs, so filter-branch leaves them alone and maps them to themselves when it
	// rewrites the parents of the ones above.
	args := append([]string{"filter-branch", "-f",
		"--commit-filter", `git commit-tree -S "$@"`,
		"--", branch, "--not", base}, clean...)
	rewrite := exec.Command("git", args...)
	rewrite.Env = append(os.Environ(), "FILTER_BRANCH_SQUELCH_WARNING=1")
	if err := g.stream(rewrite); err != nil {
		return res, fmt.Errorf("signing rewrite failed, nothing was pushed: %w\n%s", err, restore)
	}
	if err := g.run("switch", branch); err != nil {
		return res, err
	}

	after, err := g.branchCommits(branch, base)
	if err != nil {
		return res, err
	}
	var unsigned []string
	present := make(map[string]bool, len(after))
	for _, c := range after {
		present[c.SHA] = true
		if !c.Signed {
			unsigned = append(unsigned, c.SHA)
		}
	}
	if len(unsigned) > 0 {
		fmt.Fprintf(o.Out, "%d commit(s) still have no signature:\n", len(unsigned))
		for _, c := range unsigned {
			_ = g.run("show", "-s", "--format=  %h %s", c)
		}
		return res, fmt.Errorf("nothing was pushed; %s", restore)
	}
	if len(after) != count {
		return res, fmt.Errorf("commit count changed from %d to %d, refusing to push; %s", count, len(after), restore)
	}
	// The whole reason for signing incrementally: those hashes are quoted in replies that
	// are already on GitHub. If one of them moved anyway, the rewrite did something else
	// than it was told to.
	for _, sha := range clean {
		if !present[sha] {
			return res, fmt.Errorf("already-signed commit %s is no longer on %s, refusing to push; %s",
				short(sha), branch, restore)
		}
	}

	head, err = g.capture("rev-parse", "HEAD")
	if err != nil {
		return res, err
	}
	res.Head = head
	fmt.Fprintf(o.Out, "\nVerified: all %d branch-unique commit(s) contain signatures", count)
	if len(clean) > 0 {
		fmt.Fprintf(o.Out, ", and the %d already-signed one(s) still have their hashes", len(clean))
	}
	fmt.Fprintln(o.Out, ".")
	if err := g.run("log", "--graph", "--format=%h  status=%G?  signer=%GS  %s", branch, "--not", base); err != nil {
		return res, err
	}
	fmt.Fprintf(o.Out, "\nRecovery branch retained as: %s\n", backup)

	push := o.Push
	if !push {
		push = confirm(answers, o.Out, fmt.Sprintf("Push %s to %s with --force-with-lease?", branch, o.Remote))
	}
	if !push {
		fmt.Fprintf(o.Out, "Not pushed. When ready:\n  git push --force-with-lease %s %s\n", o.Remote, branch)
		return res, nil
	}
	if err := g.run("push", "--force-with-lease", o.Remote, branch); err != nil {
		return res, err
	}
	res.Pushed = true
	return res, nil
}

// splitBySigning divides the branch-unique commits into the ones the rewrite has to touch and
// the ones it must leave alone. A commit needs re-creating when it carries no signature, and
// also when anything below it does: its parent hash changes, so the commit object changes and
// the signature has to be made over the new bytes. Everything whose ancestry inside the
// branch is fully signed keeps the hash it has.
//
// commits must be oldest first, so a parent is decided before its children.
func splitBySigning(commits []branchCommit) (dirty, clean []string) {
	moved := make(map[string]bool, len(commits))
	for _, c := range commits {
		needs := !c.Signed
		for _, p := range c.Parents {
			if moved[p] {
				needs = true
			}
		}
		if !needs {
			clean = append(clean, c.SHA)
			continue
		}
		moved[c.SHA] = true
		dirty = append(dirty, c.SHA)
	}
	return dirty, clean
}

func shortList(shas []string, max int) string {
	var parts []string
	for i, s := range shas {
		if i == max {
			parts = append(parts, fmt.Sprintf("and %d more", len(shas)-max))
			break
		}
		parts = append(parts, short(s))
	}
	return strings.Join(parts, ", ")
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

// commitState is what became of the hash an agent on the box wrote into a thread file.
type commitState int

const (
	commitCurrent commitState = iota // it is on the pushed branch; quote it as is
	commitMoved                      // signing replaced it; quote the returned hash instead
	commitGone                       // not on the branch, and nothing on the branch matches it
	commitUnknown                    // git here cannot say: no such ref, no such object
)

// commitOnBranch says which hash a reply should quote. The agent on the box commits without
// a key, writes that hash into the thread file, and then signing re-creates the commit — so
// the recorded hash stops existing exactly when the branch becomes postable. The replacement
// carries the same tree and the same message, which is what identifies it.
//
// commitUnknown is deliberately not a failure: a hash this checkout cannot reason about is
// not evidence that it is wrong, and stalling the loop over it costs more than it saves.
func commitOnBranch(g gitCmd, recorded, branchRef string) (string, commitState) {
	if !g.refExists(branchRef) {
		return recorded, commitUnknown
	}
	full, err := g.capture("rev-parse", "--verify", recorded+"^{commit}")
	if err != nil {
		return recorded, commitUnknown
	}
	if exec.Command("git", "merge-base", "--is-ancestor", full, branchRef).Run() == nil {
		return recorded, commitCurrent
	}

	want, err := g.capture("show", "-s", "--format=%T%x00%s", full)
	if err != nil {
		return recorded, commitUnknown
	}
	// Bounded: a review branch is tens of commits, and scanning a repository's whole history
	// to rescue one hash is not worth the wait.
	list, err := g.capture("log", "--format=%H%x00%T%x00%s", "-n", "500", branchRef)
	if err != nil {
		return recorded, commitUnknown
	}
	for _, line := range strings.Split(list, "\n") {
		sha, rest, ok := strings.Cut(line, "\x00")
		if ok && rest == want {
			return short(sha), commitMoved
		}
	}
	return recorded, commitGone
}

func (g gitCmd) refExists(ref string) bool {
	_, err := g.capture("rev-parse", "--verify", "--quiet", ref)
	return err == nil
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

// branchCommit is one commit the branch adds over the base.
type branchCommit struct {
	SHA     string
	Parents []string
	Signed  bool
}

// branchCommits lists them oldest first, with their parents and whether they already carry a
// signature. The signature test is the commit header rather than %G?, because %G? answers
// "can this machine verify it", which needs a key ring and a trust config; what decides
// whether a commit has to be re-signed is only whether the header is there at all. The header
// is gpgsig for both OpenPGP and ssh signing, gpgsig-sha256 in a sha256 repository.
func (g gitCmd) branchCommits(rev, base string) ([]branchCommit, error) {
	list, err := g.capture("rev-list", "--reverse", "--parents", rev, "--not", base)
	if err != nil {
		return nil, err
	}
	var commits []branchCommit
	for _, line := range strings.Split(list, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		raw, err := g.capture("cat-file", "commit", f[0])
		if err != nil {
			return nil, err
		}
		commits = append(commits, branchCommit{SHA: f[0], Parents: f[1:], Signed: hasSignature(raw)})
	}
	return commits, nil
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
