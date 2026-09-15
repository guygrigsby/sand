package sand

// A box-authored review is deliberately separate from pulled review threads. These files
// create new comments in one pending review; thread files answer comments that already exist.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type reviewDraft struct {
	Branch   string
	Commit   string
	Base     string
	Comments []reviewComment
	files    []string
}

type reviewComment struct {
	Path string `yaml:"path" json:"path"`
	Line int    `yaml:"line" json:"line"`
	Side string `yaml:"side" json:"side"`
	Body string `yaml:"-" json:"body"`
}

type reviewDraftMeta struct {
	Branch string `yaml:"branch"`
	Commit string `yaml:"commit"`
	Base   string `yaml:"base,omitempty"`
}

var fullCommit = regexp.MustCompile(`\A[0-9a-fA-F]{40}\z`)

func reviewDraftPath(base string, t Target) string {
	parts := []string{base, segment(t.Owner), segment(t.Repo), "reviews"}
	for _, part := range strings.Split(t.Branch, "/") {
		parts = append(parts, segment(part))
	}
	return path.Join(parts...)
}

func runPRReview(args []string, out io.Writer) error {
	cfg, target, err := setup(args)
	if err != nil {
		return err
	}
	if err := target.LoadURL(); err != nil {
		return err
	}
	if target.Branch == "" || target.HeadSHA == "" {
		return fmt.Errorf("gh did not report the branch and head commit for %s#%d", target.Slug(), target.Number)
	}

	draftPath := reviewDraftPath(cfg.RemoteDir, target)
	var lock *remoteLock
	if !flagDryRun {
		lock, err = lockRemote(cfg, target.Repo)
		if err != nil {
			return err
		}
		defer lock.Close()
	}
	draft, err := loadReviewDraft(cfg, draftPath)
	if err != nil {
		return err
	}
	if draft.Branch != target.Branch {
		return fmt.Errorf("review draft says branch %q, but %s#%d is branch %q", draft.Branch, target.Slug(), target.Number, target.Branch)
	}
	if !strings.EqualFold(draft.Commit, target.HeadSHA) {
		return fmt.Errorf("review draft is for %s, but GitHub's %s head is %s; update the box branch and have the agent review it again", short(draft.Commit), target.Branch, short(target.HeadSHA))
	}
	if len(draft.Comments) == 0 {
		return fmt.Errorf("review draft at %s:%s has no comment files", cfg.Host, draftPath)
	}

	fmt.Fprintf(out, "%s#%d %q: %d pending review comment(s) from %s:%s\n", target.Slug(), target.Number, target.Title, len(draft.Comments), cfg.Host, draftPath)
	for _, c := range draft.Comments {
		fmt.Fprintf(out, "  %s:%d %-5s %s\n", c.Path, c.Line, c.Side, firstLine(c.Body, 100))
	}
	if flagDryRun {
		fmt.Fprintln(out, "dry run: no GitHub review was created and the sandbox draft was kept")
		return nil
	}
	if err := lock.check(); err != nil {
		return err
	}
	url, err := CreatePendingReview(target, draft.Comments)
	if err != nil {
		return fmt.Errorf("creating the pending review: %w", err)
	}
	fmt.Fprintf(out, "started a pending review with %d comment(s): %s\n", len(draft.Comments), url)
	fmt.Fprintln(out, "review and submit it from the PR's Files changed tab")
	if err := clearReviewDraft(cfg, draftPath, draft.files); err != nil {
		warn(fmt.Sprintf("the pending review exists, but its sandbox draft could not be consumed; do not run `sand pr review` again: %v", err))
	}
	if err := lock.Close(); err != nil {
		warn(fmt.Sprintf("the pending review exists, but the sandbox lock was lost while consuming its draft: %v", err))
	}
	return nil
}

func loadReviewDraft(cfg Config, remotePath string) (reviewDraft, error) {
	dir, err := fetchDir(cfg.Host, remotePath)
	if err != nil {
		return reviewDraft{}, err
	}
	defer os.RemoveAll(dir)

	manifest := filepath.Join(dir, "review.md")
	raw, err := os.ReadFile(manifest)
	if err != nil {
		return reviewDraft{}, fmt.Errorf("reading %s:%s/review.md: %w", cfg.Host, remotePath, err)
	}
	fm, _, err := splitFrontMatter(string(raw))
	if err != nil {
		return reviewDraft{}, fmt.Errorf("%s:%s/review.md: %w", cfg.Host, remotePath, err)
	}
	var meta reviewDraftMeta
	if err := strictYAML(fm, &meta); err != nil {
		return reviewDraft{}, fmt.Errorf("%s:%s/review.md front matter: %w", cfg.Host, remotePath, err)
	}
	meta.Branch, meta.Commit, meta.Base = strings.TrimSpace(meta.Branch), strings.TrimSpace(meta.Commit), strings.TrimSpace(meta.Base)
	if meta.Branch == "" {
		return reviewDraft{}, fmt.Errorf("%s:%s/review.md: branch is empty", cfg.Host, remotePath)
	}
	if !fullCommit.MatchString(meta.Commit) {
		return reviewDraft{}, fmt.Errorf("%s:%s/review.md: commit must be a full 40-character Git commit", cfg.Host, remotePath)
	}

	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return reviewDraft{}, err
	}
	sort.Strings(paths)
	draft := reviewDraft{Branch: meta.Branch, Commit: strings.ToLower(meta.Commit), Base: meta.Base, files: []string{"review.md"}}
	for _, file := range paths {
		if filepath.Base(file) == "review.md" {
			continue
		}
		comment, err := parseReviewComment(file)
		if err != nil {
			return reviewDraft{}, fmt.Errorf("%s:%s/%s: %w", cfg.Host, remotePath, filepath.Base(file), err)
		}
		draft.Comments = append(draft.Comments, comment)
		draft.files = append(draft.files, filepath.Base(file))
	}
	return draft, nil
}

func parseReviewComment(file string) (reviewComment, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return reviewComment{}, err
	}
	fm, body, err := splitFrontMatter(string(raw))
	if err != nil {
		return reviewComment{}, err
	}
	var comment reviewComment
	if err := strictYAML(fm, &comment); err != nil {
		return reviewComment{}, fmt.Errorf("front matter: %w", err)
	}
	comment.Path = strings.TrimSpace(strings.ReplaceAll(comment.Path, "\\", "/"))
	comment.Side = strings.ToUpper(strings.TrimSpace(comment.Side))
	comment.Body = strings.TrimPrefix(body, "\n")
	if comment.Path == "" || comment.Path == "." || path.IsAbs(comment.Path) || path.Clean(comment.Path) != comment.Path || strings.HasPrefix(comment.Path, "../") {
		return reviewComment{}, fmt.Errorf("path must be a clean repository-relative file path")
	}
	if comment.Line < 1 {
		return reviewComment{}, fmt.Errorf("line must be greater than zero")
	}
	if comment.Side != "RIGHT" && comment.Side != "LEFT" {
		return reviewComment{}, fmt.Errorf("side must be RIGHT or LEFT")
	}
	if strings.TrimSpace(comment.Body) == "" {
		return reviewComment{}, fmt.Errorf("comment body is empty")
	}
	return comment, nil
}

func strictYAML(fm string, v any) error {
	dec := yaml.NewDecoder(strings.NewReader(fm))
	dec.KnownFields(true)
	return dec.Decode(v)
}

func clearReviewDraft(cfg Config, remotePath string, files []string) error {
	args := make([]string, 0, len(files))
	for _, name := range files {
		if filepath.Base(name) != name || filepath.Ext(name) != ".md" {
			return fmt.Errorf("refusing to remove unexpected draft name %q", name)
		}
		args = append(args, remoteQuote(path.Join(remotePath, name)))
	}
	remote := "rm -f -- " + strings.Join(args, " ")
	if output, err := exec.Command(sshBin(), cfg.Host, remote).CombinedOutput(); err != nil {
		return fmt.Errorf("clearing the review draft on %s: %w: %s", cfg.Host, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// CreatePendingReview creates the review and its inline comments atomically. Omitting event is
// intentional: GitHub leaves the review PENDING until the user submits it in the UI.
func CreatePendingReview(t Target, comments []reviewComment) (string, error) {
	payload, err := json.Marshal(struct {
		CommitID string          `json:"commit_id"`
		Comments []reviewComment `json:"comments"`
	}{CommitID: t.HeadSHA, Comments: comments})
	if err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("repos/%s/pulls/%d/reviews", t.Slug(), t.Number)
	out, runErr := ghInput(payload, "api", "--method", "POST", "-i", endpoint, "--input", "-")
	status, headers, body := splitResponse(out)
	if runErr != nil && status == 0 {
		return "", runErr
	}
	if status < 200 || status >= 300 {
		return "", &httpError{status: status, headers: headers, body: body}
	}
	var response struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		// A 2xx means the review exists. Treating a cosmetic response decode as a failed
		// creation would preserve the files and invite a duplicate attempt.
		return t.URL, nil
	}
	if response.HTMLURL == "" {
		return t.URL, nil
	}
	return response.HTMLURL, nil
}
