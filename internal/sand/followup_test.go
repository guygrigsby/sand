package sand

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewReviewCommentReopensSentThread(t *testing.T) {
	base, _ := harness(t)
	if err := runPull(nil); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(base, "o", "r", "pr-42", "c-2043881.md")
	raw := strings.Replace(read(t, p), "status: pending", "status: sent", 1) + "\nClosed the fd in both paths.\n"
	if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	newer := strings.Replace(fixtureWithOurReply, `"diffHunk":"","author":{"login":"guy"}}]}}`, `"diffHunk":"","author":{"login":"guy"}},{"databaseId":2044000,"body":"The error path still leaks; please fix it too.","createdAt":"2026-09-01T12:00:00Z","author":{"login":"reviewer"}}]}}`, 1)
	if err := os.WriteFile(os.Getenv("GH_FIXTURE"), []byte(newer), 0600); err != nil {
		t.Fatal(err)
	}
	flagNoAgent, flagAgent, flagRepoDir = false, "true", t.TempDir()
	output := captureStdout(t)
	if err := runPull(nil); err != nil {
		t.Fatal(err)
	}
	got, err := Parse(read(t, p))
	if err != nil {
		t.Fatal(err)
	}
	if got.Sent() {
		t.Fatalf("reviewer's new follow-up was kept sent and skipped\n%s", output())
	}
	if got.Reply != "" || got.Meta.Commit != "" || got.Meta.ReplyURL != "" {
		t.Fatalf("reopened thread still carries the previous reply: %+v", got)
	}
	if !strings.Contains(read(t, p), "Closed the fd in both paths.") {
		t.Fatal("lost the posted reply from the conversation")
	}
}

func TestFollowupMergePreservesDraftsAndOwnReplies(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sent       bool
		replyURL   string
		nextAuthor string
		nextBody   string
		pending    bool
	}{
		{"pending draft", false, "", "reviewer", "Another observation.", true},
		{"own followup", true, "https://example.test/reply", "guy", "Another observation.", false},
		{"reviewer followup", true, "https://example.test/reply", "reviewer", "Another observation.", true},
		{"legacy sent marking", true, "", "reviewer", "Another observation.", true},
		{"legacy quoted followup", true, "", "reviewer", "You said: Closed the fd. It still leaks.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := sample()
			old.Reply, old.Meta.Commit, old.Meta.ReplyURL = "Closed the fd.", "abc1234", tc.replyURL
			if tc.sent {
				old.Meta.Status = StatusSent
			}
			fresh := sample()
			fresh.Comments = append(fresh.Comments,
				Comment{Author: "guy", Body: old.Reply, URL: "https://example.test/reply"},
				Comment{Author: tc.nextAuthor, Body: tc.nextBody})
			fresh.Merge(old)
			if (fresh.Meta.Status == StatusPending) != tc.pending {
				t.Fatalf("status=%s", fresh.Meta.Status)
			}
			if tc.sent && tc.pending {
				if fresh.Reply != "" || fresh.Meta.Commit != "" {
					t.Fatal("old reply could be posted again")
				}
			} else if fresh.Reply != old.Reply || fresh.Meta.Commit != old.Meta.Commit {
				t.Fatal("lost the existing reply")
			}
		})
	}
}
