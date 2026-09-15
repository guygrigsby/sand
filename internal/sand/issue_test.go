package sand

import (
	"strings"
	"testing"
)

func TestParseIssueDraft(t *testing.T) {
	in := []byte("---\ntitle: 'Sand: create issues'\n---\n\nKeep this Markdown byte-for-byte.\n\n```sh\nsand issue create brief\n```\n")
	got, err := parseIssueDraft(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Sand: create issues" {
		t.Errorf("title = %q", got.Title)
	}
	wantBody := "Keep this Markdown byte-for-byte.\n\n```sh\nsand issue create brief\n```\n"
	if string(got.Body) != wantBody {
		t.Errorf("body changed\nwant:\n%s\ngot:\n%s", wantBody, got.Body)
	}
}

func TestParseIssueDraftRejectsInvalidFiles(t *testing.T) {
	for name, in := range map[string]string{
		"no front matter": "Title\n\nBody\n",
		"invalid YAML":    "---\n: : :\n---\n\nBody\n",
		"missing title":   "---\nauthor: sand\n---\n\nBody\n",
		"multiline title": "---\ntitle: |\n  first\n  second\n---\n\nBody\n",
		"empty body":      "---\ntitle: A title\n---\n\n  \n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseIssueDraft([]byte(in)); err == nil {
				t.Fatal("accepted invalid issue.md")
			}
		})
	}
}

func TestIssuePromptDoesNotAssumeImplementedWorkOrCurrentBranch(t *testing.T) {
	prompt := issuePrompt(Target{Owner: "o", Repo: "r"}, "~/.sand/o/r/issue-draft", "report a problem")
	for _, want := range []string{"No fix needs to exist", "current branch is not part of the issue", "code and documentation only when it helps"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestSkillTeachesAnExistingAgentToDraftAnIssue(t *testing.T) {
	for _, want := range []string{
		"asks you to write or draft a new issue",
		"<remote_dir>/<owner>/<repo>/issue-draft/issue.md",
		"title: A concise issue title",
		"sand issue create` on the Mac will publish it",
	} {
		if !strings.Contains(string(skillDoc), want) {
			t.Errorf("sand skill does not contain %q", want)
		}
	}
}
