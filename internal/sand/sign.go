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
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

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

	// BoxHost and BoxDir address that same checkout over ssh rather than as a push URL,
	// because checkBoxCanReceive has to ask it questions and a push URL cannot carry one.
	// Empty skips the pre-flight.
	BoxHost, BoxDir string

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

	// The Mac's current branch is a guess at what to sign, and `--push` acts on the guess
	// without asking: this checkout on one branch and the box on another is how a branch nobody
	// was working on got signed, force-pushed and realigned. When no branch was named, the two
	// machines have to agree about which one it is.
	//
	// Neither --yes nor --push covers this, for the same reason checkSigningIdentity is not
	// covered by --yes: those flags answer for work the operator named, and this is the name
	// itself. It is a stop rather than a prompt because a prompt is exactly what `--push` was
	// asked to skip, and because confirm defaults to no, so an unattended run would answer
	// "don't push" and look like a successful signing round.
	//
	// A box that cannot say which branch it is on (a detached HEAD, a bare repo, no answer at
	// all) is not evidence of disagreement. A box that cannot hand the branch over is
	// importBranch's stop, with its own message.
	if o.Branch == "" && o.Box != "" {
		if boxBranch, err := boxCurrentBranch(g, o.Box); err == nil && boxBranch != "" && boxBranch != branch {
			return res, fmt.Errorf("refusing to sign %s: no branch was named, this checkout is on %s and the\n"+
				"box is on %s, so which one this run means is a guess, and signing pushes what it guesses.\n"+
				"  sand sign %s                  signs the branch the box is on\n"+
				"  git switch %s && sand sign    follows the box here first\n"+
				"--yes and --push do not cover this: they answer for a branch you named.",
				branch, branch, boxBranch, boxBranch, boxBranch)
		}
	}

	base := o.Remote + "/" + o.Base
	res.Branch, res.Base = branch, base

	// A name nothing can resolve is a typo, and `sand sign push` (a slip for `--push`) is the
	// one that happened: the word reached the import as a branch name. Answering it here names
	// the branch and the three places it is not, which is a better error than a failed fetch of
	// it. Not the current branch, which exists by definition, and not a branch that is only on
	// the box when there is no box to ask.
	if o.Branch != "" && !g.refExists("refs/heads/"+branch) && !g.refExists(o.Remote+"/"+branch) && !onBox(g, o, branch) {
		return res, fmt.Errorf("no branch %s: not here, not on %s, not on the box", branch, o.Remote)
	}
	// Leaves HEAD on the branch, which is why nothing below switches to it.
	if err := importBranch(g, o, branch); err != nil {
		return res, err
	}
	if err := g.run("fetch", o.Remote); err != nil {
		return res, fmt.Errorf("fetching %s failed, and what is already signed and pushed there is what "+
			"decides the rest of this run, so nothing was rewritten: %w", o.Remote, err)
	}
	if _, err := g.capture("rev-parse", "--verify", "--quiet", base); err != nil {
		return res, fmt.Errorf("base ref not found: %s (`--base <branch>` if this repo's is not %s)", base, o.Base)
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
		return res, publish(g, o, answers, &res, branch, imported, head)
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
	// Before the lineage check, not after it: the recovery that check prints ends in a push to
	// the box, so a box that cannot receive one makes that advice useless too.
	if err := checkBoxCanReceive(o); err != nil {
		return res, err
	}
	if err := checkPreSigningLineage(g, dirty, o.Remote, o.Base, branch, head, o.Box); err != nil {
		var le *lineageError
		if !errors.As(err, &le) || o.DryRun ||
			!confirm(answers, o.Out, "\nDrop the duplicated commit(s), replay the rest on the pushed branch, and continue?") {
			return res, err
		}
		head, commits, dirty, clean, err = repairLineage(g, o, le, branch, base, head)
		if err != nil {
			return res, err
		}
		count = len(commits)
		res.Total, res.Head = count, head
		res.Rewritten, res.Kept = len(dirty), len(clean)
		imported = head // the box was just given exactly this
		if len(dirty) == 0 {
			// The Mac signs what a rebase replays (commit.gpgsign), so the repair can
			// leave nothing to do.
			fmt.Fprintf(o.Out, "All %d commit(s) unique to %s are signed already; nothing to sign.\n", count, branch)
			return res, publish(g, o, answers, &res, branch, imported, head)
		}
		fmt.Fprintf(o.Out, "Repaired: %s is now %s, %d commit(s) to sign.\n", branch, short(head), len(dirty))
	}
	if o.DryRun {
		fmt.Fprintf(o.Out, "\ndry run: history not rewritten, nothing pushed to %s\n", o.Remote)
		return res, nil
	}

	backup, err := keepAt(g, branch, "before-signing", head)
	if err != nil {
		return res, fmt.Errorf("could not keep %s at %s before rewriting it, so nothing was rewritten: %w",
			branch, short(head), err)
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

	return res, publish(g, o, answers, &res, branch, imported, head)
}

// publish is the tail of a run: offer the push to the remote, and once it lands, put the same
// history on the box.
//
// Both endings reach it, the rewrite and the no-op, and that is the point. A branch can arrive
// here with every commit signed and the remote still behind it: `git rebase` on a Mac with
// commit.gpgsign signs what it replays, so the recovery from a duplicated lineage produces
// signed commits before signing ever sees them. Returning at "nothing to sign" left the remote
// five commits behind with `--push` on the command line, and the operator pushed by hand, which
// is the one step in this loop that has to be a signed push from this machine.
//
// Nothing is pushed when the remote already holds this head, and nothing is pushed when the
// remote holds commits this branch does not: a lease would let that rewind, and a remote ahead
// of a fully-signed branch means something happened that this run cannot account for.
func publish(g gitCmd, o SignOpts, answers *bufio.Reader, res *SignResult, branch, imported, head string) error {
	ref := o.Remote + "/" + branch
	if g.refExists(ref) {
		switch remoteHead, err := g.capture("rev-parse", ref); {
		case err != nil:
			return err
		case remoteHead == head:
			// Still realigned, and the invariant holds: the box is only moved to a history the
			// remote has, and here the remote has it already.
			fmt.Fprintf(o.Out, "%s is already at %s; nothing to push.\n", ref, short(head))
			res.BoxAligned = alignBox(g, o, branch, imported, head)
			return nil
		case exec.Command("git", "merge-base", "--is-ancestor", remoteHead, head).Run() != nil:
			fmt.Fprintf(o.Out, "\n%s is at %s, which is not an ancestor of %s: it holds commit(s) this\n"+
				"branch does not, so nothing was pushed. What they are:\n  git log --oneline %s --not %s\n",
				ref, short(remoteHead), short(head), ref, branch)
			return nil
		}
	}

	push := o.Push
	if !push {
		push = confirm(answers, o.Out, fmt.Sprintf("Push %s to %s with --force-with-lease?", branch, o.Remote))
	}
	if !push {
		fmt.Fprintf(o.Out, "Not pushed. When ready:\n  git push --force-with-lease %s %s\n", o.Remote, branch)
		if o.Box != "" {
			fmt.Fprintf(o.Out, "  git push --no-verify --force-with-lease %s %s   # and the box, or it keeps building on %s\n",
				o.Box, branch, short(imported))
		}
		return nil
	}
	if err := g.run("push", "--force-with-lease", o.Remote, branch); err != nil {
		return err
	}
	res.Pushed = true
	res.BoxAligned = alignBox(g, o, branch, imported, head)
	return nil
}

// importBranch puts this checkout on the box's copy of the branch, which is the only copy that
// matters: the box writes the code and the Mac holds whatever the last signing round left here.
//
// This was `aif` until it was not. That is a corp-repo binary reaching the box through a git
// remote named `ai`, so every checkout without one refused to sign, which is every fresh clone
// of this repo, and the message named a remote nothing in this tool has ever used. None of it
// was needed: `sand` already addresses the box as a git URL for the realigning push, and the
// same URL fetches. One fewer install on a new Mac, one fewer thing to keep pointed at the box.
//
// -C rather than a merge or a pull: the box is the authority on what the branch is. Anything on
// this side that the box does not have is either the rewrite from a round whose realignment did
// not land, or a branch someone moved by hand, and both are checkPreSigningLineage's to refuse
// with the recovery spelled out. Silently merging them is how two lineages start.
//
// No box configured is not a failure. It is a machine that has not run `sand config init`, the
// state alignBox and onBox already tolerate, and the branch in front of us is then the only
// answer available. Signing it is more use than a stop, as long as the run says so.
//
// It says what it did either way. This is the one step that moves a ref before the operator has
// been asked anything, and it can move it backwards, so "imported <old> → <new>" is the line
// that makes the rest of the run's hashes make sense.
func importBranch(g gitCmd, o SignOpts, branch string) error {
	if o.Box == "" {
		fmt.Fprintf(o.Out, "No box configured, so there is nothing to import: signing %s as this checkout\n"+
			"has it. `sand config set host <alias>` is what makes a run read the box instead.\n", branch)
		if err := g.run("switch", branch); err != nil {
			return fmt.Errorf("could not check out %s, so nothing was rewritten: %w", branch, err)
		}
		return nil
	}

	// Empty when the Mac does not have the branch yet, which is a normal first round.
	before, _ := g.capture("rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err := g.run("fetch", o.Box, branch); err != nil {
		return importFailed(g, o, branch, err)
	}
	imported, err := g.capture("rev-parse", "FETCH_HEAD")
	if err != nil {
		return fmt.Errorf("fetched %s from %s but cannot read FETCH_HEAD: %w", branch, o.Box, err)
	}

	switch {
	case before == imported:
		fmt.Fprintf(o.Out, "Imported %s from %s: already at %s.\n", branch, o.Box, short(imported))
	case before == "":
		fmt.Fprintf(o.Out, "Imported %s from %s at %s, new to this checkout.\n", branch, o.Box, short(imported))
	case exec.Command("git", "merge-base", "--is-ancestor", before, imported).Run() == nil:
		ahead, _ := g.capture("rev-list", "--count", before+".."+imported)
		fmt.Fprintf(o.Out, "Imported %s from %s: %s → %s, %s commit(s) this checkout did not have.\n",
			branch, o.Box, short(before), short(imported), ahead)
	default:
		// The Mac holds commits the box's branch does not. The box is the authority, so they
		// come off the branch, but they are not this command's to drop without a way back: the
		// reflog is not something to have to think of at the moment you notice. Before the ref
		// moves, and a failure to keep them stops the run rather than losing them.
		kept, err := keepAt(g, branch, "before-import", before)
		if err != nil {
			return fmt.Errorf("%s here holds commit(s) %s does not, and keeping them under a name "+
				"failed: %w\nnothing has moved; `git log --oneline %s --not %s` is what they are",
				branch, o.Box, err, branch, imported)
		}
		fmt.Fprintf(o.Out, "Imported %s from %s: %s → %s.\n"+
			"This checkout held commit(s) the box does not, and the box is what gets signed, so they\n"+
			"are off the branch now and kept as %s:\n  git log --oneline %s --not %s\n",
			branch, o.Box, short(before), short(imported), kept, kept, branch)
	}

	if err := g.run("switch", "--force-create", branch, "FETCH_HEAD"); err != nil {
		return fmt.Errorf("imported %s from %s but could not check it out: %w\n"+
			"nothing was rewritten and the import is in FETCH_HEAD, so once whatever git named above is\n"+
			"out of the way (an untracked file it would overwrite, usually):\n"+
			"  git switch -C %s FETCH_HEAD && sand sign %s", branch, o.Box, err, branch, branch)
	}
	return nil
}

// importFailed says which of the two failures it was, because the next move differs and git's
// "exit status 128" says neither. One extra question to the box, only on the way out: it either
// answers and does not have the branch, or it does not answer at all.
func importFailed(g gitCmd, o SignOpts, branch string, fetchErr error) error {
	head, err := boxBranchHead(g, o.Box, branch)
	switch {
	case err != nil:
		return fmt.Errorf("%s did not answer, so %s cannot be imported and nothing was signed: %w\n"+
			"that is the tailnet or the alias rather than the branch. `sand config get host` is what\n"+
			"this run used; `ssh %s true` is the shortest thing that fails the same way", o.Box, branch, err, o.Box)
	case head == "":
		return fmt.Errorf("%s has no branch %s, and the box is where a branch is written, so there is\n"+
			"nothing to sign. `sand status` lists what it does have; `sand new <issue>` is what creates\n"+
			"one on both machines", o.Box, branch)
	default:
		return fmt.Errorf("importing %s from %s failed, and the box has it at %s, so this is neither the\n"+
			"box being unreachable nor the branch being missing: %w", branch, o.Box, short(head), fetchErr)
	}
}

// keepAt leaves a branch at head, named `<branch>-<why>-<timestamp>`, so history about to be
// replaced stays reachable by a name somebody can read rather than only in the reflog. Two runs
// in the same second are normal in a review loop, so a taken name takes a -2, -3 suffix instead
// of failing the run.
func keepAt(g gitCmd, branch, why, head string) (string, error) {
	stem := fmt.Sprintf("%s-%s-%s", strings.ReplaceAll(branch, "/", "-"), why, time.Now().Format("20060102150405"))
	name := stem
	for i := 2; g.refExists("refs/heads/" + name); i++ {
		name = fmt.Sprintf("%s-%d", stem, i)
	}
	if err := g.run("branch", name, head); err != nil {
		return "", err
	}
	return name, nil
}

// boxCurrentBranch is the branch the box's checkout has out, and empty when it cannot say: a
// detached HEAD there, or a bare stand-in for it, neither of which is a disagreement with this
// machine. `ls-remote --symref` answers over the same URL the import uses, so asking costs no
// second transport and no ssh plumbing of its own.
func boxCurrentBranch(g gitCmd, box string) (string, error) {
	out, err := g.capture("ls-remote", "--symref", box, "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ref: ")
		if !ok {
			continue
		}
		ref, _, _ := strings.Cut(rest, "\t")
		return strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/"), nil
	}
	return "", nil
}

// boxBranchHead is the box's head for one branch: empty when it does not have it, an error when
// it could not be asked. One implementation, because every caller needs those two apart and a
// bool answer loses the difference.
func boxBranchHead(g gitCmd, box, branch string) (string, error) {
	refs, err := g.capture("ls-remote", box, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	head, _, _ := strings.Cut(strings.TrimSpace(refs), "\t")
	return head, nil
}

// onBox answers whether the box has this branch, and false when there is no box configured or
// it cannot be reached: the caller is deciding whether a name is worth importing, and an
// unreachable box is not evidence that the name is wrong.
func onBox(g gitCmd, o SignOpts, branch string) bool {
	if o.Box == "" {
		return false
	}
	head, err := boxBranchHead(g, o.Box, branch)
	return err == nil && head != ""
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
	boxHead, err := boxBranchHead(g, o.Box, branch)
	if err != nil {
		fmt.Fprintf(o.Out, "  could not read %s: %v\n", o.Box, err)
		fmt.Fprintf(o.Out, "  %s has the signed branch; the box does not. Re-run when it answers.\n", o.Remote)
		return false
	}

	lease := []string{"--force-with-lease=refs/heads/" + branch + ":" + boxHead}
	switch {
	case boxHead == "":
		lease = nil // not there yet, so there is nothing to overwrite and nothing to lease
	case boxHead == head:
		fmt.Fprintf(o.Out, "  already at %s\n", short(head))
		return true
	case boxHead == imported:
		// Exactly what this run imported: the rewrite is the only difference.
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

	// --no-verify, and only here. A Mac doing this job has a pre-push hook refusing commits it
	// cannot verify a signature on, which is exactly right for the push to GitHub and wrong for
	// this one twice over: the branch reaching the box is unsigned by construction on the recovery
	// path, and the range the hook measures is the whole history, since there is no
	// remote-tracking ref for the box to bound it. In aperture that came to 53 commits from
	// before the repo signed anything, none of them this branch's, and it blocked the push that
	// keeps the two machines on one lineage. The box cannot push to GitHub, so nothing reaches
	// GitHub unchecked by skipping it; the push above, which does, keeps the hook.
	args := append([]string{"push", "--no-verify"}, lease...)
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

// boxReceiveProbe asks the two questions that decide whether the push at the end of signing can
// land: what receive.denyCurrentBranch is set to there, and whether a tracked file is modified.
// `git config <key>` exits non-zero for a key that is not set, which is the common answer and the
// one that matters, hence the `|| echo unset`.
const boxReceiveProbe = `cd %s 2>/dev/null || { echo checkout=missing; exit 0; }
echo "deny=$(git config receive.denyCurrentBranch 2>/dev/null || echo unset)"
` + boxDirty

// checkBoxCanReceive makes the box able to take the rewrite before the rewrite exists.
//
// alignBox has to run after the push to the remote, because the box must never be moved to a
// history the remote rejected, so a box that cannot receive that push is discovered when the
// signed history is already on GitHub. That is exactly the two-lineage state all of this exists
// to stay out of, and both things that stop the push are one ssh call away, so they are answered
// here instead.
//
// receive.denyCurrentBranch is set rather than reported. git defaults it to "refuse", so a
// checkout nobody configured rejects every push into the branch it has out, and "warn" or
// "ignore" take the push while leaving the working tree behind, which is worse. Aperture's
// checkout was never set: every align push since alignBox existed failed with a hint at the tail
// of a long run, and every following round refused to sign. The setting is there only so this
// tool's own push can land, it is not source, and that push already rewrites the box's working
// tree, which is a great deal more than a line in its config. A setup command the operator has to
// remember is the step that gets skipped.
//
// A modified tracked file is a refusal, not a fix. updateInstead declines the push rather than
// overwrite it, and what becomes of the box's uncommitted work is not this machine's call. It is
// the same stop `sand status` already prints.
//
// An unreachable box or a missing checkout is neither. Neither says anything about whether the
// branch should be signed, and alignBox reports both after the push with nothing lost.
func checkBoxCanReceive(o SignOpts) error {
	if o.BoxHost == "" || o.BoxDir == "" {
		return nil
	}
	where := o.BoxHost + ":" + o.BoxDir
	out, err := exec.Command(sshBin(), o.BoxHost, fmt.Sprintf(boxReceiveProbe, remoteQuote(o.BoxDir))).Output()
	if err != nil {
		fmt.Fprintf(o.Out, "\nCould not ask %s whether it can take the rewrite: %v\n", where, err)
		return nil
	}

	deny, dirty := "", 0
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "checkout":
			fmt.Fprintf(o.Out, "\nNo checkout at %s, so the rewrite has nowhere to go back to.\n", where)
			return nil
		case "deny":
			deny = v
		case "dirty":
			dirty, _ = strconv.Atoi(v)
		}
	}

	if dirty > 0 {
		return fmt.Errorf("refusing to sign: %s has %d uncommitted tracked file(s), so the signed branch "+
			"cannot be handed back to it and the next round would arrive with unsigned copies of commits "+
			"already pushed. Commit or stash them there first:\n  ssh %s 'cd %s && git status --short'",
			where, dirty, o.BoxHost, o.BoxDir)
	}
	if deny == "updateInstead" {
		return nil
	}
	fix := fmt.Sprintf("ssh %s 'cd %s && git config receive.denyCurrentBranch updateInstead'", o.BoxHost, o.BoxDir)
	if o.DryRun {
		fmt.Fprintf(o.Out, "\ndry run: receive.denyCurrentBranch is %q on %s, which refuses the push that "+
			"hands the signed branch back; a real run sets it to updateInstead\n", deny, where)
		return nil
	}
	set := fmt.Sprintf("cd %s && git config receive.denyCurrentBranch updateInstead", remoteQuote(o.BoxDir))
	if err := exec.Command(sshBin(), o.BoxHost, set).Run(); err != nil {
		return fmt.Errorf("%s has receive.denyCurrentBranch=%s, which refuses the push that hands the signed "+
			"branch back, and setting it here failed: %w\n  %s", where, deny, err, fix)
	}
	fmt.Fprintf(o.Out, "\nSet receive.denyCurrentBranch=updateInstead on %s (was %q), without which the push "+
		"at the end of signing is refused and the box keeps building on the replaced lineage.\n", where, deny)
	return nil
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
// The recovery it prints has to run here and end on the box, in that order, because the import
// resets this checkout to the box's branch at the top of every run: a rebase done here and not
// pushed to the box is undone by the next `sand sign` before it reads anything. Only the Mac can
// do the rebase at all, since only the Mac can see <remote>/<branch>.
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
	le := &lineageError{boundary: twins[len(twins)-1].SHA}
	var dups []string
	for _, d := range twins {
		dups = append(dups, fmt.Sprintf("  %s is an unsigned copy of %s on %s: %q",
			short(d.SHA), short(d.Twin), d.On, d.Subject))
		le.merged = le.merged || d.On == remoteBase
	}

	// A commit whose twin is on the base is already merged, so it has to go rather than move:
	// rebasing onto the base drops it, since git skips what is upstream by patch id.
	fix := fmt.Sprintf("  git rebase --onto %s %s %s", remoteBranch, short(le.boundary), branch)
	if le.merged {
		fix = fmt.Sprintf("  git rebase %s %s   # drops the ones already merged", remoteBase, branch)
	}
	if box != "" {
		// Leased against the head this run imported, which is the box's, so a box that has
		// moved since rejects the push instead of losing the commits that moved it.
		// --no-verify for the reason alignBox gives: the branch this hands the box is unsigned,
		// which is the whole point of handing it over, and a signature hook here stops the
		// recovery from being runnable at all.
		fix += fmt.Sprintf("\n  git push --no-verify --force-with-lease=refs/heads/%s:%s %s %s\n"+
			"  sand sign --push", branch, head, box, branch)
	}
	where := remoteBranch
	if le.merged {
		where = remoteBase + " or " + remoteBranch
	}
	le.msg = fmt.Sprintf("refusing to sign: %d commit(s) are unsigned copies of commits already on %s.\n%s\n"+
		"This branch was built on a lineage an earlier signing round replaced, so signing it would put a\n"+
		"second copy of work that is already there on the remote. A twin on a branch goes away when the\n"+
		"branch is replaced; a twin on the base is merged and permanent. Drop or replay what is duplicated,\n"+
		"here on the Mac, then give the box the result before signing again: every run resets this\n"+
		"checkout to the box's branch, so a rebase the box has not been told about is undone by the next\n"+
		"one.\n%s",
		len(dups), where, strings.Join(dups, "\n"), fix)
	return le
}

// lineageError is checkPreSigningLineage's refusal, with what an offered repair needs: the
// commit to replay from, and whether the fix drops merged commits rather than moving them.
type lineageError struct {
	boundary string
	merged   bool
	msg      string
}

func (e *lineageError) Error() string { return e.msg }

// repairLineage does in place what the refusal message spells out: the rebase, then the push
// that tells the box, because the import resets this checkout to the box's branch and an untold
// rebase is undone by the next run. It returns the run state recomputed for the new history.
// The pre-rebase lineage needs no recovery branch: it is the box's, and the box still has it
// until the push here replaces it.
func repairLineage(g gitCmd, o SignOpts, le *lineageError, branch, base, imported string) (head string, commits []branchCommit, dirty, clean []string, err error) {
	args := []string{"rebase", "--onto", o.Remote + "/" + branch, le.boundary, branch}
	if le.merged {
		args = []string{"rebase", o.Remote + "/" + o.Base, branch}
	}
	if err := g.run(args...); err != nil {
		return "", nil, nil, nil, fmt.Errorf("rebase did not complete: %w\n"+
			"resolve and `git rebase --continue`, or `git rebase --abort`, then re-run; "+
			"the box still has the pre-rebase history", err)
	}
	head, err = g.capture("rev-parse", "HEAD")
	if err != nil {
		return "", nil, nil, nil, err
	}
	if o.Box != "" {
		// Same lease and --no-verify reasoning as the printed recovery: leased against the
		// head this run imported, so a box that moved since rejects rather than loses work.
		lease := "--force-with-lease=refs/heads/" + branch + ":" + imported
		if err := g.run("push", "--no-verify", lease, o.Box, branch); err != nil {
			return "", nil, nil, nil, fmt.Errorf("rebased, but the box was not told (%v), and the next run "+
				"resets this checkout to the box's branch: push %s to %s or reset --hard %s before re-running",
				err, branch, o.Box, short(imported))
		}
	}
	commits, err = g.branchCommits(head, base)
	if err != nil {
		return "", nil, nil, nil, err
	}
	dirty, clean = splitBySigning(commits)
	// The rebase is the fix, so anything still duplicated means it did not take (a conflict
	// resolved by keeping both sides, say). Refuse again rather than sign a second copy.
	if twins, terr := duplicatedOnRemote(g, dirty, o.Remote+"/"+branch, o.Remote+"/"+o.Base); terr != nil {
		return "", nil, nil, nil, terr
	} else if len(twins) > 0 {
		return "", nil, nil, nil, fmt.Errorf("rebase left %d duplicated commit(s) on %s; refusing to sign", len(twins), branch)
	}
	return head, commits, dirty, clean, nil
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

	ours, err := g.identitiesOf(commits)
	if err != nil {
		return nil, err
	}
	var dups []dupCommit
	for _, sha := range commits {
		id, ok := ours[sha]
		if !ok {
			return nil, fmt.Errorf("git did not report a tree and subject for %s", short(sha))
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

// identitiesOf is that key for a list of commits, in one process rather than one each. It is
// the same question `identity` answers, asked the way a caller with a branch's worth of commits
// has to ask it: signing keys every commit it is about to rewrite, and doing that a fork at a
// time cost more than the rewrite.
func (g gitCmd) identitiesOf(shas []string) (map[string]string, error) {
	if len(shas) == 0 {
		return nil, nil
	}
	out, err := g.capture(append([]string{"show", "-s", "--format=%H%x00" + commitIdentity}, shas...)...)
	if err != nil {
		return nil, err
	}
	byCommit := make(map[string]string, len(shas))
	for _, line := range strings.Split(out, "\n") {
		if sha, id, ok := strings.Cut(line, "\x00"); ok {
			byCommit[sha] = id
		}
	}
	return byCommit, nil
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
// The import takes whatever the box's branch holds, and a merge of another branch, a cherry-pick
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

// branchIndex is the pushed branch read once: the tree-plus-subject key of every commit on it,
// which is what a recorded hash is matched against. Once, because `push` asks the same question
// per reply, and asking it per reply meant re-reading the branch per reply. A nil byID is every
// way the question cannot be answered here — no such ref, or git could not say — and every
// caller answers all of those the same way, with commitUnknown.
type branchIndex struct {
	ref  string
	byID map[string][]string
}

func newBranchIndex(g gitCmd, ref string) *branchIndex {
	if ref == "" || !g.refExists(ref) {
		return &branchIndex{ref: ref}
	}
	byID, err := g.identities(ref)
	if err != nil {
		return &branchIndex{ref: ref}
	}
	return &branchIndex{ref: ref, byID: byID}
}

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
func commitOnBranch(g gitCmd, recorded string, branch *branchIndex) (string, commitState) {
	if branch == nil || branch.byID == nil {
		return recorded, commitUnknown
	}
	full, err := g.capture("rev-parse", "--verify", recorded+"^{commit}")
	if err != nil {
		return recorded, commitUnknown
	}
	if exec.Command("git", "merge-base", "--is-ancestor", full, branch.ref).Run() == nil {
		return recorded, commitCurrent
	}

	want, err := g.identity(full)
	if err != nil {
		return recorded, commitUnknown
	}
	var matches []string
	for _, sha := range branch.byID[want] {
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
//
// One process for the whole branch. `--header` prints each commit's raw header, which is where
// the parents, both identities and the signature all are, so the `git cat-file commit` this
// used to run per commit was a fork for information git had already been asked for: 64 of them
// on a 63-commit branch, and a signing run reads the branch twice, before the rewrite and
// after. Entries are NUL-separated, each one the object name, then the header, then a blank
// line and the message indented — which is why hasSignature and headerEmail, both of which
// stop at that blank line, keep working unchanged on it.
func (g gitCmd) branchCommits(rev, base string) ([]branchCommit, error) {
	out, err := g.capture("rev-list", "--reverse", "--header", rev, "--not", base)
	if err != nil {
		return nil, err
	}
	var commits []branchCommit
	for _, entry := range strings.Split(out, "\x00") {
		sha, header, ok := strings.Cut(strings.TrimLeft(entry, "\n"), "\n")
		if !ok || len(sha) < 40 { // the trailing NUL leaves an empty last entry
			continue
		}
		c := branchCommit{
			SHA: sha, Signed: hasSignature(header),
			Author: headerEmail(header, "author"), Committer: headerEmail(header, "committer"),
		}
		for _, line := range strings.Split(header, "\n") {
			if line == "" {
				break // end of the header; the message is next and is not one of these
			}
			if p, ok := strings.CutPrefix(line, "parent "); ok {
				c.Parents = append(c.Parents, strings.TrimSpace(p))
			}
		}
		commits = append(commits, c)
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
