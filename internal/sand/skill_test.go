package sand

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoWithSkill fakes a checkout: <root>/skills/sand-comments/SKILL.md plus a subdirectory
// to search up from.
func repoWithSkill(t *testing.T) (root, source string) {
	t.Helper()
	root = t.TempDir()
	source = filepath.Join(root, "skills", skillName)
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: x\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, source
}

func TestFindSkillSourceWalksUp(t *testing.T) {
	root, source := repoWithSkill(t)
	deep := filepath.Join(root, "internal", "sand")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := FindSkillSource(deep)
	if err != nil {
		t.Fatal(err)
	}
	// The temp dir may be a symlink (/tmp on darwin), so compare resolved paths.
	if want := resolve(t, source); resolve(t, got) != want {
		t.Fatalf("found %s, want %s", got, want)
	}

	if _, err := FindSkillSource(t.TempDir()); err == nil {
		t.Fatal("no error outside a checkout")
	}
}

func TestInstallSkillLinksBothHarnesses(t *testing.T) {
	_, source := repoWithSkill(t)
	home := t.TempDir()

	links, err := InstallSkill(home, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != len(harnessSkillDirs) {
		t.Fatalf("made %d links, want %d", len(links), len(harnessSkillDirs))
	}
	for _, link := range links {
		// The harness reads SKILL.md through the link, which is the point of linking.
		b, err := os.ReadFile(filepath.Join(link, "SKILL.md"))
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		if !strings.HasPrefix(string(b), "---") {
			t.Fatalf("%s: read %q through the link", link, b)
		}
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s is not a symlink (%v)", link, err)
		}
	}

	// Re-running is how an agent recovers a link that points at an old checkout.
	if _, err := InstallSkill(home, source); err != nil {
		t.Fatalf("second install: %v", err)
	}

	// A real directory in the way belongs to someone else.
	occupied := filepath.Join(home, harnessSkillDirs[0], skillName)
	if err := os.RemoveAll(occupied); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(occupied, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallSkill(home, source); err == nil {
		t.Fatal("clobbered a real directory")
	}
}

func TestInstallSkillRejectsNonSkillSource(t *testing.T) {
	if _, err := InstallSkill(t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("no error for a source without SKILL.md")
	}
}

func resolve(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
