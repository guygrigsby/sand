package sand

import (
	"net/http"
	"testing"
	"time"
)

func TestSplitResponse(t *testing.T) {
	out := "HTTP/2.0 403 Forbidden\r\nRetry-After: 60\r\nContent-Type: application/json\r\n\r\n" +
		`{"message":"You have exceeded a secondary rate limit"}`
	status, headers, body := splitResponse(out)
	if status != http.StatusForbidden {
		t.Fatalf("status %d", status)
	}
	if got := headers.Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After %q", got)
	}
	if body == "" || body[0] != '{' {
		t.Fatalf("body %q", body)
	}

	// gh failing before it makes a request: no status line, everything is the body.
	if status, _, body := splitResponse("gh: not authenticated\n"); status != 0 || body == "" {
		t.Fatalf("status %d body %q", status, body)
	}
}

func TestRetryAfterBothForms(t *testing.T) {
	secs := &httpError{headers: http.Header{"Retry-After": {"30"}}}
	if got := retryAfter(secs); got != 30*time.Second {
		t.Errorf("seconds form: %v", got)
	}

	when := time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat)
	date := &httpError{headers: http.Header{"Retry-After": {when}}}
	if got := retryAfter(date); got < 30*time.Second || got > time.Minute {
		t.Errorf("HTTP-date form: %v", got)
	}

	// A date in the past, a missing header, and a non-HTTP error all mean "no wait".
	past := &httpError{headers: http.Header{"Retry-After": {time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)}}}
	for _, e := range []error{past, &httpError{headers: http.Header{}}, errString("boom")} {
		if got := retryAfter(e); got != 0 {
			t.Errorf("%v: want 0, got %v", e, got)
		}
	}
}

func TestHTTPErrorUsesAPIMessage(t *testing.T) {
	e := &httpError{status: 422, body: `{"message":"Validation Failed"}`}
	if got := e.Error(); got != "HTTP 422: Validation Failed" {
		t.Errorf("got %q", got)
	}
	plain := &httpError{status: 500, body: "upstream broke"}
	if got := plain.Error(); got != "HTTP 500: upstream broke" {
		t.Errorf("got %q", got)
	}
	detail := &httpError{status: 422, body: `{"message":"Validation Failed","errors":[{"resource":"PullRequestReview","field":"user_id","message":"user_id can only have one pending review per pull request"}]}`}
	want := "HTTP 422: Validation Failed (user_id: user_id can only have one pending review per pull request)"
	if got := detail.Error(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestComposeReplyLinksCommit(t *testing.T) {
	tgt := Target{Owner: "o", Repo: "r", Number: 42, URL: "https://github.com/o/r/pull/42"}
	th := Thread{Meta: Meta{Commit: "abc1234"}, Reply: "  Fixed.\n"}
	want := "Fixed.\n\nFixed in [`abc1234`](https://github.com/o/r/pull/42/commits/abc1234)"
	if got := composeReply(tgt, th); got != want {
		t.Errorf("\nwant %q\ngot  %q", want, got)
	}

	th.Meta.Commit = ""
	if got := composeReply(tgt, th); got != "Fixed." {
		t.Errorf("no commit: %q", got)
	}
}

func TestResolveTargetFromURL(t *testing.T) {
	got, err := ResolveTarget("https://github.com/guy/gh-sync/pull/7/files")
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != "guy" || got.Repo != "gh-sync" || got.Number != 7 {
		t.Errorf("%+v", got)
	}
}

// A bare `gh pr view` works out the head repo from the local remotes, and the box is a remote:
// in another repo it read box:projects/other as owner "projects", looked for a head of
// `projects:guy/1532-instance-dormancy`, and said there was no open PR for a branch whose PR was
// open. Every command that takes the PR from the current branch came through here, while status
// and up were fine, because they name the repo and the head and leave nothing to guess.
func TestResolveTargetIgnoresTheBoxRemoteWhenFindingThePR(t *testing.T) {
	signRepo(t)
	harness(t)
	t.Setenv("GH_BAD_HEAD_GUESS", "1")

	got, err := ResolveTarget("")
	if err != nil {
		t.Fatalf("no PR found for the branch checked out here: %v", err)
	}
	if got.Number != 42 || got.Slug() != "o/r" {
		t.Errorf("%+v", got)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
