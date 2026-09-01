package sand

// End to end over the real code paths: a fake `gh` on PATH stands in for GitHub, and a
// shim that runs the remote command locally stands in for ssh. Everything between —
// the GraphQL decode, the file format, tar in both directions, the merge on re-pull,
// the reply POST and the sent marking — is the actual implementation.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

const fakeGH = `#!/bin/sh
printf '%s\n' "$@" >> "$GH_LOG"
case "$*" in
  *"--method POST"*)
    printf 'HTTP/2.0 201 Created\r\nContent-Type: application/json\r\n\r\n'
    printf '{"html_url":"https://github.com/o/r/pull/42#discussion_r999"}\n'
    ;;
  *"repo view"*)  echo '{"nameWithOwner":"o/r"}' ;;
  *"issue view"*) echo '{"title":"Fix the thing, safely!","url":"https://github.com/o/r/issues/42","body":"The fd leaks."}' ;;
  *"/commits?"*)  cat "$GH_COMMITS" ;;
  *"pr checks"*)
    cat "$GH_CHECKS"
    # gh exits non-zero when a check has failed, which is every run this command is for.
    exit "${GH_CHECKS_EXIT:-1}"
    ;;
  *"run view"*)   cat "$GH_RUNLOG" ;;
  *"api user"*)   echo guy ;;
  *graphql*)      cat "$GH_FIXTURE" ;;
  *"pr create"*)
    while [ "$1" ]; do
      if [ "$1" = --body-file ]; then cat "$2" > "$GH_PR_BODY"; break; fi
      shift
    done
    touch "$GH_CREATED"
    echo 'https://github.com/o/r/pull/42'
    ;;
  *"pr list"*)
    if [ "$GH_PR_MISSING" = 1 ] && [ ! -e "$GH_CREATED" ]; then echo '[]';
    else echo '[{"number":42,"url":"https://github.com/o/r/pull/42","title":"Fix the thing","headRefName":"topic"}]'; fi
    ;;
  *"pr view"*)
    # A bare "pr view" guesses the head repo from the local remotes, and in aperture the box
    # remote (guy-llm-sandbox:projects/aperture) reads as owner "projects". Naming the PR or the
    # repo leaves nothing to guess, so only the bare form fails here.
    if [ "$GH_BAD_HEAD_GUESS" = 1 ]; then
      case "$*" in
        *--repo*) ;;
        *) echo 'no pull requests found for branch "projects:topic"' >&2; exit 1 ;;
      esac
    fi
    if [ "$GH_PR_MISSING" = 1 ] && [ ! -e "$GH_CREATED" ]; then
      echo 'no pull requests found' >&2
      exit 1
    fi
    branch=topic
    [ "$GH_PR_MISSING" = 1 ] && branch=guy/42-fix-the-thing
    printf '{"number":42,"url":"https://github.com/o/r/pull/42","title":"Fix the thing","headRefName":"%s"}\n' "$branch"
    ;;
  *) echo "fake gh: unhandled: $*" >&2; exit 1 ;;
esac
`

// fakeSSH drops the host argument and runs the command here. It keeps the quoting
// honest: whatever the real ssh would hand to a remote shell, this hands to a local one.
const fakeSSH = `#!/bin/sh
shift
exec /bin/sh -c "$*"
`

const fixture = `{"data":{"repository":{"pullRequest":{
  "number":42,"title":"Fix the thing","url":"https://github.com/o/r/pull/42",
  "headRefName":"topic","author":{"login":"guy"},
  "reviews":{"pageInfo":{"hasNextPage":false},"nodes":[
    {"state":"CHANGES_REQUESTED","body":"Two things.","submittedAt":"2026-08-31T10:00:00Z",
     "url":"https://github.com/o/r/pull/42#pullrequestreview-1","author":{"login":"reviewer"}},
    {"state":"APPROVED","body":"","submittedAt":"2026-08-31T12:00:00Z",
     "url":"https://github.com/o/r/pull/42#pullrequestreview-2","author":{"login":"other"}}]},
  "reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[
    {"id":"PRRT_1","isResolved":false,"isOutdated":false,"path":"internal/foo.go","line":88,
     "comments":{"pageInfo":{"hasNextPage":false},"nodes":[
       {"databaseId":2043881,"body":"this leaks the fd","createdAt":"2026-08-31T10:00:00Z",
        "url":"https://github.com/o/r/pull/42#discussion_r2043881",
        "diffHunk":"@@ -1,3 +1,4 @@\n+\tf, _ := os.Open(p)","author":{"login":"reviewer"}},
       {"databaseId":2043882,"body":"same in bar.go","createdAt":"2026-08-31T10:05:00Z",
        "url":"https://github.com/o/r/pull/42#discussion_r2043882",
        "diffHunk":"","author":{"login":"other"}}]}},
    {"id":"PRRT_2","isResolved":true,"isOutdated":false,"path":"internal/bar.go","line":12,
     "comments":{"pageInfo":{"hasNextPage":false},"nodes":[
       {"databaseId":2043900,"body":"nit: name","createdAt":"2026-08-31T10:10:00Z",
        "url":"https://github.com/o/r/pull/42#discussion_r2043900",
        "diffHunk":"@@ -10,1 +10,1 @@","author":{"login":"reviewer"}}]}}]}}}}}
`

// fixtureWithOurReply is the same PR after a reply of ours landed on the first thread: what
// a re-fetch shows when the POST went out but the box was never marked. Derived from fixture
// rather than copied, so the two cannot drift.
var fixtureWithOurReply = strings.Replace(fixture,
	`"diffHunk":"","author":{"login":"other"}}]}}`,
	`"diffHunk":"","author":{"login":"other"}},
       {"databaseId":2043999,"body":"Closed the fd in both paths.","createdAt":"2026-08-31T11:00:00Z",
        "url":"https://github.com/o/r/pull/42#discussion_r2043999",
        "diffHunk":"","author":{"login":"guy"}}]}}`, 1)

// signedCommits is what push expects to see before it will post: GitHub reporting every
// commit of the PR as verified. Tests that care about the other case overwrite the file.
const signedCommits = `[{"sha":"abc1234abc1234abc1234abc1234abc1234abc12",
  "commit":{"message":"sand: close the fd in both paths\n\nbody","verification":{"verified":true,"reason":"valid"}}}]
`

// harness puts the fakes in place and points the commands at a temp "box".
func harness(t *testing.T) (remoteBase, ghLog string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("bin/gh", fakeGH, 0o755)
	sshPath := write("ssh-shim", fakeSSH, 0o755)
	fixturePath := write("fixture.json", fixture, 0o644)
	commitsPath := write("commits.json", signedCommits, 0o644)
	checksPath := write("checks.json", checksFixture, 0o644)
	runLogPath := write("run.log", runLogFixture, 0o644)

	ghLog = filepath.Join(dir, "gh.log")
	remoteBase = filepath.Join(dir, "box")

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SAND_SSH", sshPath)
	t.Setenv("GH_LOG", ghLog)
	t.Setenv("GH_FIXTURE", fixturePath)
	t.Setenv("GH_COMMITS", commitsPath)
	t.Setenv("GH_CHECKS", checksPath)
	t.Setenv("GH_RUNLOG", runLogPath)
	t.Setenv("GH_CREATED", filepath.Join(dir, "pr-created"))
	t.Setenv("GH_PR_BODY", filepath.Join(dir, "pr-body"))
	t.Setenv("HOME", dir) // keep any real ~/.config/sand out of it

	flagHost = "box"
	flagRemoteDir = remoteBase
	flagPR = "42"
	flagRemote = "origin"
	flagDryRun, flagAll = false, false
	flagLogLines = defaultLogLines
	// Starting an agent is pull's job, not every test's: the ones about it opt in.
	flagNoAgent, flagAgent, flagRepoDir = true, "", ""
	betweenPosts = 0
	t.Cleanup(func() {
		flagHost, flagRemoteDir, flagPR, flagRemote = "", "", "", ""
		flagNoAgent, flagAgent, flagRepoDir = false, "", ""
		flagLogLines = 0
		betweenPosts = 0
	})
	return remoteBase, ghLog
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNewCreatesIssueWorkspaceAndBranch(t *testing.T) {
	dir, remote := signRepo(t)
	mustRun(t, dir, "git", "switch", "--quiet", "main")
	remoteBase, _ := harness(t)
	boxRepo := filepath.Join(os.Getenv("HOME"), "projects", "r")
	if err := os.MkdirAll(filepath.Dir(boxRepo), 0o755); err != nil {
		t.Fatal(err)
	}
	mustRun(t, filepath.Dir(boxRepo), "git", "clone", "--quiet", remote, boxRepo)
	flagBase = "main"
	t.Cleanup(func() { flagBase = "" })

	if err := runNew([]string{"42"}); err != nil {
		t.Fatalf("new: %v", err)
	}

	branch := "guy/42-fix-the-thing-safely"
	if got := mustRun(t, dir, "git", "branch", "--show-current"); got != branch {
		t.Errorf("Mac branch %q, want %q", got, branch)
	}
	if got := mustRun(t, boxRepo, "git", "branch", "--show-current"); got != branch {
		t.Errorf("box branch %q, want %q", got, branch)
	}
	issueDir := filepath.Join(remoteBase, "o", "r", "issue-42")
	for _, want := range []string{"# Fix the thing, safely!", "https://github.com/o/r/issues/42", "The fd leaks."} {
		if got := read(t, filepath.Join(issueDir, "issue.md")); !strings.Contains(got, want) {
			t.Errorf("issue.md missing %q:\n%s", want, got)
		}
	}
}

func TestPullThenPush(t *testing.T) {
	remoteBase, ghLog := harness(t)
	prDir := filepath.Join(remoteBase, "o", "r", "pr-42")

	if err := runPull(nil); err != nil {
		t.Fatalf("pull: %v", err)
	}

	// The unresolved thread landed; the resolved one was filtered.
	got, err := filepath.Glob(filepath.Join(prDir, "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range got {
		names = append(names, filepath.Base(g))
	}
	if len(names) != 2 || !strings.Contains(strings.Join(names, " "), "c-2043881.md") {
		t.Fatalf("unexpected files on the box: %v", names)
	}

	thread := read(t, filepath.Join(prDir, "c-2043881.md"))
	for _, want := range []string{
		"comment_id: 2043881", "thread_id: PRRT_1", "status: pending",
		"> this leaks the fd", "> same in bar.go", "@@ -1,3 +1,4 @@", replyHeading,
	} {
		if !strings.Contains(thread, want) {
			t.Errorf("thread file missing %q:\n%s", want, thread)
		}
	}
	index := read(t, filepath.Join(prDir, "index.md"))
	for _, want := range []string{"Two things.", "c-2043881.md", "internal/foo.go:88"} {
		if !strings.Contains(index, want) {
			t.Errorf("index missing %q:\n%s", want, index)
		}
	}
	if strings.Contains(index, "pullrequestreview-2") {
		t.Error("an approval with an empty body is not context")
	}

	// The agent on the box drafts a reply and records the commit.
	p := filepath.Join(prDir, "c-2043881.md")
	edited := strings.Replace(read(t, p), "commit: \"\"", "commit: abc1234", 1)
	if !strings.Contains(edited, "commit: abc1234") {
		t.Fatalf("could not find the commit slot:\n%s", edited)
	}
	if err := os.WriteFile(p, []byte(edited+"\nClosed the fd in both paths.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A second pull must not throw that draft away.
	if err := runPull(nil); err != nil {
		t.Fatalf("re-pull: %v", err)
	}
	again := read(t, p)
	if !strings.Contains(again, "Closed the fd in both paths.") || !strings.Contains(again, "commit: abc1234") {
		t.Fatalf("re-pull clobbered the draft:\n%s", again)
	}

	if err := runPush(nil); err != nil {
		t.Fatalf("push: %v", err)
	}
	log := read(t, ghLog)
	if !strings.Contains(log, "repos/o/r/pulls/42/comments/2043881/replies") {
		t.Errorf("reply went to the wrong place:\n%s", log)
	}
	if !strings.Contains(log, "body=Closed the fd in both paths.") {
		t.Errorf("reply body not posted:\n%s", log)
	}
	if !strings.Contains(log, "Fixed in [`abc1234`](https://github.com/o/r/pull/42/commits/abc1234)") {
		t.Errorf("commit link missing from the reply:\n%s", log)
	}

	sent := read(t, p)
	if !strings.Contains(sent, "status: sent") || !strings.Contains(sent, "discussion_r999") {
		t.Fatalf("not marked sent on the box:\n%s", sent)
	}

	// Pushing again posts nothing.
	before := len(read(t, ghLog))
	if err := runPush(nil); err != nil {
		t.Fatalf("second push: %v", err)
	}
	after := read(t, ghLog)
	if strings.Count(after, "/replies") != 1 {
		t.Errorf("re-push posted again (log grew from %d to %d):\n%s", before, len(after), after)
	}
}

// fakeAgent stands in for claude on the box: it records where it ran and what it was
// asked, answers the thread the way an agent would, and reports in stream-json.
const fakeAgent = `#!/bin/sh
{ pwd; echo "$@"; } > "$AGENT_LOG"
for f in "$SAND_PR_DIR"/c-*.md; do
  printf '\n%s\n' "Closed the fd in both paths." >> "$f"
  sed -i 's/^commit: .*/commit: deadbee/' "$f"
done
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"internal/foo.go"}},{"type":"text","text":"fixing the leak"}]}}'
echo 'plain line from some other agent'
echo '{"type":"result","subtype":"success","result":"Answered 1 thread."}'
`

// pull is not done when the files land: an agent on the box has to be started on them, or
// the loop needs a human to ssh in and type the prompt, which is the step that gets skipped.
func TestPullStartsTheAgentAndReportsBack(t *testing.T) {
	remoteBase, _ := harness(t)
	prDir := filepath.Join(remoteBase, "o", "r", "pr-42")
	home := os.Getenv("HOME")

	// The agent runs in the repo checkout, which on the box is ~/projects/<repo>.
	checkout := filepath.Join(home, "projects", "r")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	agentLog := filepath.Join(home, "agent.log")
	agent := filepath.Join(home, "fake-agent")
	if err := os.WriteFile(agent, []byte(fakeAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_LOG", agentLog)
	t.Setenv("SAND_PR_DIR", prDir)

	flagNoAgent = false
	flagAgent = agent
	out := captureStdout(t)

	if err := runPull(nil); err != nil {
		t.Fatalf("pull: %v", err)
	}

	ran := read(t, agentLog)
	cwd, prompt, _ := strings.Cut(strings.TrimSpace(ran), "\n")
	if cwd != checkout {
		t.Errorf("agent ran in %q, want the checkout %q", cwd, checkout)
	}
	for _, want := range []string{"sand skill", prDir, "o/r#42", "make check", "do not push"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}

	printed := out()
	for _, want := range []string{
		"· Read internal/foo.go", // stream-json turned into progress
		"fixing the leak",
		"plain line from some other agent", // anything else passes through
		"Answered 1 thread.",
		"answered c-2043881.md", // read back off the box afterwards
		"deadbee",
		"next: sand up 42",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("output missing %q:\n%s", want, printed)
		}
	}
}

// Two agents in one checkout is not a race to lose, it is a corrupted working tree: they edit
// the same files, commit over each other and answer the same thread twice. Two PRs share a
// repo, and an operator who thinks a run has stalled re-runs it, so this is a Tuesday.
func TestPullRefusesASecondAgentInTheSameCheckout(t *testing.T) {
	remoteBase, _ := harness(t)
	home := os.Getenv("HOME")
	if err := os.MkdirAll(filepath.Join(home, "projects", "r"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Stand in for the agent that is already running: hold its lock, nothing else.
	lock := agentLock(remoteBase, "r")
	if err := os.MkdirAll(filepath.Dir(lock), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("could not take the lock the test is about: %v", err)
	}

	ran := filepath.Join(home, "ran")
	agent := filepath.Join(home, "fake-agent")
	if err := os.WriteFile(agent, []byte("#!/bin/sh\ntouch \""+ran+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	flagNoAgent, flagAgent = false, agent
	captureStdout(t)

	err = runPull(nil)
	if err == nil {
		t.Fatal("pull started a second agent in a locked checkout")
	}
	if !strings.Contains(err.Error(), "already working") {
		t.Errorf("error does not say an agent is already there: %v", err)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("the second agent ran anyway")
	}
}

// A harness that is not on the box's PATH is the most likely first failure on a new box, and
// it used to be reported as a lock failure: flock exits 69 when it cannot exec the command,
// which was this tool's own code for "no flock". So the operator was told the checkout could
// not be locked when the lock was fine and the agent binary was simply missing.
func TestPullSaysWhenTheHarnessIsMissingOnTheBox(t *testing.T) {
	remoteBase, _ := harness(t)
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), "projects", "r"), 0o755); err != nil {
		t.Fatal(err)
	}
	flagNoAgent, flagAgent = false, "sand-no-such-harness"
	captureStdout(t)

	err := runPull(nil)
	if err == nil {
		t.Fatal("pull reported success with no agent binary on the box")
	}
	if strings.Contains(err.Error(), "lock") {
		t.Errorf("a missing harness was reported as a lock failure: %v", err)
	}
	for _, want := range []string{"sand-no-such-harness", "PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
	// And nothing was started, so the lock was never taken either.
	if _, statErr := os.Stat(agentLock(remoteBase, "r")); statErr == nil {
		t.Error("took the lock for an agent that could not run")
	}
}

// Everything already answered and posted: starting an agent there costs a turn to be told
// there is nothing to do.
func TestPullSkipsTheAgentWhenNothingIsPending(t *testing.T) {
	remoteBase, _ := harness(t)
	if err := runPull(nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	p := filepath.Join(remoteBase, "o", "r", "pr-42", "c-2043881.md")
	if err := os.WriteFile(p, []byte(read(t, p)+"\nAnswered.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runPush(nil); err != nil {
		t.Fatalf("push: %v", err)
	}

	flagNoAgent = false
	flagAgent = filepath.Join(t.TempDir(), "never-runs") // running this would fail
	out := captureStdout(t)
	if err := runPull(nil); err != nil {
		t.Fatalf("re-pull: %v", err)
	}
	if printed := out(); !strings.Contains(printed, "nothing pending") {
		t.Errorf("started an agent with everything sent:\n%s", printed)
	}
}

// captureStdout collects what the commands print, since progress reporting is the feature
// under test here rather than a side effect.
func captureStdout(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	real := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	var got string
	var once sync.Once
	read := func() string {
		once.Do(func() {
			os.Stdout = real
			w.Close()
			got = <-done
		})
		return got
	}
	t.Cleanup(func() { read() })
	return read
}

// With no PR argument, the PR comes from the branch checked out on the Mac.
func TestPullDetectsPRFromBranch(t *testing.T) {
	remoteBase, ghLog := harness(t)
	flagPR = ""

	if err := runPull(nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	// Named repo, named head: the question with no local remote in it. The log is one
	// argument per line, hence the two.
	log := read(t, ghLog)
	if !strings.Contains(log, "\n--head\n") || !strings.Contains(log, "number,title,url,headRefName") {
		t.Errorf("did not ask gh for the current branch's PR by repo and head:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(remoteBase, "o", "r", "pr-42", "c-2043881.md")); err != nil {
		t.Errorf("nothing landed for the detected PR: %v", err)
	}
}

func TestPullDryRunTouchesNothing(t *testing.T) {
	remoteBase, _ := harness(t)
	flagDryRun = true
	if err := runPull(nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if _, err := os.Stat(filepath.Join(remoteBase, "o")); !os.IsNotExist(err) {
		t.Errorf("dry run wrote to the box: %v", err)
	}
}

// Replies quote commit hashes, and signing rewrites them, so posting before the branch is
// signed publishes hashes that are about to stop existing. push has to stop instead.
// The reply is posted, then the box is told. Everything in between is a way to lose the
// telling: a dropped ssh, a rebooted box, a full disk, Ctrl-C in a batch that sleeps a
// second between replies. The operator then does what every message in this tool says and
// re-runs, which used to post the same reply a second time and notify every subscriber
// again. GitHub is now asked what is already on the thread, so the re-run re-marks it.
func TestPushDoesNotPostTwiceAfterTheMarkingIsLost(t *testing.T) {
	remoteBase, ghLog := harness(t)
	if err := runPull(nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	prDir := filepath.Join(remoteBase, "o", "r", "pr-42")
	p := filepath.Join(prDir, "c-2043881.md")
	if err := os.WriteFile(p, []byte(read(t, p)+"\nClosed the fd in both paths.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The box stops accepting writes the moment the POST is away.
	if err := os.Chmod(prDir, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := runPush(nil); err != nil {
		t.Fatalf("push: %v", err) // a lost marking is a warning now, not a failure
	}
	if err := os.Chmod(prDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(read(t, ghLog), "/replies"); n != 1 {
		t.Fatalf("first push made %d reply POST(s), want 1", n)
	}
	if got := read(t, p); !strings.Contains(got, "status: pending") {
		t.Fatalf("the box was marked after all, so this test proves nothing:\n%s", got)
	}

	// GitHub now has the reply, which is what a re-fetch would show.
	if err := os.WriteFile(os.Getenv("GH_FIXTURE"), []byte(fixtureWithOurReply), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runPush(nil); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if n := strings.Count(read(t, ghLog), "/replies"); n != 1 {
		t.Errorf("the re-run posted the same reply again: %d POST(s) total", n)
	}
	if got := read(t, p); !strings.Contains(got, "status: sent") {
		t.Errorf("the re-run did not re-mark the thread sent:\n%s", got)
	}
}

// Refusing to post is the only safe answer when GitHub cannot say what is already there.
func TestPushRefusesWhenItCannotCheckWhatIsPosted(t *testing.T) {
	remoteBase, ghLog := harness(t)
	if err := runPull(nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	p := filepath.Join(remoteBase, "o", "r", "pr-42", "c-2043881.md")
	if err := os.WriteFile(p, []byte(read(t, p)+"\nClosed the fd.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("GH_FIXTURE"), []byte(`{"errors":[{"message":"upstream is having a day"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runPush(nil)
	if err == nil {
		t.Fatal("push posted without being able to check for replies already posted")
	}
	if !strings.Contains(err.Error(), "already on") && !strings.Contains(err.Error(), "cannot tell") {
		t.Errorf("error does not say why it stopped: %v", err)
	}
	if strings.Contains(read(t, ghLog), "/replies") {
		t.Error("a reply went out anyway")
	}
}

func TestPushRefusesUnsignedCommits(t *testing.T) {
	remoteBase, ghLog := harness(t)
	if err := runPull(nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	p := filepath.Join(remoteBase, "o", "r", "pr-42", "c-2043881.md")
	if err := os.WriteFile(p, []byte(read(t, p)+"\nClosed the fd in both paths.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unsigned := `[{"sha":"1111111111111111111111111111111111111111",
	  "commit":{"message":"sand: close the fd\n\nbody","verification":{"verified":false,"reason":"unsigned"}}},
	 {"sha":"2222222222222222222222222222222222222222",
	  "commit":{"message":"sand: tests","verification":{"verified":true,"reason":"valid"}}}]`
	if err := os.WriteFile(os.Getenv("GH_COMMITS"), []byte(unsigned), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runPush(nil)
	if err == nil {
		t.Fatal("push posted replies onto an unsigned branch")
	}
	for _, want := range []string{"1111111", "unsigned", "sand sign topic"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(read(t, ghLog), "/replies") {
		t.Error("a reply was posted despite the refusal")
	}
	if got := read(t, p); !strings.Contains(got, "status: pending") || strings.Contains(got, "discussion_r999") {
		t.Errorf("a thread was marked sent despite the refusal:\n%s", got)
	}
}

// The preview is worth having before the branch is signed, so --dry-run warns instead.
func TestPushDryRunWarnsAboutUnsignedCommits(t *testing.T) {
	remoteBase, ghLog := harness(t)
	if err := runPull(nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	p := filepath.Join(remoteBase, "o", "r", "pr-42", "c-2043881.md")
	if err := os.WriteFile(p, []byte(read(t, p)+"\nClosed the fd.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unsigned := `[{"sha":"1111111111111111111111111111111111111111",
	  "commit":{"message":"sand: close the fd","verification":{"verified":false,"reason":"unsigned"}}}]`
	if err := os.WriteFile(os.Getenv("GH_COMMITS"), []byte(unsigned), 0o644); err != nil {
		t.Fatal(err)
	}

	flagDryRun = true
	if err := runPush(nil); err != nil {
		t.Fatalf("dry run refused to preview: %v", err)
	}
	if strings.Contains(read(t, ghLog), "/replies") {
		t.Error("dry run posted a reply")
	}
}

// The whole loop end to end on the point that breaks it: the agent commits on a box with no
// key and records that hash, signing on the Mac replaces the commit, and the reply must quote
// what is actually on GitHub rather than the hash that stopped existing.
func TestPushRepointsTheHashSigningMoved(t *testing.T) {
	dir, _ := signRepo(t)
	// The fake gh says the PR's head branch is "topic", and that is the branch the hashes
	// get checked against, so the repo has to agree.
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "topic")
	recorded := mustRun(t, dir, "git", "rev-parse", "--short=7", "HEAD")

	remoteBase, ghLog := harness(t)
	var signOut strings.Builder
	o := signOpts(&signOut, "")
	o.Branch, o.Push = "topic", true
	if _, err := Sign(o); err != nil {
		t.Fatalf("sign: %v\n%s", err, signOut.String())
	}
	signed := mustRun(t, dir, "git", "rev-parse", "--short=7", "origin/topic")
	if signed == recorded {
		t.Fatalf("signing did not move the commit, so there is nothing to test: %s", recorded)
	}

	if err := runPull(nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	p := filepath.Join(remoteBase, "o", "r", "pr-42", "c-2043881.md")
	drafted := strings.Replace(read(t, p), `commit: ""`, "commit: "+recorded, 1)
	if !strings.Contains(drafted, "commit: "+recorded) {
		t.Fatalf("could not put the agent's hash in the file:\n%s", drafted)
	}
	if err := os.WriteFile(p, []byte(drafted+"\nClosed the fd in both paths.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runPush(nil); err != nil {
		t.Fatalf("push: %v", err)
	}
	log := read(t, ghLog)
	if !strings.Contains(log, "Fixed in [`"+signed+"`]") {
		t.Errorf("reply does not quote the signed commit %s:\n%s", signed, log)
	}
	if strings.Contains(log, recorded) {
		t.Errorf("reply still quotes the pre-signing hash %s:\n%s", recorded, log)
	}
	// And the box learns it, so a re-pull does not hand the stale hash back to the agent.
	if got := read(t, p); !strings.Contains(got, "commit: "+signed) {
		t.Errorf("the file on the box kept the stale hash:\n%s", got)
	}
}

// `sand up` is the one command for the Mac's half, so the test is the whole half: an answered
// thread on the box, unsigned commits on the branch, and nothing done by hand.
func TestPushAliasesUp(t *testing.T) {
	cmd, _, err := root().Find([]string{"push"})
	if err != nil || cmd.Name() != "up" {
		t.Fatalf("push resolved to %v, %v", cmd, err)
	}
}

func TestUpRequiresDescriptionBeforeSigning(t *testing.T) {
	dir, _ := signRepo(t)
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "guy/42-fix-the-thing-safely")
	before := mustRun(t, dir, "git", "rev-parse", "HEAD")
	harness(t)
	t.Setenv("GH_PR_MISSING", "1")

	cmd := upCmd()
	flagPR, flagYes = "", true
	t.Cleanup(func() { flagYes = false })
	err := runUp(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "pr-description.md") {
		t.Fatalf("want missing description error, got %v", err)
	}
	if after := mustRun(t, dir, "git", "rev-parse", "HEAD"); after != before {
		t.Errorf("signed before validating the description: %s became %s", before, after)
	}
}

func TestUpCreatesMissingPR(t *testing.T) {
	dir, _ := signRepo(t)
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "guy/42-fix-the-thing-safely")
	remoteBase, ghLog := harness(t)
	prDir := filepath.Join(remoteBase, "o", "r", "issue-42")
	if err := os.MkdirAll(prDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prDir, "pr-description.md"), []byte("Fix the thing without leaking the fd.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_PR_MISSING", "1")

	cmd := upCmd()
	flagPR, flagYes = "", true
	t.Cleanup(func() { flagYes = false })
	out := captureStdout(t)
	if err := runUp(cmd, nil); err != nil {
		t.Fatalf("up: %v\n%s", err, out())
	}

	log := read(t, ghLog)
	for _, want := range []string{"pr", "create", "--head", "guy/42-fix-the-thing-safely", "--body-file"} {
		if !strings.Contains(log, want) {
			t.Errorf("gh log missing %q:\n%s", want, log)
		}
	}
	if got := read(t, os.Getenv("GH_PR_BODY")); got != "Fix the thing without leaking the fd.\n" {
		t.Errorf("PR body %q", got)
	}
	if !strings.Contains(out(), "opened https://github.com/o/r/pull/42") {
		t.Errorf("output did not report the new PR:\n%s", out())
	}
}

func TestUpSignsPushesAndPosts(t *testing.T) {
	dir, _ := signRepo(t)
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "topic")
	recorded := mustRun(t, dir, "git", "rev-parse", "--short=7", "HEAD")
	remoteBase, ghLog := harness(t)

	if err := runPull(nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	p := filepath.Join(remoteBase, "o", "r", "pr-42", "c-2043881.md")
	drafted := strings.Replace(read(t, p), `commit: ""`, "commit: "+recorded, 1)
	if err := os.WriteFile(p, []byte(drafted+"\nClosed the fd in both paths.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := upCmd() // registering the flags is what sets remote, base and the rest
	flagPR, flagYes = "42", true
	t.Cleanup(func() { flagYes = false })
	out := captureStdout(t)
	if err := runUp(cmd, nil); err != nil {
		t.Fatalf("up: %v\n%s", err, out())
	}

	printed := out()
	signed := mustRun(t, dir, "git", "rev-parse", "topic")
	if pushed := mustRun(t, dir, "git", "rev-parse", "origin/topic"); pushed != signed {
		t.Errorf("origin/topic at %s, local topic at %s", pushed, signed)
	}
	for _, want := range []string{
		"1/4 sign", "topic: 3 commit(s), 3 signed now",
		"2/4 push", "3/4 verify", "as verified", "4/4 replies", "posted c-2043881.md",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("output missing %q:\n%s", want, printed)
		}
	}
	if log := read(t, ghLog); !strings.Contains(log, "Fixed in [`"+short(signed)+"`]") {
		t.Errorf("the posted reply does not quote the signed commit %s:\n%s", short(signed), log)
	}
	if got := read(t, p); !strings.Contains(got, "status: sent") {
		t.Errorf("the thread was not marked sent on the box:\n%s", got)
	}
}

// Signing pushes what it rewrote, so step 2 only has work when there was nothing to sign:
// a branch signed in an earlier round, or by hand, that never reached the remote. That is
// the case the step exists for, and posting a reply about commits GitHub cannot see is
// exactly what it prevents.
func TestUpPushesABranchThatNeededNoSigning(t *testing.T) {
	dir, _ := signRepo(t)
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "topic")
	var signOut strings.Builder
	if _, err := Sign(SignOpts{Branch: "topic", Remote: "origin", Base: "main", Yes: true,
		In: strings.NewReader("n\n"), Out: &signOut}); err != nil { // "n" declines the push
		t.Fatalf("signing without pushing: %v\n%s", err, signOut.String())
	}
	if refs := mustRun(t, dir, "git", "for-each-ref", "refs/remotes/origin/topic"); refs != "" {
		t.Fatalf("setup pushed after all: %q", refs)
	}
	harness(t)

	if err := runPull(nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	cmd := upCmd()
	flagPR, flagYes = "42", true
	t.Cleanup(func() { flagYes = false })
	out := captureStdout(t)
	if err := runUp(cmd, nil); err != nil {
		t.Fatalf("up: %v\n%s", err, out())
	}

	printed := out()
	local := mustRun(t, dir, "git", "rev-parse", "topic")
	pushed := mustRun(t, dir, "git", "rev-parse", "--verify", "--quiet", "origin/topic")
	if pushed != local {
		t.Errorf("origin/topic at %q, local topic at %s\n%s", pushed, local, printed)
	}
	// Step 1 pushes it, having nothing to sign but a remote behind the branch, so step 2 is the
	// proof that the ref matches rather than the thing that moved it.
	for _, want := range []string{"signed already", "origin/topic is already at " + short(local)} {
		if !strings.Contains(printed, want) {
			t.Errorf("output missing %q:\n%s", want, printed)
		}
	}
}

// The dry run has to be honest about all four steps at once: no rewrite, no push, no post.
func TestUpDryRunChangesNothing(t *testing.T) {
	dir, _ := signRepo(t)
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "topic")
	before := mustRun(t, dir, "git", "rev-parse", "topic")
	remoteBase, ghLog := harness(t)

	if err := runPull(nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	p := filepath.Join(remoteBase, "o", "r", "pr-42", "c-2043881.md")
	if err := os.WriteFile(p, []byte(read(t, p)+"\nClosed the fd.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := upCmd()
	flagPR, flagYes, flagDryRun = "42", true, true
	t.Cleanup(func() { flagYes, flagDryRun = false, false })
	out := captureStdout(t)
	if err := runUp(cmd, nil); err != nil {
		t.Fatalf("up --dry-run: %v\n%s", err, out())
	}

	if after := mustRun(t, dir, "git", "rev-parse", "topic"); after != before {
		t.Errorf("dry run moved the branch from %s to %s", before, after)
	}
	if refs := mustRun(t, dir, "git", "for-each-ref", "refs/remotes/origin/topic"); refs != "" {
		t.Errorf("dry run pushed: %q", refs)
	}
	if log := read(t, ghLog); strings.Contains(log, "/replies") {
		t.Errorf("dry run posted a reply:\n%s", log)
	}
	if printed := out(); !strings.Contains(printed, "would push") {
		t.Errorf("dry run did not say what it would push:\n%s", printed)
	}
}

func TestPushWithNothingPulled(t *testing.T) {
	harness(t)
	err := runPush(nil)
	if err == nil || !strings.Contains(err.Error(), "pull") {
		t.Errorf("want a pull-first error, got %v", err)
	}
}
