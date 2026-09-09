package sand

// The CI half, over the same fakes as the review half: `gh` on PATH, ssh shimmed to a local
// shell, the box a temp directory. What these cannot prove is GitHub's own behaviour — that
// `gh pr checks` really does exit non-zero on a red PR is asserted here by the fake and has to
// be confirmed by a run on the Mac.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two Actions checks, one Buildkite-style commit status with no fetchable log, and a pass that
// must not get a file. The lint name has a space, a slash and a dot in it on purpose.
const checksFixture = `[
 {"name":"build","workflow":"CI","bucket":"fail","state":"FAILURE","event":"pull_request",
  "link":"https://github.com/o/r/actions/runs/7788/job/1","completedAt":"2026-08-31T10:00:00Z",
  "description":"failing after 2m"},
 {"name":"lint / go 1.26","workflow":"CI","bucket":"fail","state":"FAILURE","event":"pull_request",
  "link":"https://github.com/o/r/actions/runs/7788/job/2","completedAt":"2026-08-31T10:01:00Z",
  "description":""},
 {"name":"buildkite/build","workflow":"","bucket":"fail","state":"FAILURE","event":"push",
  "link":"https://buildkite.com/o/r/builds/12","completedAt":"2026-08-31T10:02:00Z",
  "description":"build #12 failed"},
 {"name":"vet","workflow":"CI","bucket":"pass","state":"SUCCESS","event":"pull_request",
  "link":"https://github.com/o/r/actions/runs/7788/job/3","completedAt":"2026-08-31T10:03:00Z",
  "description":""}
]
`

const runLogFixture = `build	Run make check	go vet ./...
build	Run make check	internal/sand/ci.go:12:2: undefined: nope
build	Run make check	make: *** [check] Error 1
`

// greenChecks is the same PR after the fix: nothing failing at all.
var greenChecks = strings.ReplaceAll(checksFixture, `"bucket":"fail","state":"FAILURE"`,
	`"bucket":"pass","state":"SUCCESS"`)

func ciDir(remoteBase string) string { return filepath.Join(remoteBase, "o", "r", "pr-42", "ci") }

// setChecks swaps what the fake gh answers, for the second half of a re-pull test.
func setChecks(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(os.Getenv("GH_CHECKS"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCIPullWritesTheFailingChecks(t *testing.T) {
	remoteBase, _ := harness(t)
	captureStdout(t)

	if err := runCIPull(nil); err != nil {
		t.Fatalf("ci pull: %v", err)
	}

	dir := ciDir(remoteBase)
	build := read(t, filepath.Join(dir, (CIFailure{Meta: CIMeta{Workflow: "CI", Check: "build"}}).Filename()))
	for _, want := range []string{
		"check: build",
		"bucket: fail",
		"status: pending",
		"run_id: \"7788\"",
		"undefined: nope", // the log came through
		notesHeading,
	} {
		if !strings.Contains(build, want) {
			t.Errorf("ci-build.md missing %q:\n%s", want, build)
		}
	}

	// A name with a slash and a space cannot become a path or two arguments.
	lint := filepath.Join(dir, (CIFailure{Meta: CIMeta{Workflow: "CI", Check: "lint / go 1.26"}}).Filename())
	if _, err := os.Stat(lint); err != nil {
		t.Errorf("no file for the check with a slash in its name: %v", err)
	}

	// Buildkite posts a commit status, and this machine has no client for it: link, no log.
	bk := read(t, filepath.Join(dir, (CIFailure{Meta: CIMeta{Workflow: "", Check: "buildkite/build"}}).Filename()))
	if !strings.Contains(bk, "https://buildkite.com/o/r/builds/12") {
		t.Errorf("buildkite file lost its link:\n%s", bk)
	}
	if !strings.Contains(bk, "not a GitHub Actions run") {
		t.Errorf("buildkite file does not say why there is no log:\n%s", bk)
	}
	if strings.Contains(bk, "undefined: nope") {
		t.Errorf("fetched an Actions log for a non-Actions check:\n%s", bk)
	}

	// A passing check is not something to fix, so it gets no file.
	if _, err := os.Stat(filepath.Join(dir, (CIFailure{Meta: CIMeta{Workflow: "CI", Check: "vet"}}).Filename())); !os.IsNotExist(err) {
		t.Errorf("wrote a file for a passing check: %v", err)
	}

	index := read(t, filepath.Join(dir, "index.md"))
	for _, want := range []string{"o/r#42", (CIFailure{Meta: CIMeta{Workflow: "CI", Check: "build"}}).Filename(), "buildkite/build", "sand up"} {
		if !strings.Contains(index, want) {
			t.Errorf("index missing %q:\n%s", want, index)
		}
	}
	if strings.Contains(index, "| vet |") {
		t.Errorf("index lists a passing check without --all:\n%s", index)
	}

	flagAll = true
	if err := runCIPull(nil); err != nil {
		t.Fatalf("ci pull --all: %v", err)
	}
	if index := read(t, filepath.Join(dir, "index.md")); !strings.Contains(index, "| vet |") {
		t.Errorf("--all did not list the passing checks:\n%s", index)
	}
}

// A failed job's log runs to megabytes and the part that says why is at the end. Cutting it
// silently would have an agent reasoning from a log that is not the one GitHub holds.
func TestCIPullTailsTheLogAndSaysSo(t *testing.T) {
	remoteBase, _ := harness(t)
	var b strings.Builder
	for i := range 500 {
		fmt.Fprintf(&b, "build\tstep\tline %d\n", i)
	}
	b.WriteString("build\tstep\tFAIL: the last line\n")
	if err := os.WriteFile(os.Getenv("GH_RUNLOG"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	flagLogLines = 5
	captureStdout(t)

	if err := runCIPull(nil); err != nil {
		t.Fatalf("ci pull: %v", err)
	}

	got := read(t, filepath.Join(ciDir(remoteBase), (CIFailure{Meta: CIMeta{Workflow: "CI", Check: "build"}}).Filename()))
	if !strings.Contains(got, "FAIL: the last line") {
		t.Errorf("kept the head of the log instead of the tail:\n%s", got)
	}
	if strings.Contains(got, "line 0\n") {
		t.Errorf("log was not tailed:\n%s", got)
	}
	if !strings.Contains(got, "496 earlier line(s) cut") {
		t.Errorf("file does not say how much was cut:\n%s", got)
	}
}

// Re-pull is the normal case, not the exception: the agent is mid-fix and CI has run again.
// Notes and the commit it recorded are the box's, and losing them loses its work.
func TestCIPullKeepsNotesAndRefreshesAGreenCheck(t *testing.T) {
	remoteBase, _ := harness(t)
	captureStdout(t)
	if err := runCIPull(nil); err != nil {
		t.Fatalf("ci pull: %v", err)
	}

	p := filepath.Join(ciDir(remoteBase), (CIFailure{Meta: CIMeta{Workflow: "CI", Check: "build"}}).Filename())
	body := strings.Replace(read(t, p), "status: pending", "status: fixed", 1)
	body = strings.Replace(body, "commit: \"\"", "commit: deadbee", 1)
	if err := os.WriteFile(p, []byte(body+"\nWas a missing import.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	setChecks(t, greenChecks)
	out := captureStdout(t)
	if err := runCIPull(nil); err != nil {
		t.Fatalf("re-pull: %v", err)
	}

	got := read(t, p)
	for _, want := range []string{"Was a missing import.", "commit: deadbee", "status: fixed"} {
		if !strings.Contains(got, want) {
			t.Errorf("re-pull lost %q:\n%s", want, got)
		}
	}
	// The transport adds and never deletes, so a file left saying `fail` about a check that
	// now passes is the failure mode: it has to be refreshed in place.
	if !strings.Contains(got, "bucket: pass") {
		t.Errorf("file still claims the check is failing:\n%s", got)
	}
	index := read(t, filepath.Join(ciDir(remoteBase), "index.md"))
	if !strings.Contains(index, "not failing any more") {
		t.Errorf("index does not say the check went green:\n%s", index)
	}
	if printed := out(); !strings.Contains(printed, "nothing failing") {
		t.Errorf("did not say the PR is green:\n%s", printed)
	}
}

func TestCIPullStartsTheAgentAndReportsBack(t *testing.T) {
	remoteBase, _ := harness(t)
	home := os.Getenv("HOME")
	if err := os.MkdirAll(filepath.Join(home, "projects", "r"), 0o755); err != nil {
		t.Fatal(err)
	}
	agentLog := filepath.Join(home, "agent.log")
	agent := filepath.Join(home, "fake-agent")
	if err := os.WriteFile(agent, []byte(fakeCIAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_LOG", agentLog)
	t.Setenv("SAND_CI_DIR", ciDir(remoteBase))

	flagNoAgent, flagAgent = false, agent
	out := captureStdout(t)
	if err := runCIPull(nil); err != nil {
		t.Fatalf("ci pull: %v", err)
	}

	ran := read(t, agentLog)
	cwd, prompt, _ := strings.Cut(strings.TrimSpace(ran), "\n")
	if want := filepath.Join(home, "projects", "r"); cwd != want {
		t.Errorf("agent ran in %q, want the checkout %q", cwd, want)
	}
	for _, want := range []string{"sand skill", ciDir(remoteBase), "o/r#42", "3 failing", "do not push"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	printed := out()
	for _, want := range []string{"worked", "deadbee", "next: sand up"} {
		if !strings.Contains(printed, want) {
			t.Errorf("output missing %q:\n%s", want, printed)
		}
	}
}

const fakeCIAgent = `#!/bin/sh
{ pwd; echo "$@"; } > "$AGENT_LOG"
for f in "$SAND_CI_DIR"/ci-*.md; do
  printf '\n%s\n' "Fixed the import." >> "$f"
  sed -i 's/^commit: .*/commit: deadbee/' "$f"
  sed -i 's/^status: pending/status: fixed/' "$f"
done
echo '{"type":"result","subtype":"success","result":"Fixed 3 checks."}'
`

// A green PR has nothing for an agent to do, and starting one there costs a turn to be told so.
func TestCIPullSkipsTheAgentWhenNothingFailed(t *testing.T) {
	harness(t)
	setChecks(t, greenChecks)
	flagNoAgent = false
	flagAgent = filepath.Join(t.TempDir(), "never-runs") // running this would fail
	out := captureStdout(t)

	if err := runCIPull(nil); err != nil {
		t.Fatalf("ci pull: %v", err)
	}
	if printed := out(); !strings.Contains(printed, "nothing to fix") {
		t.Errorf("started an agent for a green PR:\n%s", printed)
	}
}

func TestCIPullDryRunTouchesNothing(t *testing.T) {
	remoteBase, _ := harness(t)
	flagDryRun = true
	captureStdout(t)
	if err := runCIPull(nil); err != nil {
		t.Fatalf("ci pull: %v", err)
	}
	if _, err := os.Stat(filepath.Join(remoteBase, "o")); !os.IsNotExist(err) {
		t.Errorf("dry run wrote to the box: %v", err)
	}
}

// The log is verbatim, so a build that prints the notes heading at the start of a line would
// otherwise take the slot, and everything after it in the log would come back as the agent's
// notes: a file that answers itself.
func TestCINotesSurviveALogThatPrintsTheHeading(t *testing.T) {
	f := CIFailure{
		Meta: CIMeta{Check: "build", Bucket: bucketFail, Link: "https://example.invalid/1"},
		Log:  "make: *** printing\n## notes\nnot the agent's\n",
	}
	body, err := f.Render()
	if err != nil {
		t.Fatal(err)
	}
	body += "\nReal notes, written on the box.\n"

	got, err := ParseCIFailure(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.Notes != "Real notes, written on the box." {
		t.Errorf("notes came back as %q", got.Notes)
	}
}

// gh exits non-zero on a red PR, which is every PR this command is for. Reading the exit
// status instead of stdout would make the feature unusable in exactly its own case.
func TestFetchChecksIgnoresGHsExitStatus(t *testing.T) {
	harness(t)
	t.Setenv("GH_CHECKS_EXIT", "8") // what gh exits while anything is still pending

	checks, err := FetchChecks(Target{Owner: "o", Repo: "r", Number: 42})
	if err != nil {
		t.Fatalf("FetchChecks: %v", err)
	}
	if len(checks) != 4 {
		t.Fatalf("got %d checks, want 4", len(checks))
	}
	if checks[0].RunID != "7788" {
		t.Errorf("run id from the link: got %q", checks[0].RunID)
	}
	if checks[2].RunID != "" {
		t.Errorf("read an Actions run id out of a buildkite link: %q", checks[2].RunID)
	}
}

func TestCIRetryAfterFailedFix(t *testing.T) {
	base, _ := harness(t)
	captureStdout(t)
	if err := runCIPull(nil); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(ciDir(base), "ci-*.md"))
	for _, p := range paths {
		body := strings.Replace(read(t, p), "status: pending", "status: fixed", 1)
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	setChecks(t, strings.ReplaceAll(checksFixture, "7788", "8899"))
	flagNoAgent = false
	flagAgent = "/bin/true"
	flagRepoDir = t.TempDir()
	out := captureStdout(t)
	if err := runCIPull(nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out(), "nothing to fix, so no agent started") {
		t.Fatal("fresh failing CI run was skipped because old run was marked fixed")
	}
}
func TestCINotesAreNotAFix(t *testing.T) {
	base, _ := harness(t)
	captureStdout(t)
	if err := runCIPull(nil); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(ciDir(base), "ci-*.md"))
	for _, p := range paths {
		if err := os.WriteFile(p, []byte(read(t, p)+"\nInvestigating; still broken.\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	flagNoAgent = false
	flagAgent = "/bin/true"
	flagRepoDir = t.TempDir()
	out := captureStdout(t)
	if err := runCIPull(nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out(), "nothing to fix, so no agent started") {
		t.Fatal("pending checks with investigation notes skipped")
	}
}
func TestCICollidingNames(t *testing.T) {
	base, _ := harness(t)
	captureStdout(t)
	setChecks(t, `[{"name":"build/linux","bucket":"fail","link":"https://example.test/a"},{"name":"build:linux","bucket":"fail","link":"https://example.test/b"}]`)
	if err := runCIPull(nil); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(ciDir(base), "ci-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("2 failing checks produced %d file(s)", len(paths))
	}
}

func TestCISameNameDifferentWorkflows(t *testing.T) {
	base, _ := harness(t)
	captureStdout(t)
	setChecks(t, `[{"name":"build","workflow":"Linux","bucket":"fail","link":"https://example.test/a"},{"name":"build","workflow":"Mac","bucket":"fail","link":"https://example.test/b"}]`)
	if err := runCIPull(nil); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(ciDir(base), "ci-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("2 failing workflows produced %d file(s)", len(paths))
	}
}

func TestCIRerunPreservesLegacyNotesButResetsFixed(t *testing.T) {
	base, _ := harness(t)
	captureStdout(t)
	checks := `[{"name":"build","workflow":"CI","bucket":"fail","state":"FAILURE","link":"https://github.com/o/r/actions/runs/7788/job/1","completedAt":"2026-08-31T10:00:00Z"}]`
	setChecks(t, checks)
	if err := runCIPull(nil); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(ciDir(base), "ci-*.md"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("files=%v err=%v", paths, err)
	}
	legacy := filepath.Join(ciDir(base), "ci-build.md")
	if err := os.Rename(paths[0], legacy); err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(read(t, legacy), "status: pending", "status: fixed", 1) + "\nTried fixing the import.\n"
	if err := os.WriteFile(legacy, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runCIPull(nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, legacy), "status: fixed") {
		t.Fatal("same run lost its fixed marking")
	}
	setChecks(t, strings.Replace(checks, "10:00:00Z", "11:00:00Z", 1))
	if err := runCIPull(nil); err != nil {
		t.Fatal(err)
	}
	got, err := ParseCIFailure(read(t, legacy))
	if err != nil {
		t.Fatal(err)
	}
	if got.Fixed() || got.Notes != "Tried fixing the import." {
		t.Fatalf("rerun status/notes: %+v", got)
	}
	paths, err = filepath.Glob(filepath.Join(ciDir(base), "ci-*.md"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("duplicated legacy file: %v, %v", paths, err)
	}
}
