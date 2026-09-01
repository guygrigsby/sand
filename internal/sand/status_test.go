package sand

// status is read-only, so these run the real thing end to end: a real git repo for the Mac, a
// real clone standing in for the box, the fake gh for GitHub and the ssh shim for the box's
// probe. The one that matters is the first: the pre-signing lineage is the state this command
// exists to make visible, and it is invisible in either machine's git log.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// boxCheckout is the box: a working clone, not a bare repo, because status asks it what branch
// it is on and how dirty it is.
func boxCheckout(t *testing.T, from, branch string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "box-repo")
	mustRun(t, from, "git", "clone", "--quiet", from, dir)
	mustRun(t, dir, "git", "switch", "--quiet", branch)
	mustRun(t, dir, "git", "config", "receive.denyCurrentBranch", "updateInstead")
	return dir
}

func statusOpts(t *testing.T, box string, out *bytes.Buffer) StatusOpts {
	t.Helper()
	cfg, err := Resolve(flagHost, flagRemoteDir)
	if err != nil {
		t.Fatal(err)
	}
	target, hasPR, err := currentBranchPR()
	if err != nil {
		t.Fatal(err)
	}
	return StatusOpts{
		Cfg: cfg, Target: target, HasPR: hasPR,
		Remote: "origin", Base: "main",
		Box: box, RepoDir: box, Out: out,
	}
}

func TestStatusSeesAPreSigningLineage(t *testing.T) {
	dir, _ := signRepo(t)
	box := boxCheckout(t, dir, "feature") // the box's copy, taken before anything is signed
	harness(t)

	// Sign and push without realigning the box, which is exactly the round that leaves the two
	// machines holding the same work under different hashes.
	var signed strings.Builder
	o := signOpts(&signed, "")
	o.Branch, o.Push = "feature", true
	if _, err := Sign(o); err != nil {
		t.Fatalf("sign: %v\n%s", err, signed.String())
	}

	var out bytes.Buffer
	if err := Status(statusOpts(t, box, &out)); err != nil {
		t.Fatalf("status: %v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"is an unsigned copy of", "on origin/feature",
		`"feature: a"`,
		"next: sand sign --push",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status missing %q:\n%s", want, got)
		}
	}
}

func TestStatusSaysPullWhenGitHubHasAThreadTheBoxHasNot(t *testing.T) {
	dir, _ := signRepo(t)
	mustRun(t, dir, "git", "switch", "--quiet", "main")
	box := boxCheckout(t, dir, "main")
	harness(t)

	var out bytes.Buffer
	if err := Status(statusOpts(t, box, &out)); err != nil {
		t.Fatalf("status: %v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"1 unresolved thread(s), 1 of them not on the box",
		"no agent running",
		"next: sand comments pull 42",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "unsigned copy") {
		t.Errorf("a matching box reported as a duplicate lineage:\n%s", got)
	}
}

func TestStatusCountsADraftedReplyAndSaysUp(t *testing.T) {
	dir, _ := signRepo(t)
	mustRun(t, dir, "git", "switch", "--quiet", "main")
	box := boxCheckout(t, dir, "main")
	remoteBase, _ := harness(t)

	// Pull writes the thread files to the box; then answer one, the way an agent there would.
	if err := runPull(nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	file := filepath.Join(remoteBase, "o", "r", "pr-42", "c-2043881.md")
	raw := read(t, file)
	if err := os.WriteFile(file, []byte(raw+"\nClosed the fd in both paths.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Status(statusOpts(t, box, &out)); err != nil {
		t.Fatalf("status: %v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"threads: 1 (1 drafted, 0 sent)",
		"1 unresolved thread(s), 0 of them not on the box",
		"next: sand up 42",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status missing %q:\n%s", want, got)
		}
	}
}

func TestStatusWithNoPRSkipsGitHubAndSaysSo(t *testing.T) {
	dir, _ := signRepo(t)
	mustRun(t, dir, "git", "switch", "--quiet", "main")
	box := boxCheckout(t, dir, "main")
	harness(t)
	t.Setenv("GH_PR_MISSING", "1")
	flagPR = ""

	var out bytes.Buffer
	if err := Status(statusOpts(t, box, &out)); err != nil {
		t.Fatalf("status: %v\n%s", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "no open PR for main") {
		t.Errorf("status did not say there is no PR:\n%s", got)
	}
	if strings.Contains(got, "github") {
		t.Errorf("status asked GitHub about a PR that does not exist:\n%s", got)
	}
	if !strings.Contains(got, "next: nothing") {
		t.Errorf("want next: nothing, got:\n%s", got)
	}
}

// nextStep is the whole point of the command, and its order is the design: the states that stop
// everything come before the work that is ready, which comes before the work to bring over.
func TestNextStepOrder(t *testing.T) {
	o := StatusOpts{HasPR: true, Target: Target{Number: 42}}
	full := statusReport{
		box: boxState{
			Dups: []dupCommit{{}}, Agent: true, Dirty: 2, Drafted: 1, Unsigned: 1,
		},
		mac: macState{Unsigned: 1, Ahead: 1},
		hub: hubState{New: 1, Failing: 1},
	}

	for _, tc := range []struct {
		name string
		peel func(*statusReport)
		want string
	}{
		{"duplicated lineage first", func(*statusReport) {}, "sand sign --push"},
		{"then a running agent", func(r *statusReport) { r.box.Dups = nil }, "an agent is working on the box"},
		{"then the box's dirty tree", func(r *statusReport) { r.box.Agent = false }, "2 uncommitted file(s)"},
		{"then publishing", func(r *statusReport) { r.box.Dirty = 0 }, "sand up 42"},
		{"then new threads", func(r *statusReport) {
			r.box.Drafted, r.box.Unsigned, r.mac = 0, 0, macState{}
		}, "sand comments pull 42"},
		{"then failing checks", func(r *statusReport) { r.hub.New = 0 }, "sand ci pull 42"},
		{"then nothing", func(r *statusReport) { r.hub.Failing = 0 }, "nothing"},
	} {
		tc.peel(&full)
		if got := nextStep(o, full); !strings.Contains(got, tc.want) {
			t.Errorf("%s: next %q, want it to contain %q", tc.name, got, tc.want)
		}
	}
}
