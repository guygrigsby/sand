package sand

// End to end over the real code paths: a fake `gh` on PATH stands in for GitHub, and a
// shim that runs the remote command locally stands in for ssh. Everything between —
// the GraphQL decode, the file format, tar in both directions, the merge on re-pull,
// the reply POST and the sent marking — is the actual implementation.

import (
	"os"
	"path/filepath"
	"strings"
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
  *"/commits?"*)  cat "$GH_COMMITS" ;;
  *graphql*)      cat "$GH_FIXTURE" ;;
  *"pr view"*)    echo '{"number":42,"url":"https://github.com/o/r/pull/42","title":"Fix the thing","headRefName":"topic"}' ;;
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

	ghLog = filepath.Join(dir, "gh.log")
	remoteBase = filepath.Join(dir, "box")

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SAND_SSH", sshPath)
	t.Setenv("GH_LOG", ghLog)
	t.Setenv("GH_FIXTURE", fixturePath)
	t.Setenv("GH_COMMITS", commitsPath)
	t.Setenv("HOME", dir) // keep any real ~/.config/sand out of it

	flagHost = "box"
	flagRemoteDir = remoteBase
	flagPR = "42"
	flagDryRun, flagAll = false, false
	betweenPosts = 0
	t.Cleanup(func() {
		flagHost, flagRemoteDir, flagPR = "", "", ""
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

// With no PR argument, the PR comes from the branch checked out on the Mac.
func TestPullDetectsPRFromBranch(t *testing.T) {
	remoteBase, ghLog := harness(t)
	flagPR = ""

	if err := runPull(nil); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !strings.Contains(read(t, ghLog), "number,headRefName") {
		t.Errorf("did not ask gh for the current branch's PR:\n%s", read(t, ghLog))
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

func TestPushWithNothingPulled(t *testing.T) {
	harness(t)
	err := runPush(nil)
	if err == nil || !strings.Contains(err.Error(), "pull") {
		t.Errorf("want a pull-first error, got %v", err)
	}
}
