package sand

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// signRepo builds a repository that looks like the Mac's after a sandbox branch landed: a
// pushed main, a feature branch with unsigned commits and a merge commit, an ssh signing
// key, and an aif that has nothing left to do because the branch is already here.
func signRepo(t *testing.T) (dir, remote string) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available, cannot sign")
	}
	root := t.TempDir()
	dir = filepath.Join(root, "repo")
	remote = filepath.Join(root, "remote.git")
	key := filepath.Join(root, "id")

	mustRun(t, root, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "sand-test", "-f", key)
	mustRun(t, root, "git", "init", "--quiet", "--bare", "--initial-branch=main", remote)
	mustRun(t, root, "git", "init", "--quiet", "--initial-branch=main", dir)

	for _, kv := range [][2]string{
		{"user.name", "Sand Test"},
		{"user.email", "sand@example.invalid"},
		{"gpg.format", "ssh"},
		{"user.signingkey", key + ".pub"},
		{"commit.gpgsign", "false"}, // the box cannot sign, so nothing arrives signed
	} {
		mustRun(t, dir, "git", "config", kv[0], kv[1])
	}
	mustRun(t, dir, "git", "remote", "add", "origin", remote)

	commit(t, dir, "base.txt", "base\n", "main: base")
	mustRun(t, dir, "git", "push", "--quiet", "-u", "origin", "main")

	// Feature branch: a commit, a side branch, and a merge, so the rewrite has a topology
	// to preserve rather than a straight line.
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "feature")
	commit(t, dir, "a.txt", "a\n", "feature: a")
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "feature-side")
	commit(t, dir, "b.txt", "b\n", "feature: b")
	mustRun(t, dir, "git", "switch", "--quiet", "feature")
	mustRun(t, dir, "git", "merge", "--quiet", "--no-ff", "-m", "feature: merge side", "feature-side")

	// aif is required and must be found; here the branch is already local, so a stub that
	// does nothing is the honest fake.
	aif := filepath.Join(root, "aif")
	if err := os.WriteFile(aif, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAND_AIF", aif)
	t.Chdir(dir)
	return dir, remote
}

func mustRun(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	c := exec.Command(name, args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file, body, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "add", file)
	mustRun(t, dir, "git", "commit", "--quiet", "-m", message)
}

func signOpts(out *strings.Builder, answer string) SignOpts {
	return SignOpts{Remote: "origin", Base: "main", Yes: true, In: strings.NewReader(answer), Out: out}
}

// `sand sign push`, a slip for `sand sign --push`, reached aif as the branch name and aif read
// it as its own push subcommand: this machine's HEAD went to the box before git ever said
// "invalid reference: push". A name nothing can resolve does not get handed over.
func TestSignRefusesABranchNothingHas(t *testing.T) {
	dir, _ := signRepo(t)
	ran := filepath.Join(dir, "aif-ran")
	aif := filepath.Join(t.TempDir(), "aif")
	if err := os.WriteFile(aif, []byte("#!/bin/sh\ntouch "+ran+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAND_AIF", aif)

	var out strings.Builder
	o := signOpts(&out, "")
	o.Branch = "push"
	_, err := Sign(o)
	if err == nil || !strings.Contains(err.Error(), "no branch push") {
		t.Fatalf("want a refusal naming the branch, got %v\n%s", err, out.String())
	}
	if _, statErr := os.Stat(ran); statErr == nil {
		t.Error("aif was run with a name that is not a branch")
	}
}

// boxRepo stands in for the box's checkout, holding the branch at the commit the Mac has just
// imported. Bare, because what the realignment has to get right is the ref; a real box also has
// a working tree, and following it is receive.denyCurrentBranch=updateInstead's job rather than
// anything this code does.
func boxRepo(t *testing.T, dir, branch string) string {
	t.Helper()
	box := filepath.Join(t.TempDir(), "box.git")
	mustRun(t, dir, "git", "init", "--quiet", "--bare", box)
	mustRun(t, dir, "git", "push", "--quiet", box, branch)
	return box
}

// boxWorkingCheckout is the box as it really is, where boxRepo is only its refs: a non-bare
// repository with the branch checked out, and receive.denyCurrentBranch left as `git init` leaves
// it. That combination is what rejects the realigning push, so it is what the pre-flight has to be
// tested against.
func boxWorkingCheckout(t *testing.T, dir, branch string) string {
	t.Helper()
	box := filepath.Join(t.TempDir(), "box")
	mustRun(t, dir, "git", "init", "--quiet", "--initial-branch=unborn", box)
	mustRun(t, dir, "git", "push", "--quiet", box, branch)
	mustRun(t, box, "git", "switch", "--quiet", branch)
	return box
}

// sshShim is the e2e stand-in for ssh on its own: it drops the host and runs the command here, so
// the questions signing asks the box are exercised without a second machine.
func sshShim(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ssh-shim")
	if err := os.WriteFile(p, []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// The failure this repo actually shipped, in aperture rather than here. `git init` leaves
// receive.denyCurrentBranch unset, git defaults it to refuse, and the box has the branch checked
// out, so every realigning push was rejected. alignBox runs after the push to the remote, so by
// then GitHub already had the signed history and the box was on the lineage it replaced: the next
// round refused to sign, and the round after that too. The precondition is one ssh call away, so
// signing settles it before it rewrites anything.
func TestSignMakesTheBoxAbleToTakeTheRewrite(t *testing.T) {
	dir, _ := signRepo(t)
	box := boxWorkingCheckout(t, dir, "feature")
	t.Setenv("SAND_SSH", sshShim(t))

	var out strings.Builder
	o := signOpts(&out, "")
	o.Push, o.Box = true, box
	o.BoxHost, o.BoxDir = "box", box
	res, err := Sign(o)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	signed := mustRun(t, dir, "git", "rev-parse", "feature")
	if !res.BoxAligned {
		t.Fatalf("the box did not take the rewrite\n%s", out.String())
	}
	if got := mustRun(t, box, "git", "rev-parse", "HEAD"); got != signed {
		t.Errorf("box is at %s, want the signed %s\n%s", short(got), short(signed), out.String())
	}
	if got := mustRun(t, box, "git", "config", "receive.denyCurrentBranch"); got != "updateInstead" {
		t.Errorf("receive.denyCurrentBranch on the box is %q, so the next push is refused too", got)
	}
	// updateInstead rather than ignore: the box's working tree has to follow the ref, or the
	// agent there edits files from a history that is gone.
	if err := exec.Command("git", "-C", box, "diff", "--quiet").Run(); err != nil {
		t.Error("the box's working tree does not match the branch it now holds")
	}
}

// A modified tracked file on the box refuses the realigning push, so the round cannot finish, so
// it does not start. What becomes of the box's uncommitted work is not this machine's call, and
// signing anyway is what leaves GitHub and the box on different lineages.
func TestSignRefusesADirtyBox(t *testing.T) {
	dir, _ := signRepo(t)
	box := boxWorkingCheckout(t, dir, "feature")
	t.Setenv("SAND_SSH", sshShim(t))
	if err := os.WriteFile(filepath.Join(box, "a.txt"), []byte("still being worked on\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unsigned := mustRun(t, dir, "git", "rev-parse", "feature")

	var out strings.Builder
	o := signOpts(&out, "")
	o.Push, o.Box = true, box
	o.BoxHost, o.BoxDir = "box", box
	if _, err := Sign(o); err == nil {
		t.Fatalf("signed with a box that cannot take the result\n%s", out.String())
	} else {
		for _, want := range []string{"1 uncommitted", "Commit or stash"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error does not mention %q: %v", want, err)
			}
		}
	}
	if after := mustRun(t, dir, "git", "rev-parse", "feature"); after != unsigned {
		t.Errorf("history moved anyway: %s → %s", short(unsigned), short(after))
	}
	if got := mustRun(t, dir, "git", "branch", "--list", "feature-before-signing-*"); got != "" {
		t.Errorf("the refusal left a recovery branch:\n%s", got)
	}
}

// Signing rewrites every commit it touches, so the box is left holding a chain that exists
// nowhere else. Until this ran, the box then committed on top of it and the next round arrived
// with unsigned copies of commits already signed and pushed: two lineages, same trees,
// different hashes, and hours to untangle by hand.
func TestSignPutsTheRewriteBackOnTheBox(t *testing.T) {
	dir, remote := signRepo(t)
	box := boxRepo(t, dir, "feature")
	unsigned := mustRun(t, dir, "git", "rev-parse", "feature")

	var out strings.Builder
	o := signOpts(&out, "")
	o.Push, o.Box = true, box
	res, err := Sign(o)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	signed := mustRun(t, dir, "git", "rev-parse", "feature")
	if signed == unsigned {
		t.Fatal("nothing was rewritten, so this proves nothing")
	}
	if got := mustRun(t, dir, "git", "--git-dir", box, "rev-parse", "feature"); got != signed {
		t.Errorf("box is at %s, want the signed %s\n%s", short(got), short(signed), out.String())
	}
	if got := mustRun(t, dir, "git", "--git-dir", remote, "rev-parse", "feature"); got != signed {
		t.Errorf("remote is at %s, want %s", short(got), short(signed))
	}
	if !res.BoxAligned {
		t.Errorf("BoxAligned false after realigning it\n%s", out.String())
	}

	// And the second round is a no-op on both, rather than re-signing what the box now holds.
	out.Reset()
	res, err = Sign(o)
	if err != nil {
		t.Fatalf("second round: %v\n%s", err, out.String())
	}
	if res.Rewritten != 0 {
		t.Errorf("second round rewrote %d commit(s)\n%s", res.Rewritten, out.String())
	}
}

// The second reason no realigning push had ever landed for aperture, independent of the box's
// receive config: a pre-push hook in the Mac's checkout, refusing every commit it cannot verify a
// signature on, on every remote. It fires for the box too, where the range is the whole history
// (there is no remote-tracking ref to bound it), so it counted 53 commits from before this repo
// signed anything and blocked the push. The hook is right about GitHub and has no business on this
// one, so the box's push skips it and the remote's does not.
func TestSignPushesToTheBoxPastALocalPrePushHook(t *testing.T) {
	dir, remote := signRepo(t)
	box := boxRepo(t, dir, "feature")
	log := filepath.Join(t.TempDir(), "pre-push.log")
	hook := "#!/bin/sh\necho \"$2\" >> " + log + "\n" +
		"case \"$2\" in *box.git*) echo 'pre-push: UNVERIFIED commit, refusing' 1>&2; exit 1;; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "hooks", "pre-push"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	o := signOpts(&out, "")
	o.Push, o.Box = true, box
	res, err := Sign(o)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	signed := mustRun(t, dir, "git", "rev-parse", "feature")
	if !res.BoxAligned {
		t.Fatalf("the hook blocked the realigning push\n%s", out.String())
	}
	if got := mustRun(t, dir, "git", "--git-dir", box, "rev-parse", "feature"); got != signed {
		t.Errorf("box is at %s, want the signed %s\n%s", short(got), short(signed), out.String())
	}

	// The gate that matters is still a gate: the hook ran for the remote and not for the box.
	ran, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ran), remote) {
		t.Errorf("the hook did not run for %s, so signing is skipping it everywhere:\n%s", remote, ran)
	}
	if strings.Contains(string(ran), box) {
		t.Errorf("the hook ran for the box:\n%s", ran)
	}
}

// A branch can reach signing with every commit signed and the remote still behind it: `git
// rebase` on a Mac with commit.gpgsign signs what it replays, so recovering from a duplicated
// lineage hands signing five already-signed commits. `--push` then printed "nothing to sign" and
// pushed nothing, and aperture's branch sat five commits behind GitHub until it was pushed by
// hand. The signed push from this machine is the one step of the ring nothing else can do.
func TestSignPushesWhatWasSignedButNeverPushed(t *testing.T) {
	dir, remote := signRepo(t)
	box := boxRepo(t, dir, "feature")

	var out strings.Builder
	first := signOpts(&out, "n\n") // signs, declines the push, so nothing leaves this machine
	first.Box = box
	if _, err := Sign(first); err != nil {
		t.Fatalf("first round: %v\n%s", err, out.String())
	}
	signed := mustRun(t, dir, "git", "rev-parse", "feature")
	if got := mustRun(t, dir, "git", "--git-dir", remote, "branch", "--list", "feature"); got != "" {
		t.Fatalf("the remote has the branch already, so this proves nothing: %q", got)
	}
	// The operator hands the box the signed branch, which is where the aperture round got to.
	mustRun(t, dir, "git", "push", "--quiet", "--force", box, "feature")

	out.Reset()
	o := signOpts(&out, "")
	o.Push, o.Box = true, box
	res, err := Sign(o)
	if err != nil {
		t.Fatalf("second round: %v\n%s", err, out.String())
	}
	if res.Rewritten != 0 {
		t.Errorf("re-signed %d commit(s) that were already signed", res.Rewritten)
	}
	if !res.Pushed {
		t.Fatalf("--push signed nothing and pushed nothing, leaving the remote behind\n%s", out.String())
	}
	if got := mustRun(t, dir, "git", "--git-dir", remote, "rev-parse", "feature"); got != signed {
		t.Errorf("remote is at %s, want %s\n%s", short(got), short(signed), out.String())
	}
	if !res.BoxAligned {
		t.Errorf("BoxAligned false with the box already on the signed head\n%s", out.String())
	}

	// And a third round has nothing left to do anywhere, rather than pushing again every time.
	out.Reset()
	res, err = Sign(o)
	if err != nil {
		t.Fatalf("third round: %v\n%s", err, out.String())
	}
	if res.Pushed {
		t.Errorf("pushed a remote that was already at the branch head\n%s", out.String())
	}
	if !strings.Contains(out.String(), "nothing to push") {
		t.Errorf("output did not say the remote was up to date:\n%s", out.String())
	}
}

// The box is where the code is written. If it committed while signing ran, those commits are
// only there, and a force push to realign would be the tool destroying the work it exists to
// carry. It stops instead, and says how to put the new commits on top.
func TestSignWillNotOverwriteABoxThatMovedOn(t *testing.T) {
	dir, _ := signRepo(t)
	box := boxRepo(t, dir, "feature")
	imported := mustRun(t, dir, "git", "rev-parse", "feature")

	// The agent answers one more thread on the box, after aif imported the branch here.
	commit(t, dir, "c.txt", "c\n", "feature: c on the box while signing ran")
	boxHead := mustRun(t, dir, "git", "rev-parse", "feature")
	mustRun(t, dir, "git", "push", "--quiet", box, "feature")
	mustRun(t, dir, "git", "reset", "--hard", "--quiet", imported)

	var out strings.Builder
	o := signOpts(&out, "")
	o.Push, o.Box = true, box
	res, err := Sign(o)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	if got := mustRun(t, dir, "git", "--git-dir", box, "rev-parse", "feature"); got != boxHead {
		t.Errorf("box moved from %s to %s, losing the commit only it had", short(boxHead), short(got))
	}
	if res.BoxAligned {
		t.Error("reported the box as realigned when it was not")
	}
	for _, want := range []string{short(boxHead), "only on the box", "rebase"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not mention %q:\n%s", want, out.String())
		}
	}
}

// The worse half of the same disease, and the one this repo actually grew: the duplicate is on
// the base, so it is merged and permanent rather than something replacing the branch undoes.
// Signing would put a third copy of the work on the remote and the two would refuse to merge.
func TestSignRefusesWorkAlreadyMergedIntoTheBase(t *testing.T) {
	dir, _ := signRepo(t)
	old := mustRun(t, dir, "git", "rev-parse", "main")

	// The same change committed twice off the same point: one copy merged to main by an
	// earlier round, one still on a branch. The empty commit only forces a different parent,
	// so the two dup commits differ in hash and agree on tree and subject, which is exactly
	// what a signing rewrite leaves behind.
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "merged", old)
	mustRun(t, dir, "git", "commit", "--quiet", "--allow-empty", "-m", "main: unrelated")
	commit(t, dir, "dup.txt", "dup\n", "the duplicated one")
	mustRun(t, dir, "git", "push", "--quiet", "origin", "merged:main")
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "dup", old)
	commit(t, dir, "dup.txt", "dup\n", "the duplicated one")

	var out strings.Builder
	o := signOpts(&out, "")
	o.Push = true
	_, err := Sign(o)
	if err == nil {
		t.Fatalf("signed work that is already on origin/main\n%s", out.String())
	}
	for _, want := range []string{"origin/main", "the duplicated one", "git rebase origin/main dup"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// The tripwire for the runs that get past the realignment: a branch built on the lineage the
// last round replaced. Signing it would push a second copy of work that is already on the
// remote, so it stops before making a recovery branch, and names the pairs.
func TestSignRefusesAPreSigningLineage(t *testing.T) {
	dir, remote := signRepo(t)
	box := boxRepo(t, dir, "feature")
	var out strings.Builder
	o := signOpts(&out, "")
	o.Push, o.Box = true, box
	if _, err := Sign(o); err != nil {
		t.Fatalf("first round: %v\n%s", err, out.String())
	}
	signed := mustRun(t, dir, "git", "rev-parse", "feature")

	// The box never took the rewrite, so it is still on what it committed, and it commits again.
	backup := mustRun(t, dir, "git", "branch", "--list", "--format=%(refname:short)", "feature-before-signing-*")
	if backup == "" {
		t.Fatal("no recovery branch to rewind to")
	}
	mustRun(t, dir, "git", "reset", "--hard", "--quiet", backup)
	commit(t, dir, "d.txt", "d\n", "feature: d, on the stale lineage")
	stale := mustRun(t, dir, "git", "rev-parse", "feature")

	out.Reset()
	_, err := Sign(o)
	if err == nil {
		t.Fatalf("signed a pre-signing lineage\n%s", out.String())
	}
	// The recovery has to be runnable as printed: the rebase is a Mac-side one onto the pushed
	// branch, and it is followed by the push that tells the box, without which aif undoes it.
	for _, want := range []string{
		"unsigned copies", "origin/feature", "feature: a",
		"git rebase --onto origin/feature ",
		"--force-with-lease=refs/heads/feature:" + stale, box,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
	if after := mustRun(t, dir, "git", "rev-parse", "feature"); after != stale {
		t.Errorf("history moved anyway: %s → %s", short(stale), short(after))
	}
	if got := mustRun(t, dir, "git", "--git-dir", remote, "rev-parse", "feature"); got != signed {
		t.Errorf("remote moved from %s to %s", short(signed), short(got))
	}
	// One recovery branch from the successful first round, and none from the refusal.
	if got := mustRun(t, dir, "git", "branch", "--list", "feature-before-signing-*"); strings.Count(got, "\n") != 0 {
		t.Errorf("refusal left a second recovery branch:\n%s", got)
	}
}

func TestSignSignsEveryBranchCommitAndKeepsTheMerge(t *testing.T) {
	dir, remote := signRepo(t)
	before := mustRun(t, dir, "git", "log", "--format=%P %s", "feature", "--not", "origin/main")
	var out strings.Builder

	// "n" declines the push, so this asserts the rewrite alone.
	if _, err := Sign(signOpts(&out, "n\n")); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	shas := strings.Fields(mustRun(t, dir, "git", "rev-list", "feature", "--not", "origin/main"))
	if len(shas) != 3 {
		t.Fatalf("signed %d commits, want 3\n%s", len(shas), out.String())
	}
	for _, sha := range shas {
		if raw := mustRun(t, dir, "git", "cat-file", "commit", sha); !hasSignature(raw) {
			t.Errorf("%s is not signed", sha)
		}
	}

	// Same shape, same messages, same parent counts: only the signatures are new.
	if after := mustRun(t, dir, "git", "log", "--format=%P %s", "feature", "--not", "origin/main"); countParents(after) != countParents(before) {
		t.Errorf("topology changed:\nbefore\n%s\nafter\n%s", before, after)
	}
	if merges := mustRun(t, dir, "git", "rev-list", "--merges", "feature", "--not", "origin/main"); len(strings.Fields(merges)) != 1 {
		t.Errorf("merge commit did not survive, --merges gave %q", merges)
	}

	if backups := mustRun(t, dir, "git", "branch", "--list", "feature-before-signing-*"); backups == "" {
		t.Error("no recovery branch left behind")
	}
	if refs := mustRun(t, dir, "git", "--git-dir", remote, "branch", "--list", "feature"); refs != "" {
		t.Errorf("declining the push still pushed: %q", refs)
	}
	if !strings.Contains(out.String(), "Verified: all 3") {
		t.Errorf("output did not report the verification:\n%s", out.String())
	}
}

// Review is a loop: round one signs, and the hashes it produced get quoted in replies that
// are posted to GitHub. Round two must sign only what the agent added, because re-signing an
// already-signed commit gives it a new hash and every reply quoting the old one goes stale.
func TestSignOnlySignsWhatIsNotSignedYet(t *testing.T) {
	dir, _ := signRepo(t)
	var out strings.Builder
	if _, err := Sign(signOpts(&out, "n\n")); err != nil {
		t.Fatalf("first round: %v\n%s", err, out.String())
	}
	roundOne := strings.Fields(mustRun(t, dir, "git", "rev-list", "feature", "--not", "origin/main"))
	if len(roundOne) != 3 {
		t.Fatalf("first round left %d commits, want 3", len(roundOne))
	}

	// The agent on the box answers another thread, so one more unsigned commit lands on top.
	commit(t, dir, "c.txt", "c\n", "feature: c")

	out.Reset()
	res, err := Sign(signOpts(&out, "n\n"))
	if err != nil {
		t.Fatalf("second round: %v\n%s", err, out.String())
	}
	if res.Total != 4 || res.Rewritten != 1 || res.Kept != 3 {
		t.Errorf("signed %d of %d with %d kept, want 1 of 4 with 3 kept\n%s",
			res.Rewritten, res.Total, res.Kept, out.String())
	}

	// Hash equality alone does not prove the range was narrowed: an ssh ed25519 signature over
	// the same bytes is the same bytes, so re-signing this repo's commits happens to reproduce
	// them. An OpenPGP signature carries its creation time and does not, which is the case the
	// narrowing is for. filter-branch's own "(n/m)" is the count of what it was handed.
	if !strings.Contains(out.String(), "(1/1)") {
		t.Errorf("filter-branch was given more than the one unsigned commit:\n%s", out.String())
	}

	now := map[string]bool{}
	for _, sha := range strings.Fields(mustRun(t, dir, "git", "rev-list", "feature", "--not", "origin/main")) {
		now[sha] = true
		if raw := mustRun(t, dir, "git", "cat-file", "commit", sha); !hasSignature(raw) {
			t.Errorf("%s came out of the second round unsigned", sha)
		}
	}
	for _, sha := range roundOne {
		if !now[sha] {
			t.Errorf("second round moved %s, which a posted reply already quotes\n%s", sha, out.String())
		}
	}
	if len(now) != 4 {
		t.Errorf("branch has %d commits after the second round, want 4", len(now))
	}
}

// Nothing new on the branch: the whole run is a no-op, and in particular does not rewrite
// history for the sake of it.
func TestSignSkipsAFullySignedBranch(t *testing.T) {
	dir, _ := signRepo(t)
	var out strings.Builder
	if _, err := Sign(signOpts(&out, "n\n")); err != nil {
		t.Fatalf("first round: %v\n%s", err, out.String())
	}
	before := mustRun(t, dir, "git", "rev-parse", "feature")
	backupsBefore := mustRun(t, dir, "git", "branch", "--list", "feature-before-signing-*")

	out.Reset()
	res, err := Sign(signOpts(&out, "n\n"))
	if err != nil {
		t.Fatalf("second round: %v\n%s", err, out.String())
	}
	if res.Rewritten != 0 || res.Kept != 3 {
		t.Errorf("res = %+v, want nothing rewritten and 3 kept", res)
	}
	if after := mustRun(t, dir, "git", "rev-parse", "feature"); after != before {
		t.Errorf("branch moved from %s to %s with nothing to sign", before, after)
	}
	if got := mustRun(t, dir, "git", "branch", "--list", "feature-before-signing-*"); got != backupsBefore {
		t.Errorf("made a recovery branch for a no-op: %q then %q", backupsBefore, got)
	}
	if !strings.Contains(out.String(), "signed already") {
		t.Errorf("output did not say the branch was already signed:\n%s", out.String())
	}
}

func TestSignPushesWhenAsked(t *testing.T) {
	dir, remote := signRepo(t)
	var out strings.Builder

	o := signOpts(&out, "")
	o.Push = true
	if _, err := Sign(o); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	local := mustRun(t, dir, "git", "rev-parse", "feature")
	pushed := mustRun(t, dir, "git", "--git-dir", remote, "rev-parse", "feature")
	if local != pushed {
		t.Fatalf("remote at %s, local at %s", pushed, local)
	}
}

// Both answers come from one stdin, so the reader behind the first prompt must not swallow
// the second: piping "y\ny\n" has to sign and push.
func TestSignAnswersBothPromptsFromOneStdin(t *testing.T) {
	dir, remote := signRepo(t)
	var out strings.Builder
	o := signOpts(&out, "y\ny\n")
	o.Yes = false

	if _, err := Sign(o); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	local := mustRun(t, dir, "git", "rev-parse", "feature")
	pushed := mustRun(t, dir, "git", "--git-dir", remote, "rev-parse", "feature")
	if local != pushed {
		t.Fatalf("second answer lost: remote at %q, local at %s\n%s", pushed, local, out.String())
	}
}

func TestSignDryRunRewritesNothing(t *testing.T) {
	dir, remote := signRepo(t)
	before := mustRun(t, dir, "git", "rev-parse", "feature")
	var out strings.Builder
	o := signOpts(&out, "y\ny\n") // answers waiting, and still nothing must happen
	o.Yes, o.Push, o.DryRun = false, true, true

	if _, err := Sign(o); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if after := mustRun(t, dir, "git", "rev-parse", "feature"); after != before {
		t.Errorf("dry run moved the branch from %s to %s", before, after)
	}
	if backups := mustRun(t, dir, "git", "branch", "--list", "feature-before-signing-*"); backups != "" {
		t.Errorf("dry run left a recovery branch: %q", backups)
	}
	if refs := mustRun(t, dir, "git", "--git-dir", remote, "branch", "--list", "feature"); refs != "" {
		t.Errorf("dry run pushed: %q", refs)
	}
	if !strings.Contains(out.String(), "3 commit(s)") || !strings.Contains(out.String(), "dry run") {
		t.Errorf("dry run did not report what it would sign:\n%s", out.String())
	}
}

// The box commits without a key, so the hash an agent writes into `commit:` is the hash of
// an unsigned commit that signing then replaces. Every state of that lookup matters: the
// wrong answer either posts a dead link or refuses a good one.
func TestCommitOnBranch(t *testing.T) {
	dir, _ := signRepo(t)
	g := gitCmd{out: &strings.Builder{}}

	// A commit that exists but never reaches the branch: nothing on the branch matches it.
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "elsewhere")
	commit(t, dir, "d.txt", "d\n", "elsewhere: unrelated work")
	orphan := mustRun(t, dir, "git", "rev-parse", "--short", "HEAD")
	mustRun(t, dir, "git", "switch", "--quiet", "feature")

	recorded := mustRun(t, dir, "git", "rev-parse", "--short", "HEAD") // what the agent wrote down
	var out strings.Builder
	if _, err := Sign(signOpts(&out, "y\n")); err != nil { // "y" pushes it, so origin/feature exists
		t.Fatalf("%v\n%s", err, out.String())
	}
	signed := mustRun(t, dir, "git", "rev-parse", "--short=7", "HEAD")

	for _, tc := range []struct {
		name  string
		hash  string
		ref   string
		want  commitState
		hash2 string // the hash a reply should quote
	}{
		{"signing moved it", recorded, "origin/feature", commitMoved, signed},
		{"already on the branch", signed, "origin/feature", commitCurrent, signed},
		{"not on the branch at all", orphan, "origin/feature", commitGone, orphan},
		{"no such branch here", recorded, "origin/nope", commitUnknown, recorded},
		{"no such commit here", "0000000", "origin/feature", commitUnknown, "0000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, state := commitOnBranch(g, tc.hash, tc.ref)
			if state != tc.want {
				t.Errorf("state = %d, want %d", state, tc.want)
			}
			if got != tc.hash2 {
				t.Errorf("hash = %q, want %q", got, tc.hash2)
			}
		})
	}
}

// A signature says the signer vouches for the commit. `aif` imports whatever the box's branch
// holds, so a merge of another branch, a cherry-pick or an agent with a different git config
// puts someone else's commits in front of this Mac's key. --yes must not wave that through: it
// answers a question about known work, it does not widen what the key attests to.
func TestSignRefusesSomeoneElsesCommits(t *testing.T) {
	dir, _ := signRepo(t)

	c := exec.Command("git", "commit", "--quiet", "--allow-empty", "-m", "colleague: their work")
	c.Dir = dir
	c.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Someone Else", "GIT_AUTHOR_EMAIL=else@example.invalid",
		"GIT_COMMITTER_NAME=Someone Else", "GIT_COMMITTER_EMAIL=else@example.invalid")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	before := mustRun(t, dir, "git", "rev-parse", "HEAD")

	var out strings.Builder
	o := signOpts(&out, "y\n") // Yes: true, so this is the flag that must not help
	if _, err := Sign(o); err == nil {
		t.Fatalf("signed someone else's commit\n%s", out.String())
	} else if !strings.Contains(err.Error(), "else@example.invalid") {
		t.Errorf("error does not name who made it: %v", err)
	}
	if after := mustRun(t, dir, "git", "rev-parse", "HEAD"); after != before {
		t.Errorf("history moved anyway: %s → %s", before, after)
	}
	if backups := mustRun(t, dir, "git", "branch", "--list", "feature-before-signing-*"); backups != "" {
		t.Errorf("refusal left a recovery branch: %q", backups)
	}

	// The operator who does vouch for it says so, and then it signs.
	o.AllowOtherAuthors = true
	res, err := Sign(o)
	if err != nil {
		t.Fatalf("--allow-other-authors: %v\n%s", err, out.String())
	}
	if res.Rewritten != 4 {
		t.Errorf("signed %d commit(s), want 4", res.Rewritten)
	}
}

// The operator is being asked to vouch for work done on another machine, so the prompt has to
// show more than hashes and subject lines.
func TestSignShowsWhatItAttestsTo(t *testing.T) {
	signRepo(t)
	var out strings.Builder
	o := signOpts(&out, "")
	o.DryRun, o.Yes = true, false
	if _, err := Sign(o); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, want := range []string{"attests to", "a.txt", "b.txt", "2 files changed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in the output:\n%s", want, out.String())
		}
	}
}

// Tree plus subject is an identity claim, and two commits on the branch can satisfy it: a
// cherry-pick, a merge that brought a copy back, a branch signed twice. Guessing between them
// posts a reviewer a link to the wrong commit, which is the one failure this lookup exists to
// prevent, so it holds the reply instead.
func TestCommitOnBranchRefusesToGuessBetweenTwoMatches(t *testing.T) {
	dir, _ := signRepo(t)
	g := gitCmd{out: &strings.Builder{}}

	// commit-tree rather than commit: the duplicates need the same tree and subject with
	// different hashes, and two `git commit`s in the same second produce the same bytes.
	base := mustRun(t, dir, "git", "rev-parse", "HEAD")
	tree := mustRun(t, dir, "git", "rev-parse", "HEAD^{tree}")
	mk := func(parent, date string) string {
		t.Helper()
		c := exec.Command("git", "commit-tree", tree, "-p", parent, "-m", "sand: dup me")
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
		out, err := c.Output()
		if err != nil {
			t.Fatalf("commit-tree: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	d1 := mk(base, "2026-01-01T00:00:00Z")
	d2 := mk(d1, "2026-01-02T00:00:00Z")
	recorded := mk(base, "2026-01-03T00:00:00Z") // never reaches the branch: the box's hash
	mustRun(t, dir, "git", "update-ref", "refs/heads/feature", d2)
	mustRun(t, dir, "git", "push", "--quiet", "origin", "feature")

	got, state := commitOnBranch(g, recorded, "origin/feature")
	if state != commitAmbiguous {
		t.Fatalf("state = %d, want commitAmbiguous (%d); got hash %q", state, commitAmbiguous, got)
	}
	for _, want := range []string{short(d1), short(d2)} {
		if !strings.Contains(got, want) {
			t.Errorf("%q does not name the candidate %s", got, want)
		}
	}
}

func TestSignRefusals(t *testing.T) {
	t.Run("protected branch", func(t *testing.T) {
		dir, _ := signRepo(t)
		mustRun(t, dir, "git", "switch", "--quiet", "main")
		var out strings.Builder
		_, err := Sign(signOpts(&out, ""))
		if err == nil || !strings.Contains(err.Error(), "protected") {
			t.Fatalf("err = %v, want a refusal to rewrite main", err)
		}
	})

	t.Run("dirty tree", func(t *testing.T) {
		dir, _ := signRepo(t)
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("edited\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		_, err := Sign(signOpts(&out, ""))
		if err == nil || !strings.Contains(err.Error(), "uncommitted") {
			t.Fatalf("err = %v, want a refusal on a dirty tree", err)
		}
	})

	t.Run("nothing to sign", func(t *testing.T) {
		dir, _ := signRepo(t)
		mustRun(t, dir, "git", "switch", "--quiet", "-c", "empty", "origin/main")
		var out strings.Builder
		if _, err := Sign(signOpts(&out, "")); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "nothing to sign") {
			t.Errorf("output was %q", out.String())
		}
		if backups := mustRun(t, dir, "git", "branch", "--list", "empty-before-signing-*"); backups != "" {
			t.Errorf("made a recovery branch for nothing: %q", backups)
		}
	})

	t.Run("missing aif", func(t *testing.T) {
		signRepo(t)
		t.Setenv("SAND_AIF", filepath.Join(t.TempDir(), "not-installed"))
		var out strings.Builder
		_, err := Sign(signOpts(&out, ""))
		if err == nil || !strings.Contains(err.Error(), "not on PATH") {
			t.Fatalf("err = %v, want a stop on the missing import tool", err)
		}
		// Their own binary, so ours is not the answer.
		if strings.Contains(err.Error(), "./misc/aif") {
			t.Errorf("told them to install aif over their SAND_AIF: %v", err)
		}
	})

	// The stop for the real aif has to say where aif comes from: it ships in the corp repo,
	// not here, so it is the one requirement a first run cannot satisfy from the error alone.
	t.Run("missing aif says where to get it", func(t *testing.T) {
		if hint := aifHint("aif"); !strings.Contains(hint, "install ./misc/aif") {
			t.Errorf("hint for the real aif was %q", hint)
		}
		if hint := aifHint("/opt/mine/aif-fork"); hint != "" {
			t.Errorf("hint for an overridden aif was %q", hint)
		}
	})

	t.Run("declining the rewrite changes nothing", func(t *testing.T) {
		dir, _ := signRepo(t)
		before := mustRun(t, dir, "git", "rev-parse", "feature")
		var out strings.Builder
		o := signOpts(&out, "no\n")
		o.Yes = false
		if _, err := Sign(o); err != nil {
			t.Fatal(err)
		}
		if after := mustRun(t, dir, "git", "rev-parse", "feature"); after != before {
			t.Errorf("branch moved from %s to %s after declining", before, after)
		}
		if !strings.Contains(out.String(), "Cancelled") {
			t.Errorf("output was %q", out.String())
		}
	})
}

func TestHasSignatureIgnoresTheMessage(t *testing.T) {
	commit := "tree abc\nauthor A <a@b> 1 +0000\ncommitter A <a@b> 1 +0000\n\ngpgsig this is prose\n"
	if hasSignature(commit) {
		t.Error("a message mentioning gpgsig counted as a signature")
	}
	if !hasSignature("tree abc\ngpgsig -----BEGIN SSH SIGNATURE-----\n\nmessage\n") {
		t.Error("a real header was missed")
	}
}

// countParents summarises "%P %s" as "<parent count> <subject>" per commit: the shape a
// rebase would flatten and commit-tree preserves, with the rewritten hashes dropped.
func countParents(log string) string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		fields := strings.Fields(line)
		n := 0
		for _, f := range fields {
			if len(f) == 40 && !strings.ContainsAny(f, " :") && isHex(f) {
				n++
				continue
			}
			break
		}
		lines = append(lines, fmt.Sprintf("%d %s", n, strings.Join(fields[n:], " ")))
	}
	return strings.Join(lines, "\n")
}

func isHex(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
