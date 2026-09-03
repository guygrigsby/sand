package sand

import (
	"bytes"
	"os"
	"os/exec"
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

// fakeBox points the transport at a local shell and gives it a $HOME of its own, so the
// remote install runs its real script against a real filesystem. That is the whole point of
// testing this one: the decisions are in shell there, not in Go here.
func fakeBox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	shim := filepath.Join(t.TempDir(), "ssh-shim")
	if err := os.WriteFile(shim, []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAND_SSH", shim)
	t.Setenv("HOME", home)
	return home
}

// The box has no sand to install the skill with, so the Mac has to do it over ssh, and the
// shell doing the work there has to reach the same three answers InstallSkill reaches here.
func TestInstallSkillRemoteWritesAndLinks(t *testing.T) {
	home := fakeBox(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := InstallSkillRemote("box")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, canonicalSkillPath); got.Path != want {
		t.Errorf("path %s, want %s", got.Path, want)
	}
	if !got.Updated || !got.Changed() {
		t.Error("first install reported no change")
	}
	if body := installed(t, got.Path); body != string(skillDoc) {
		t.Error("the box did not get the embedded skill")
	}
	if len(got.Linked) != 1 || !strings.HasSuffix(got.Linked[0], filepath.Join(".claude", "skills", "sand", "SKILL.md")) {
		t.Fatalf("linked %v, want the claude one", got.Linked)
	}
	if body := installed(t, got.Linked[0]); body != string(skillDoc) {
		t.Error("claude's link does not read the skill")
	}
	if len(got.Absent) != 1 || got.Absent[0] != "pi" {
		t.Errorf("absent %v, want pi", got.Absent)
	}

	// Re-run: quiet, because a pull does this every time and only a change is worth a line.
	again, err := InstallSkillRemote("box")
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed() || again.Updated || len(again.Linked) != 0 {
		t.Errorf("a second install changed something: %+v", again)
	}
	if len(again.Current) != 1 {
		t.Errorf("current links %v, want the one already there", again.Current)
	}

	// An upgraded Mac carrying different text wins, which is the only update path the box has.
	if err := os.WriteFile(got.Path, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := InstallSkillRemote("box")
	if err != nil {
		t.Fatal(err)
	}
	if !third.Updated {
		t.Error("install left a stale skill on the box")
	}
	if body := installed(t, third.Current[0]); body != string(skillDoc) {
		t.Error("the link still reads the stale skill")
	}
}

// Half a skill is worse than none: it installs clean, loads clean, and is missing whichever
// rule came after the cut. A connection that drops mid-copy reaches `cat` as an ordinary end of
// input, so the count is the only thing that can tell the two apart.
func TestRemoteSkillScriptRefusesAShortStream(t *testing.T) {
	home := fakeBox(t)
	if _, err := InstallSkillRemote("box"); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", "-c", remoteSkillScript())
	cmd.Stdin = bytes.NewReader(skillDoc[:len(skillDoc)/2])
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("a truncated skill installed cleanly:\n%s", out)
	}
	if !strings.Contains(string(out), "bytes of") {
		t.Errorf("output does not say the stream was short:\n%s", out)
	}
	if body := installed(t, filepath.Join(home, canonicalSkillPath)); body != string(skillDoc) {
		t.Error("a short stream replaced the installed skill")
	}
}

func TestInstallSkillRemoteKeepsSomeoneElsesFile(t *testing.T) {
	home := fakeBox(t)
	link := filepath.Join(home, harnesses[0].link)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("hand written skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := InstallSkillRemote("box"); err == nil {
		t.Fatal("no error with a real file in the way on the box")
	} else if !strings.Contains(err.Error(), "not a symlink") {
		t.Errorf("error does not say what is in the way: %v", err)
	}
	if body := installed(t, link); body != "hand written skill\n" {
		t.Fatalf("clobbered a real file on the box: %q", body)
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
