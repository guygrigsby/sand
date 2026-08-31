package sand

import (
	"strings"
	"testing"
)

func sample() Thread {
	return Thread{
		Meta: Meta{
			ThreadID:  "PRRT_kwDOabc",
			CommentID: 2043881,
			URL:       "https://github.com/o/r/pull/42#discussion_r2043881",
			Path:      "internal/foo.go",
			Line:      88,
			Author:    "reviewer",
			Status:    StatusPending,
		},
		Comments: []Comment{
			{Author: "reviewer", CreatedAt: "2026-08-31T10:00:00Z", Body: "this leaks the fd\n\non the error path"},
			{Author: "otherdev", CreatedAt: "2026-08-31T11:00:00Z", Body: "same shape in bar.go"},
		},
		DiffHunk: "@@ -1,3 +1,4 @@\n+\tf, _ := os.Open(p)",
	}
}

func renderParse(t *testing.T, th Thread) Thread {
	t.Helper()
	s, err := th.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got, err := Parse(s)
	if err != nil {
		t.Fatalf("parse: %v\n---\n%s", err, s)
	}
	return got
}

func TestRoundTripEmptyReply(t *testing.T) {
	got := renderParse(t, sample())
	if got.Meta != sample().Meta {
		t.Errorf("front matter changed\nwant %+v\ngot  %+v", sample().Meta, got.Meta)
	}
	if got.Reply != "" {
		t.Errorf("reply should be empty, got %q", got.Reply)
	}
}

func TestRoundTripWithReply(t *testing.T) {
	th := sample()
	th.Reply = "Closed the fd in both paths.\n\n```go\ndefer f.Close()\n```"
	th.Meta.Commit = "abc1234"

	got := renderParse(t, th)
	if got.Reply != th.Reply {
		t.Errorf("reply changed\nwant %q\ngot  %q", th.Reply, got.Reply)
	}
	if got.Meta.Commit != "abc1234" {
		t.Errorf("commit changed: %q", got.Meta.Commit)
	}
}

// A reply that quotes the reply heading must not truncate itself.
func TestReplyContainingHeading(t *testing.T) {
	th := sample()
	th.Reply = "As the file says:\n\n## reply\n\nthat is the slot."
	if got := renderParse(t, th); got.Reply != th.Reply {
		t.Errorf("want %q, got %q", th.Reply, got.Reply)
	}
}

func TestParseErrors(t *testing.T) {
	for _, s := range []string{
		"no front matter here",
		"---\npath: foo.go\n---\n\n## reply\n", // no comment_id
		"---\n: : :\n---\n",                    // not YAML
	} {
		if _, err := Parse(s); err == nil {
			t.Errorf("expected an error for %q", s)
		}
	}
}

func TestMergeKeepsBoxSideEdits(t *testing.T) {
	old := sample()
	old.Reply = "done"
	old.Meta.Commit = "abc1234"
	old.Meta.Status = StatusSent
	old.Meta.RepliedAt = "2026-08-31T12:00:00Z"
	old.Meta.ReplyURL = "https://github.com/o/r/pull/42#discussion_r2043999"

	// A fresh pull: same thread, a new comment, and no knowledge of the draft.
	fresh := sample()
	fresh.Comments = append(fresh.Comments, Comment{Author: "reviewer", CreatedAt: "2026-09-01T09:00:00Z", Body: "ping"})
	fresh.Merge(old)

	if fresh.Reply != "done" || fresh.Meta.Commit != "abc1234" {
		t.Fatalf("draft lost: reply=%q commit=%q", fresh.Reply, fresh.Meta.Commit)
	}
	if !fresh.Sent() || fresh.Meta.ReplyURL != old.Meta.ReplyURL {
		t.Fatalf("sent state lost: %+v", fresh.Meta)
	}
	if len(fresh.Comments) != 3 {
		t.Fatalf("upstream comment lost: %d comments", len(fresh.Comments))
	}
}

func TestReplaceFrontMatterKeepsBody(t *testing.T) {
	th := sample()
	th.Reply = "fixed"
	orig, err := th.Render()
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := strings.Cut(orig, "\n---\n")

	m := th.Meta
	m.Status = StatusSent
	m.ReplyURL = "https://example.test/r/1"
	out, err := ReplaceFrontMatter(orig, m)
	if err != nil {
		t.Fatal(err)
	}
	if _, gotBody, _ := strings.Cut(out, "\n---\n"); gotBody != body {
		t.Errorf("body changed\nwant %q\ngot  %q", body, gotBody)
	}
	got, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Sent() || got.Reply != "fixed" {
		t.Errorf("bad reparse: %+v reply=%q", got.Meta, got.Reply)
	}
}

func TestRemotePathIsShellSafe(t *testing.T) {
	tgt := Target{Owner: "o;rm -rf /", Repo: "r r", Number: 42}
	got := tgt.RemotePath("~/.sand")
	if want := "~/.sand/o-rm--rf-/r-r/pr-42"; got != want {
		t.Errorf("want %q, got %q", want, got)
	}
	if q := remoteQuote(got); !strings.HasPrefix(q, "~/'") || strings.Contains(q[3:], "~") {
		t.Errorf("only a leading ~ may escape quoting: %q", q)
	}
	if q := remoteQuote("/tmp/it's"); q != `'/tmp/it'\''s'` {
		t.Errorf("quote: %q", q)
	}
}
