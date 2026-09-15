package sand

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

type issueDraft struct {
	Title string
	Body  []byte
}

func issueDraftPath(base string, target Target) string {
	return path.Join(base, segment(target.Owner), segment(target.Repo), "issue-draft")
}

func runIssueCreate(brief string) error {
	cfg, err := Resolve(flagHost, flagRemoteDir)
	if err != nil {
		return err
	}
	target, err := currentRepo()
	if err != nil {
		return err
	}
	draftDir := issueDraftPath(cfg.RemoteDir, target)
	if brief != "" {
		run, err := agentRun(cfg, target, issuePrompt(target, draftDir, brief))
		if err != nil {
			return err
		}
		if flagDryRun {
			fmt.Printf("dry run: would run the agent in %s:%s to write issue.md\n", cfg.Host, run.Dir)
			fmt.Printf("dry run: would then open an issue in %s\n", target.Slug())
			return nil
		}
		if err := ensureRemoteSkill(cfg, os.Stdout); err != nil {
			return err
		}
		if err := clearIssueDraft(cfg, draftDir); err != nil {
			return err
		}
		fmt.Printf("starting the agent in %s:%s to draft the issue, Ctrl-C to stop it\n\n", cfg.Host, run.Dir)
		if err := RunAgent(run); err != nil {
			return err
		}
	}
	var lock *remoteLock
	if !flagDryRun {
		lock, err = lockRemote(cfg, target.Repo)
		if err != nil {
			return err
		}
		defer lock.Close()
	}
	draft, err := loadIssueDraft(cfg, draftDir)
	if err != nil {
		return err
	}
	if flagDryRun {
		fmt.Printf("dry run: would open %q in %s from %s:%s/issue.md\n", draft.Title, target.Slug(), cfg.Host, draftDir)
		return nil
	}
	if err := lock.check(); err != nil {
		return err
	}
	url, err := createIssue(target, draft)
	if err != nil {
		return err
	}
	fmt.Printf("opened %s\n", url)
	if err := clearIssueDraft(cfg, draftDir); err != nil {
		warn(fmt.Sprintf("issue was created, but its draft could not be removed; do not run `sand issue create` again: %v", err))
	}
	if err := lock.Close(); err != nil {
		warn(fmt.Sprintf("issue was created, but the sandbox lock was lost while consuming its draft: %v", err))
	}
	return nil
}

func clearIssueDraft(cfg Config, draftDir string) error {
	remote := fmt.Sprintf("rm -f -- %s", remoteQuote(path.Join(draftDir, "issue.md")))
	if out, err := exec.Command(sshBin(), cfg.Host, remote).CombinedOutput(); err != nil {
		return fmt.Errorf("clearing the old issue draft on %s: %w: %s", cfg.Host, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func loadIssueDraft(cfg Config, draftDir string) (issueDraft, error) {
	dir, err := fetchDir(cfg.Host, draftDir)
	if err != nil {
		return issueDraft{}, err
	}
	defer os.RemoveAll(dir)

	b, err := os.ReadFile(path.Join(dir, "issue.md"))
	if err != nil {
		return issueDraft{}, fmt.Errorf("reading %s:%s/issue.md: %w", cfg.Host, draftDir, err)
	}
	draft, err := parseIssueDraft(b)
	if err != nil {
		return issueDraft{}, fmt.Errorf("%s:%s/issue.md: %w", cfg.Host, draftDir, err)
	}
	return draft, nil
}

func parseIssueDraft(b []byte) (issueDraft, error) {
	fm, body, err := splitFrontMatter(string(b))
	if err != nil {
		return issueDraft{}, err
	}
	var meta struct {
		Title string `yaml:"title"`
	}
	if err := yaml.Unmarshal([]byte(fm), &meta); err != nil {
		return issueDraft{}, fmt.Errorf("front matter: %w", err)
	}
	meta.Title = strings.TrimSpace(meta.Title)
	if meta.Title == "" || strings.ContainsAny(meta.Title, "\r\n") {
		return issueDraft{}, fmt.Errorf("front matter title must contain one non-empty line")
	}
	// The blank line after YAML front matter separates metadata from Markdown; it is not
	// part of the issue body sent to GitHub. Everything after it remains byte-for-byte.
	body = strings.TrimPrefix(body, "\n")
	if strings.TrimSpace(body) == "" {
		return issueDraft{}, fmt.Errorf("issue body is empty")
	}
	return issueDraft{Title: meta.Title, Body: []byte(body)}, nil
}

func createIssue(target Target, draft issueDraft) (string, error) {
	f, err := os.CreateTemp("", "sand-issue-description-*.md")
	if err != nil {
		return "", err
	}
	body := f.Name()
	defer os.Remove(body)
	if _, err := f.Write(draft.Body); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	out, err := gh("issue", "create", "--repo", target.Slug(), "--title", draft.Title, "--body-file", body)
	if err != nil {
		return "", err
	}
	url := strings.TrimSpace(out)
	if url == "" {
		return "", fmt.Errorf("gh issue create returned no issue URL")
	}
	return url, nil
}
