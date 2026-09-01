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

	// Box is the box's checkout of this repo as a git URL (host:projects/<repo>). The
	// rewrite is put back there once it is on the remote: see alignBox. Empty skips that,
	// which is a machine with no box configured, not a normal run.
	Box string

	// AllowOtherAuthors signs commits whose author or committer is not this machine's git
	// identity. Off by default: see checkSigningIdentity.
	AllowOtherAuthors bool
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

	// BoxAligned is whether the box now holds the history this run signed. False with
	// Pushed true is the state that used to go unnoticed and is worth reporting: GitHub has
	// the signed branch and the box is still on the lineage it stopped existing on.
	BoxAligned bool
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

	// aif takes the branch as its only argument, so a word that is not a branch is a word aif
	// is free to read as one of its own subcommands: `sand sign push` pushed this machine's
	// HEAD to the box and then failed on `git switch push`. The name has to be a branch
	// something can see before it is handed over. Not the current branch, which exists by
	// definition, and not a branch that is only on the box when there is no box to ask.
	if o.Branch != "" && !g.refExists("refs/heads/"+branch) && !g.refExists(o.Remote+"/"+branch) && !onBox(g, o, branch) {
		return res, fmt.Errorf("no branch %s: not here, not on %s, not on the box", branch, o.Remote)
	}
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
	imported := head // what the box had when this run started, for alignBox
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

	// A signature is a statement about content, and until now the operator was shown hashes
	// and subject lines: the summary of work an agent did on another machine, not the work.
	// The diffstat is the cheapest thing that answers "what am I putting my name on".
	fmt.Fprintf(o.Out, "\nWhat the signature attests to, %s..%s:\n", base, short(head))
	if err := g.run("diff", "--stat", base+"..HEAD"); err != nil {
		return res, err
	}

	// Before the recovery branch and before the prompt: a refusal is not something to make
	// the operator answer a question about first.
	if !o.AllowOtherAuthors {
		if err := checkSigningIdentity(g, commits, dirty); err != nil {
			return res, err
		}
	}
	if err := checkPreSigningLineage(g, dirty, o.Remote, o.Base, branch, head, o.Box); err != nil {
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
		if o.Box != "" {
			fmt.Fprintf(o.Out, "  git push --force-with-lease %s %s   # and the box, or it keeps building on %s\n",
				o.Box, branch, short(imported))
		}
		return res, nil
	}
	if err := g.run("push", "--force-with-lease", o.Remote, branch); err != nil {
		return res, err
	}
	res.Pushed = true
	res.BoxAligned = alignBox(g, o, branch, imported, head)
	return res, nil
}

// onBox answers whether the box has this branch, and false when there is no box configured or
// it cannot be reached: the caller is deciding whether to hand a name to aif, and an unreachable
// box is not evidence that the name is wrong.
func onBox(g gitCmd, o SignOpts, branch string) bool {
	if o.Box == "" {
		return false
	}
	refs, err := g.capture("ls-remote", o.Box, "refs/heads/"+branch)
	return err == nil && strings.TrimSpace(refs) != ""
}

// alignBox puts the box's branch on the history this run just signed.
//
// The rewrite is the only thing that moves history on the Mac, and it changes every hash it
// touches, so a box left on the pre-signing lineage is on a chain that no longer exists
// anywhere else. It keeps committing there, and the next round arrives with unsigned copies of
// commits that are already signed and pushed: two chains, same trees, different hashes, and
// nothing in either machine's `git log` says so. That is not a state to detect and recover
// from, it is a state not to enter, so the realignment is part of signing rather than a step
// someone has to remember. checkPreSigningLineage is only the tripwire for the paths that skip
// it (a declined push, a box whose tree was dirty).
//
// The box is where the code is written, so nothing here force-pushes on faith. The box's branch
// has to be exactly what this run imported, or already an ancestor of the result; anything else
// means the box committed while signing ran and those commits exist nowhere else. The push
// leases against the hash that was just read, so a box that moves in between is a rejection
// rather than a loss.
func alignBox(g gitCmd, o SignOpts, branch, imported, head string) bool {
	if o.Box == "" {
		fmt.Fprintf(o.Out, "\nNo box configured, so %s there still points at %s: `sand config set host <alias>`.\n",
			branch, short(imported))
		return false
	}

	fmt.Fprintf(o.Out, "\nRealigning the box, which is still on the pre-signing %s:\n", short(imported))
	refs, err := g.capture("ls-remote", o.Box, "refs/heads/"+branch)
	if err != nil {
		fmt.Fprintf(o.Out, "  could not read %s: %v\n", o.Box, err)
		fmt.Fprintf(o.Out, "  %s has the signed branch; the box does not. Re-run when it answers.\n", o.Remote)
		return false
	}
	boxHead, _, _ := strings.Cut(strings.TrimSpace(refs), "\t")

	lease := []string{"--force-with-lease=refs/heads/" + branch + ":" + boxHead}
	switch {
	case boxHead == "":
		lease = nil // not there yet, so there is nothing to overwrite and nothing to lease
	case boxHead == head:
		fmt.Fprintf(o.Out, "  already at %s\n", short(head))
		return true
	case boxHead == imported:
		// Exactly what aif brought over: the rewrite is the only difference.
	default:
		// Anything else has to be proved harmless before it is overwritten. Behind the signed
		// history is fine; ahead of it is the box's own work and only the box has it.
		if err := g.run("fetch", "--quiet", o.Box, branch); err != nil ||
			exec.Command("git", "merge-base", "--is-ancestor", boxHead, head).Run() != nil {
			fmt.Fprintf(o.Out, "  %s is at %s there, which is neither the %s this run imported nor\n"+
				"  an ancestor of the signed %s: it committed while signing ran, and those commits are\n"+
				"  only on the box. Nothing was overwritten. On the box, put its new commits on top of\n"+
				"  the signed branch, then re-run:\n"+
				"    git fetch %s %s && git rebase --onto FETCH_HEAD %s %s\n",
				branch, short(boxHead), short(imported), short(head), o.Remote, branch, short(imported), branch)
			return false
		}
	}

	args := append([]string{"push"}, lease...)
	if err := g.run(append(args, o.Box, branch)...); err != nil {
		fmt.Fprintf(o.Out, "  push to %s failed: %v\n", o.Box, err)
		fmt.Fprintf(o.Out, "  denyCurrentBranch in that message: run once on the box,\n"+
			"    git -C projects/<repo> config receive.denyCurrentBranch updateInstead\n"+
			"  the working tree in it: the box has uncommitted changes, which that setting refuses\n"+
			"  rather than overwrite. Commit or stash them there and re-run.\n")
		return false
	}
	fmt.Fprintf(o.Out, "  %s %s → %s\n", o.Box, branch, short(head))
	return true
}

// checkPreSigningLineage refuses to sign a commit whose signed twin is already on the pushed
// branch. Identical tree and identical subject is what a signing rewrite leaves behind, so a
// match means this branch was built on the lineage the last round replaced: signing it again
// would push a second copy of work that is already there, and every hash quoted in an earlier
// reply would be joined by a duplicate.
//
// alignBox is what keeps this from happening. This is the stop for the runs that got past it:
// a push the operator declined, a box whose tree was dirty when the realignment came, a branch
// someone moved by hand.
//
// The recovery it prints has to run here and end on the box, in that order, because `aif` resets
// this checkout to the box's branch at the top of every run: a rebase done here and not pushed
// to the box is undone by the next `sand sign` before it reads anything. Only the Mac can do the
// rebase at all, since only the Mac can see <remote>/<branch>.
//
// <remote>/<base> is checked as well as <remote>/<branch>, and it is the worse of the two. A twin
// on the branch is undone by replacing the branch; a twin on main is merged, permanent, and
// already carries whatever signature it was given. This repo grew seven such pairs, same trees,
// both copies signed, one set on main and one on the branch, from two rounds signing the same box
// originals. Nothing said so until the two refused to merge.
func checkPreSigningLineage(g gitCmd, dirty []string, remote, base, branch, head, box string) error {
	remoteBranch, remoteBase := remote+"/"+branch, remote+"/"+base
	twins, err := duplicatedOnRemote(g, dirty, remoteBranch, remoteBase)
	if err != nil {
		return err
	}
	if len(twins) == 0 {
		return nil
	}

	// dirty is oldest first, so the last commit with a twin is the boundary: everything above
	// it is the work that exists only here, and is what has to be replayed.
	var dups []string
	boundary, merged := "", false
	for _, d := range twins {
		dups = append(dups, fmt.Sprintf("  %s is an unsigned copy of %s on %s: %q",
			short(d.SHA), short(d.Twin), d.On, d.Subject))
		boundary = d.SHA
		merged = merged || d.On == remoteBase
	}

	// A commit whose twin is on the base is already merged, so it has to go rather than move:
	// rebasing onto the base drops it, since git skips what is upstream by patch id.
	fix := fmt.Sprintf("  git rebase --onto %s %s %s", remoteBranch, short(boundary), branch)
	if merged {
		fix = fmt.Sprintf("  git rebase %s %s   # drops the ones already merged", remoteBase, branch)
	}
	if box != "" {
		// Leased against the head this run imported, which is the box's, so a box that has
		// moved since rejects the push instead of losing the commits that moved it.
		fix += fmt.Sprintf("\n  git push --force-with-lease=refs/heads/%s:%s %s %s\n"+
			"  sand sign --push", branch, head, box, branch)
	}
	where := remoteBranch
	if merged {
		where = remoteBase + " or " + remoteBranch
	}
	return fmt.Errorf("refusing to sign: %d commit(s) are unsigned copies of commits already on %s.\n%s\n"+
		"This branch was built on a lineage an earlier signing round replaced, so signing it would put a\n"+
		"second copy of work that is already there on the remote. A twin on a branch goes away when the\n"+
		"branch is replaced; a twin on the base is merged and permanent. Drop or replay what is duplicated,\n"+
		"here on the Mac, then give the box the result before signing again: aif resets this checkout to\n"+
		"the box's branch, so a rebase the box has not been told about is undone by the next run.\n%s",
		len(dups), where, strings.Join(dups, "\n"), fix)
}

// dupCommit is one commit whose twin is already on the remote: same tree, same subject, a
// different hash because one of the two carries a signature.
type dupCommit struct {
	SHA     string
	Twin    string
	On      string // the ref the twin sits on
	Subject string
}

// duplicatedOnRemote finds, for each of commits, a twin already pushed. Two callers ask it in
// opposite directions and it has to be one implementation: signing refuses to add a second copy
// of work that is already there, and status reports the same state before anyone runs signing.
//
// The base is checked second so a commit with a twin on both refs is reported against the base,
// which is the worse of the two: a twin on a branch goes away when the branch is replaced, a
// twin on the base is merged and permanent.
func duplicatedOnRemote(g gitCmd, commits []string, remoteBranch, remoteBase string) ([]dupCommit, error) {
	pushed := map[string][]string{}
	on := map[string]string{} // identity -> the ref its twin sits on
	for _, ref := range []string{remoteBranch, remoteBase} {
		if !g.refExists(ref) {
			continue
		}
		ids, err := g.identities(ref)
		if err != nil {
			return nil, err
		}
		for id, shas := range ids {
			pushed[id], on[id] = shas, ref
		}
	}

	var dups []dupCommit
	for _, sha := range commits {
		id, err := g.identity(sha)
		if err != nil {
			return nil, err
		}
		if twins := pushed[id]; len(twins) > 0 {
			dups = append(dups, dupCommit{SHA: sha, Twin: twins[0], On: on[id], Subject: identitySubject(id)})
		}
	}
	return dups, nil
}

// commitIdentity is what a signing rewrite preserves and a hash does not: the tree and the
// subject. Two commits sharing it are the same work, signed and unsigned.
const commitIdentity = "%T%x00%s"

func (g gitCmd) identity(rev string) (string, error) {
	return g.capture("show", "-s", "--format="+commitIdentity, rev)
}

// identities maps that key to the commits on a ref that carry it. Bounded like the scan in
// commitOnBranch: a review branch is tens of commits, and reading a repository's whole history
// to answer this is not worth the wait.
func (g gitCmd) identities(ref string) (map[string][]string, error) {
	list, err := g.capture("log", "--format=%H%x00"+commitIdentity, "-n", "500", ref)
	if err != nil {
		return nil, err
	}
	byID := make(map[string][]string)
	for _, line := range strings.Split(list, "\n") {
		sha, id, ok := strings.Cut(line, "\x00")
		if ok {
			byID[id] = append(byID[id], sha)
		}
	}
	return byID, nil
}

func identitySubject(id string) string {
	_, subject, _ := strings.Cut(id, "\x00")
	return subject
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

// checkSigningIdentity refuses to put this machine's key on a commit somebody else made.
// `aif` imports whatever the box's branch holds, and a merge of another branch, a cherry-pick
// or an agent with a different git config all put commits into the branch-unique set that this
// operator never wrote. filter-branch keeps their author and committer, so the result reads
// "written by them, vouched for by you", which is a claim nobody made on purpose.
//
// It runs whatever `--yes` says: that flag exists to skip a question about work the operator
// already knows about, not to widen what their key attests to. Only the commits being rewritten
// are checked; the already-signed ones keep whatever signature they arrived with.
func checkSigningIdentity(g gitCmd, commits []branchCommit, dirty []string) error {
	me, err := g.capture("config", "user.email")
	if err != nil || me == "" {
		return fmt.Errorf("no git user.email is configured, so there is no identity to sign as")
	}
	rewriting := make(map[string]bool, len(dirty))
	for _, sha := range dirty {
		rewriting[sha] = true
	}

	var lines []string
	for _, c := range commits {
		if !rewriting[c.SHA] {
			continue
		}
		for _, f := range []struct{ field, email string }{
			{"author", c.Author}, {"committer", c.Committer},
		} {
			if !strings.EqualFold(f.email, me) {
				who := f.email
				if who == "" {
					who = "(none)"
				}
				lines = append(lines, fmt.Sprintf("  %s  %s %s", short(c.SHA), f.field, who))
			}
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to sign as %s: these commits were made by someone else, and the "+
		"signature would say you vouch for them\n%s\ndrop them from the branch, or pass "+
		"--allow-other-authors if you do vouch for them", me, strings.Join(lines, "\n"))
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
	commitCurrent   commitState = iota // it is on the pushed branch; quote it as is
	commitMoved                        // signing replaced it; quote the returned hash instead
	commitGone                         // not on the branch, and nothing on the branch matches it
	commitAmbiguous                    // more than one commit on the branch matches it
	commitUnknown                      // git here cannot say: no such ref, no such object
)

// commitOnBranch says which hash a reply should quote. The agent on the box commits without
// a key, writes that hash into the thread file, and then signing re-creates the commit — so
// the recorded hash stops existing exactly when the branch becomes postable. The replacement
// carries the same tree and the same message, which is what identifies it.
//
// commitUnknown is deliberately not a failure: a hash this checkout cannot reason about is
// not evidence that it is wrong, and stalling the loop over it costs more than it saves.
// Two matches is a different thing and is a failure: tree plus subject is an identity claim,
// and a duplicated commit (a cherry-pick, a merge that brought a copy back, a branch signed
// twice) means the claim is false. Quoting either one is a coin flip, and a reply that points
// a reviewer at the wrong commit is worse than a reply that has not been posted yet.
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

	want, err := g.identity(full)
	if err != nil {
		return recorded, commitUnknown
	}
	onBranch, err := g.identities(branchRef)
	if err != nil {
		return recorded, commitUnknown
	}
	var matches []string
	for _, sha := range onBranch[want] {
		matches = append(matches, short(sha))
	}
	switch len(matches) {
	case 0:
		return recorded, commitGone
	case 1:
		return matches[0], commitMoved
	default:
		return strings.Join(matches, ", "), commitAmbiguous
	}
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
	SHA       string
	Parents   []string
	Signed    bool
	Author    string // email, for the identity check
	Committer string // email
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
		commits = append(commits, branchCommit{
			SHA: f[0], Parents: f[1:], Signed: hasSignature(raw),
			Author: headerEmail(raw, "author"), Committer: headerEmail(raw, "committer"),
		})
	}
	return commits, nil
}

// headerEmail reads the address out of a commit header field ("author", "committer"). Header
// only, like hasSignature: a message line beginning "author " is text, not an identity.
func headerEmail(raw, field string) string {
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			return ""
		}
		v, ok := strings.CutPrefix(line, field+" ")
		if !ok {
			continue
		}
		_, rest, ok := strings.Cut(v, "<")
		if !ok {
			return ""
		}
		email, _, _ := strings.Cut(rest, ">")
		return email
	}
	return ""
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
