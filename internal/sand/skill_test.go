package sand

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The embedded skill has to satisfy both harnesses' front matter rules, or it loads as
// nothing and the box gets no instructions at all with no error anywhere.
func TestEmbeddedSkillFrontMatter(t *testing.T) {
	m := frontMatter.FindSubmatch(skillDoc)
	if m == nil {
		t.Fatal("skill.md has no YAML front matter")
	}
	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal(m[1], &fm); err != nil {
		t.Fatal(err)
	}
	if fm.Name != skillName {
		t.Errorf("front matter name is %q, want %q (Claude Code requires it to match the directory)", fm.Name, skillName)
	}
	if fm.Description == "" {
		t.Error("front matter has no description, so pi will not discover it")
	}
}

// installed reads the skill back the way a harness would: through whatever the loader finds
// at that path.
func installed(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return string(b)
}

func TestInstallSkillLinksHarnessesPresent(t *testing.T) {
	home := t.TempDir()
	// Only pi is installed here; the Claude Code directory is deliberately missing.
	if err := os.MkdirAll(filepath.Join(home, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := InstallSkill(home)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Updated {
		t.Error("first install reported no write")
	}
	if want := filepath.Join(home, ".agents", "skills", "sand.md"); got.Path != want {
		t.Errorf("canonical path %s, want %s", got.Path, want)
	}
	if body := installed(t, got.Path); body != string(skillDoc) {
		t.Error("canonical file is not the embedded skill")
	}

	if len(got.Links) != 1 || !strings.HasSuffix(got.Links[0], filepath.Join(".pi", "agent", "skills", "sand.md")) {
		t.Fatalf("links %v, want just the pi one", got.Links)
	}
	if body := installed(t, got.Links[0]); body != string(skillDoc) {
		t.Error("pi's link does not read the skill")
	}
	if len(got.Absent) != 1 || got.Absent[0].Name != "claude" {
		t.Errorf("absent %v, want claude", got.Absent)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Error("install created a directory for a harness that is not installed")
	}
}

func TestInstallSkillIsRepeatable(t *testing.T) {
	home := t.TempDir()
	for _, h := range harnesses {
		if err := os.MkdirAll(filepath.Join(home, h.marker), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	first, err := InstallSkill(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Links) != len(harnesses) {
		t.Fatalf("linked %d harnesses, want %d", len(first.Links), len(harnesses))
	}

	second, err := InstallSkill(home)
	if err != nil {
		t.Fatal(err)
	}
	if second.Updated {
		t.Error("second install rewrote an identical file")
	}
	for _, link := range second.Links {
		if body := installed(t, link); body != string(skillDoc) {
			t.Errorf("%s does not read the skill after a re-install", link)
		}
	}

	// An upgraded binary carrying different text has to win: that is the update path.
	if err := os.WriteFile(first.Path, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := InstallSkill(home)
	if err != nil {
		t.Fatal(err)
	}
	if !third.Updated {
		t.Error("install left a stale skill in place")
	}
	if body := installed(t, third.Links[0]); body != string(skillDoc) {
		t.Error("link still reads the stale skill")
	}
}

func TestInstallSkillKeepsSomeoneElsesFile(t *testing.T) {
	home := t.TempDir()
	claude := harnesses[1]
	link := filepath.Join(home, claude.link)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("hand written skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := InstallSkill(home); err == nil {
		t.Fatal("no error with a real file in the way")
	}
	if body := installed(t, link); body != "hand written skill\n" {
		t.Fatalf("clobbered a real file: %q", body)
	}
}
