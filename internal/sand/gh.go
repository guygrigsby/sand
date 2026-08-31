package sand

// GitHub access goes through the `gh` CLI: auth is already solved there on the Mac, and
// a token that never touches this program is a token it cannot leak.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Target is the pull request being worked on.
type Target struct {
	Owner  string
	Repo   string
	Number int

	// Filled in by Fetch.
	Title  string
	URL    string
	Branch string
	Author string
}

func (t Target) Slug() string { return t.Owner + "/" + t.Repo }

// Review is a review summary body (the text submitted with an approval or a
// changes-requested). Read-only context: GitHub has no in-thread reply for it.
type Review struct {
	Author      string
	State       string
	Body        string
	SubmittedAt string
	URL         string
}

var prURLRe = regexp.MustCompile(`github\.com/([^/]+)/([^/]+)/pull/(\d+)`)

// ResolveTarget works out which PR to act on: an explicit URL, an explicit number in
// the current repo, or the PR for the current branch.
func ResolveTarget(arg string) (Target, error) {
	if m := prURLRe.FindStringSubmatch(arg); m != nil {
		n, _ := strconv.Atoi(m[3])
		return Target{Owner: m[1], Repo: strings.TrimSuffix(m[2], ".git"), Number: n}, nil
	}

	var repo struct{ NameWithOwner string }
	if err := ghJSON(&repo, "repo", "view", "--json", "nameWithOwner"); err != nil {
		return Target{}, fmt.Errorf("not in a GitHub repo checkout (%w)", err)
	}
	owner, name, ok := strings.Cut(repo.NameWithOwner, "/")
	if !ok {
		return Target{}, fmt.Errorf("unexpected repo name %q from gh", repo.NameWithOwner)
	}
	t := Target{Owner: owner, Repo: name}

	if arg != "" {
		n, err := strconv.Atoi(arg)
		if err != nil {
			return Target{}, fmt.Errorf("PR must be a number or a github.com pull URL, got %q", arg)
		}
		t.Number = n
		return t, nil
	}

	// No PR given: the branch checked out here is the answer. gh matches it against
	// the open PRs' head branches.
	var pr struct {
		Number      int    `json:"number"`
		HeadRefName string `json:"headRefName"`
	}
	if err := ghJSON(&pr, "pr", "view", "--json", "number,headRefName"); err != nil {
		return Target{}, fmt.Errorf("no open PR for branch %q in %s; pass a number or URL (%w)",
			currentBranch(), t.Slug(), err)
	}
	t.Number, t.Branch = pr.Number, pr.HeadRefName
	return t, nil
}

// currentBranch is for error messages only, so a failure to read it is not worth
// reporting on its own.
func currentBranch() string {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "HEAD"
	}
	return strings.TrimSpace(string(out))
}

// LoadURL fills in the PR's web URL, which push needs to link the fixing commit and
// which pull gets for free from the thread query. Asking gh rather than assembling
// https://github.com/... keeps it right on an Enterprise host.
func (t *Target) LoadURL() error {
	if t.URL != "" {
		return nil
	}
	var pr struct {
		URL         string `json:"url"`
		Title       string `json:"title"`
		HeadRefName string `json:"headRefName"`
	}
	err := ghJSON(&pr, "pr", "view", strconv.Itoa(t.Number),
		"--repo", t.Slug(), "--json", "url,title,headRefName")
	if err != nil {
		return err
	}
	t.URL, t.Title, t.Branch = pr.URL, pr.Title, pr.HeadRefName
	return nil
}

// PRCommit is one commit of the pull request, carrying GitHub's own verdict on its
// signature rather than this program's.
type PRCommit struct {
	SHA     string
	Subject string
	Reason  string // GitHub's word for why it is not verified: "unsigned", "unknown_key", ...
}

// UnverifiedCommits lists the PR's commits GitHub does not report as verified. It asks
// GitHub rather than the local checkout on purpose: what a reply's `commit:` hash points
// at is the pushed history, and the Mac's copy of the branch can be behind or ahead of it.
// Paging is by hand because `gh api --paginate` concatenates JSON arrays.
func UnverifiedCommits(t Target) ([]PRCommit, error) {
	const perPage = 100
	var unverified []PRCommit
	for page := 1; ; page++ {
		var batch []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message      string `json:"message"`
				Verification struct {
					Verified bool   `json:"verified"`
					Reason   string `json:"reason"`
				} `json:"verification"`
			} `json:"commit"`
		}
		path := fmt.Sprintf("repos/%s/pulls/%d/commits?per_page=%d&page=%d", t.Slug(), t.Number, perPage, page)
		if err := ghJSON(&batch, "api", path); err != nil {
			return nil, err
		}
		for _, c := range batch {
			if c.Commit.Verification.Verified {
				continue
			}
			subject, _, _ := strings.Cut(c.Commit.Message, "\n")
			reason := c.Commit.Verification.Reason
			if reason == "" {
				reason = "unsigned"
			}
			unverified = append(unverified, PRCommit{SHA: c.SHA, Subject: subject, Reason: reason})
		}
		if len(batch) < perPage {
			return unverified, nil
		}
	}
}

// CommitURL links a commit as it appears on the PR, which is where a reviewer reading
// the reply wants to land.
func (t Target) CommitURL(sha string) string {
	return fmt.Sprintf("%s/commits/%s", t.URL, sha)
}

const threadQuery = `
query($owner: String!, $repo: String!, $number: Int!, $cursor: String) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $number) {
      number title url headRefName
      author { login }
      reviews(first: 100) {
        pageInfo { hasNextPage }
        nodes { state body submittedAt url author { login } }
      }
      reviewThreads(first: 50, after: $cursor) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id isResolved isOutdated path line startLine
          comments(first: 100) {
            pageInfo { hasNextPage }
            nodes { databaseId body createdAt url diffHunk author { login } }
          }
        }
      }
    }
  }
}`

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type queryResult struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				Number      int    `json:"number"`
				Title       string `json:"title"`
				URL         string `json:"url"`
				HeadRefName string `json:"headRefName"`
				Author      struct {
					Login string `json:"login"`
				} `json:"author"`
				Reviews struct {
					PageInfo pageInfo `json:"pageInfo"`
					Nodes    []struct {
						State       string `json:"state"`
						Body        string `json:"body"`
						SubmittedAt string `json:"submittedAt"`
						URL         string `json:"url"`
						Author      struct {
							Login string `json:"login"`
						} `json:"author"`
					} `json:"nodes"`
				} `json:"reviews"`
				ReviewThreads struct {
					PageInfo pageInfo `json:"pageInfo"`
					Nodes    []struct {
						ID         string `json:"id"`
						IsResolved bool   `json:"isResolved"`
						IsOutdated bool   `json:"isOutdated"`
						Path       string `json:"path"`
						Line       *int   `json:"line"`
						StartLine  *int   `json:"startLine"`
						Comments   struct {
							PageInfo pageInfo `json:"pageInfo"`
							Nodes    []struct {
								DatabaseID int64  `json:"databaseId"`
								Body       string `json:"body"`
								CreatedAt  string `json:"createdAt"`
								URL        string `json:"url"`
								DiffHunk   string `json:"diffHunk"`
								Author     struct {
									Login string `json:"login"`
								} `json:"author"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`

	// GraphQL reports field and permission errors with HTTP 200 and a populated
	// errors array, so a decode that "worked" still has to be checked.
	Errors []struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"errors"`
}

func (r queryResult) err() error {
	if len(r.Errors) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(r.Errors))
	for _, e := range r.Errors {
		msgs = append(msgs, strings.TrimSpace(e.Type+" "+e.Message))
	}
	return fmt.Errorf("GitHub GraphQL: %s", strings.Join(msgs, "; "))
}

// Fetch pulls every review thread and review summary for the PR, filling in the
// target's metadata as a side effect. Resolved threads come back too; the caller
// decides. warn reports anything the query had to leave on the table.
func Fetch(t *Target, warn func(string)) ([]Thread, []Review, error) {
	var (
		threads []Thread
		reviews []Review
		cursor  string
	)
	for page := 0; ; page++ {
		args := []string{
			"api", "graphql",
			"-f", "query=" + threadQuery,
			"-F", "owner=" + t.Owner,
			"-F", "repo=" + t.Repo,
			"-F", "number=" + strconv.Itoa(t.Number),
		}
		if cursor != "" {
			args = append(args, "-F", "cursor="+cursor)
		}

		var res queryResult
		if err := ghJSON(&res, args...); err != nil {
			return nil, nil, err
		}
		if err := res.err(); err != nil {
			return nil, nil, err
		}
		pr := res.Data.Repository.PullRequest
		if pr.Number == 0 {
			return nil, nil, fmt.Errorf("no PR #%d in %s", t.Number, t.Slug())
		}

		if page == 0 {
			t.Title, t.URL, t.Branch, t.Author = pr.Title, pr.URL, pr.HeadRefName, pr.Author.Login
			for _, r := range pr.Reviews.Nodes {
				if strings.TrimSpace(r.Body) == "" {
					continue // an approval with no words is not context
				}
				reviews = append(reviews, Review{
					Author: r.Author.Login, State: r.State, Body: r.Body,
					SubmittedAt: r.SubmittedAt, URL: r.URL,
				})
			}
			if pr.Reviews.PageInfo.HasNextPage {
				warn("more than 100 reviews: only the first 100 summary bodies were fetched")
			}
		}

		for _, n := range pr.ReviewThreads.Nodes {
			if len(n.Comments.Nodes) == 0 {
				continue
			}
			if n.Comments.PageInfo.HasNextPage {
				warn(fmt.Sprintf("thread %s: more than 100 comments, only the first 100 were fetched", n.Path))
			}
			first := n.Comments.Nodes[0]
			line := 0
			switch {
			case n.Line != nil:
				line = *n.Line
			case n.StartLine != nil:
				line = *n.StartLine
			}
			th := Thread{
				Meta: Meta{
					ThreadID:  n.ID,
					CommentID: first.DatabaseID,
					URL:       first.URL,
					Path:      n.Path,
					Line:      line,
					Author:    first.Author.Login,
					Resolved:  n.IsResolved,
					Outdated:  n.IsOutdated,
					Status:    StatusPending,
				},
				DiffHunk: first.DiffHunk,
			}
			for _, c := range n.Comments.Nodes {
				th.Comments = append(th.Comments, Comment{
					Author: c.Author.Login, CreatedAt: c.CreatedAt, Body: c.Body,
				})
			}
			threads = append(threads, th)
		}

		if !pr.ReviewThreads.PageInfo.HasNextPage {
			return threads, reviews, nil
		}
		cursor = pr.ReviewThreads.PageInfo.EndCursor
	}
}

// Reply posts body as a threaded reply to a review comment and returns the new
// comment's URL. Replies to replies are rejected by the API, so commentID must be the
// first comment of its thread.
//
// Sequential by design, with a retry that honours Retry-After: this endpoint sends
// notifications and is the documented way to trip GitHub's secondary rate limits.
func Reply(t Target, commentID int64, body string) (string, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/comments/%d/replies", t.Owner, t.Repo, t.Number, commentID)

	var lastErr error
	backoff := 2 * time.Second
	for attempt := range 4 {
		if attempt > 0 {
			wait := backoff
			if ra := retryAfter(lastErr); ra > 0 {
				wait = ra
			}
			time.Sleep(wait)
			backoff *= 4
		}

		out, err := gh("api", "--method", "POST", "-i", path, "-f", "body="+body)
		status, headers, payload := splitResponse(out)
		if err != nil && status == 0 {
			return "", err // gh itself failed: not authenticated, no network, bad args
		}
		if status >= 200 && status < 300 {
			var c struct {
				HTMLURL string `json:"html_url"`
			}
			_ = json.Unmarshal([]byte(payload), &c)
			return c.HTMLURL, nil
		}

		lastErr = &httpError{status: status, headers: headers, body: payload}
		if status != http.StatusForbidden && status != http.StatusTooManyRequests && status < 500 {
			return "", lastErr // 404, 422: retrying will not help
		}
	}
	return "", lastErr
}

type httpError struct {
	status  int
	headers http.Header
	body    string
}

func (e *httpError) Error() string {
	msg := e.body
	var payload struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(e.body), &payload) == nil && payload.Message != "" {
		msg = payload.Message
	}
	return fmt.Sprintf("HTTP %d: %s", e.status, strings.TrimSpace(msg))
}

// retryAfter reads the Retry-After header in either allowed form: a delay in seconds,
// or an HTTP-date.
func retryAfter(err error) time.Duration {
	he, ok := err.(*httpError)
	if !ok {
		return 0
	}
	v := he.headers.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(v); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}

// splitResponse takes `gh api -i` output apart into status, headers and body. A status
// of 0 means the output had no status line, i.e. gh failed before making the request.
func splitResponse(out string) (int, http.Header, string) {
	head, body, _ := strings.Cut(strings.ReplaceAll(out, "\r\n", "\n"), "\n\n")
	lines := strings.Split(head, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "HTTP/") {
		return 0, nil, out
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 2 {
		return 0, nil, out
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, nil, out
	}
	headers := http.Header{}
	for _, l := range lines[1:] {
		if k, v, ok := strings.Cut(l, ":"); ok {
			headers.Add(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
	return status, headers, body
}

func gh(args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("gh", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		// gh -i writes the response to stdout even on a failing status; only fall back
		// to stderr when there is nothing else to report.
		msg := strings.TrimSpace(stderr.String())
		if stdout.Len() == 0 {
			if msg == "" {
				msg = err.Error()
			}
			return "", fmt.Errorf("gh %s: %s", strings.Join(args[:min(2, len(args))], " "), msg)
		}
	}
	return stdout.String(), err
}

func ghJSON(v any, args ...string) error {
	out, err := gh(args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		return fmt.Errorf("decoding gh %s output: %w", strings.Join(args[:min(2, len(args))], " "), err)
	}
	return nil
}
