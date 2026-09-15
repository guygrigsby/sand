package sand

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseReviewComment(t *testing.T) {
	file := filepath.Join(t.TempDir(), "comment-001.md")
	body := "This return can expose a partially initialized value.\n\nGuard it before assigning `result`.\n"
	raw := "---\npath: internal/foo.go\nline: 88\nside: right\n---\n\n" + body
	if err := os.WriteFile(file, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := parseReviewComment(file)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "internal/foo.go" || got.Line != 88 || got.Side != "RIGHT" || got.Body != body {
		t.Fatalf("unexpected comment: %#v", got)
	}
}

func TestParseReviewCommentRejectsInvalidFiles(t *testing.T) {
	for name, raw := range map[string]string{
		"absolute path": "---\npath: /tmp/foo.go\nline: 2\nside: RIGHT\n---\n\nFinding.\n",
		"parent path":   "---\npath: ../foo.go\nline: 2\nside: RIGHT\n---\n\nFinding.\n",
		"zero line":     "---\npath: foo.go\nline: 0\nside: RIGHT\n---\n\nFinding.\n",
		"bad side":      "---\npath: foo.go\nline: 2\nside: MIDDLE\n---\n\nFinding.\n",
		"empty body":    "---\npath: foo.go\nline: 2\nside: RIGHT\n---\n\n",
		"unknown field": "---\npath: foo.go\nline: 2\nside: RIGHT\nreply_to: 9\n---\n\nFinding.\n",
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "comment.md")
			if err := os.WriteFile(file, []byte(raw), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := parseReviewComment(file); err == nil {
				t.Fatal("accepted invalid review comment")
			}
		})
	}
}

func TestReviewDraftPathPreservesBranchHierarchy(t *testing.T) {
	got := reviewDraftPath("~/.sand", Target{Owner: "o", Repo: "r", Branch: "person/topic"})
	if got != "~/.sand/o/r/reviews/person/topic" {
		t.Fatalf("review draft path = %q", got)
	}
}

func TestPRReviewCreatesOnePendingReviewAndConsumesDraft(t *testing.T) {
	dir, _ := signRepo(t)
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "topic")
	remoteBase, ghLog := harness(t)
	flagPR = ""
	t.Cleanup(func() { flagPR = "" })
	draftDir := filepath.Join(remoteBase, "o", "r", "reviews", "topic")
	if err := os.MkdirAll(draftDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nbranch: topic\ncommit: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nbase: origin/main\n---\n"
	comment := "---\npath: internal/foo.go\nline: 88\nside: RIGHT\n---\n\nClose the descriptor on this error path.\n"
	if err := os.WriteFile(filepath.Join(draftDir, "review.md"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(draftDir, "comment-001.md"), []byte(comment), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t)
	if err := runPRReview(nil, os.Stdout); err != nil {
		t.Fatalf("pr review: %v\n%s\ngh:\n%s", err, out(), read(t, ghLog))
	}

	var payload struct {
		CommitID string `json:"commit_id"`
		Event    string `json:"event"`
		Comments []struct {
			Path string `json:"path"`
			Line int    `json:"line"`
			Side string `json:"side"`
			Body string `json:"body"`
		} `json:"comments"`
	}
	rawPayload := read(t, os.Getenv("GH_REVIEW_INPUT"))
	if err := json.Unmarshal([]byte(rawPayload), &payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rawPayload, `"event"`) {
		t.Fatalf("review payload submits the review instead of leaving it pending: %s", rawPayload)
	}
	if payload.CommitID != strings.Repeat("a", 40) || payload.Event != "" || len(payload.Comments) != 1 {
		t.Fatalf("unexpected review payload: %#v", payload)
	}
	got := payload.Comments[0]
	if got.Path != "internal/foo.go" || got.Line != 88 || got.Side != "RIGHT" || !strings.Contains(got.Body, "Close the descriptor") {
		t.Fatalf("unexpected inline comment: %#v", got)
	}
	if _, err := os.Stat(filepath.Join(draftDir, "review.md")); !os.IsNotExist(err) {
		t.Errorf("successful review did not consume the manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(draftDir, "comment-001.md")); !os.IsNotExist(err) {
		t.Errorf("successful review did not consume the comment: %v", err)
	}
	for _, want := range []string{"started a pending review", "Files changed tab", "pullrequestreview-99"} {
		if !strings.Contains(out(), want) {
			t.Errorf("output missing %q:\n%s", want, out())
		}
	}
	if strings.Contains(read(t, ghLog), "/replies") {
		t.Error("new review comments were posted through the threaded reply endpoint")
	}
}

func TestPRReviewRefusesAStaleHead(t *testing.T) {
	dir, _ := signRepo(t)
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "topic")
	remoteBase, ghLog := harness(t)
	flagPR = ""
	draftDir := filepath.Join(remoteBase, "o", "r", "reviews", "topic")
	if err := os.MkdirAll(draftDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(draftDir, "review.md"), []byte("---\nbranch: topic\ncommit: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(draftDir, "comment.md"), []byte("---\npath: foo.go\nline: 1\nside: RIGHT\n---\n\nFinding.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runPRReview(nil, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "review it again") {
		t.Fatalf("want stale-head refusal, got %v", err)
	}
	if strings.Contains(read(t, ghLog), "pulls/42/reviews") {
		t.Error("created a review from stale comments")
	}
	if _, err := os.Stat(filepath.Join(draftDir, "review.md")); err != nil {
		t.Errorf("stale draft was consumed: %v", err)
	}
}

func TestPRReviewFailureKeepsDraft(t *testing.T) {
	dir, _ := signRepo(t)
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "topic")
	remoteBase, _ := harness(t)
	flagPR = ""
	t.Setenv("GH_REVIEW_EXIT", "1")
	draftDir := filepath.Join(remoteBase, "o", "r", "reviews", "topic")
	if err := os.MkdirAll(draftDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(draftDir, "review.md")
	comment := filepath.Join(draftDir, "comment.md")
	if err := os.WriteFile(manifest, []byte("---\nbranch: topic\ncommit: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(comment, []byte("---\npath: foo.go\nline: 1\nside: RIGHT\n---\n\nFinding.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runPRReview(nil, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "line must be part of the diff") {
		t.Fatalf("want GitHub validation error, got %v", err)
	}
	for _, file := range []string{manifest, comment} {
		if _, err := os.Stat(file); err != nil {
			t.Errorf("failed review consumed %s: %v", filepath.Base(file), err)
		}
	}
}

func TestPRReviewDryRunKeepsDraft(t *testing.T) {
	dir, _ := signRepo(t)
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "topic")
	remoteBase, ghLog := harness(t)
	flagPR, flagDryRun = "", true
	t.Cleanup(func() { flagDryRun = false })
	draftDir := filepath.Join(remoteBase, "o", "r", "reviews", "topic")
	if err := os.MkdirAll(draftDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(draftDir, "review.md"), []byte("---\nbranch: topic\ncommit: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(draftDir, "comment.md"), []byte("---\npath: foo.go\nline: 1\nside: RIGHT\n---\n\nFinding.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runPRReview(nil, os.Stdout); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(t, ghLog), "pulls/42/reviews") {
		t.Error("dry run created a review")
	}
	if _, err := os.Stat(filepath.Join(draftDir, "review.md")); err != nil {
		t.Errorf("dry run consumed the draft: %v", err)
	}
}
