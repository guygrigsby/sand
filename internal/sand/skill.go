package sand

// The box runs two agent harnesses and neither of them should own a copy of the skill:
// skills/sand-comments/SKILL.md in this repo is the only one, and install points both
// harnesses at it with a symlink. A copy would go stale the first time the file changes
// and nothing would say so.

import (
	"fmt"
	"os"
	"path/filepath"
)

const skillName = "sand-comments"

// harnessSkillDirs are the user-level skill directories of the harnesses on the box,
// relative to $HOME. Claude Code reads only its own (it ignores ~/.agents/skills, which
// pi does read), so each gets its own link.
var harnessSkillDirs = []string{
	filepath.Join(".claude", "skills"),
	filepath.Join(".pi", "agent", "skills"),
}

// FindSkillSource walks up from dir for the checkout's skill directory, so install works
// from anywhere inside the repo.
func FindSkillSource(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		src := filepath.Join(dir, "skills", skillName)
		if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err == nil {
			return src, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no skills/%s/SKILL.md above the current directory: run this inside the sand checkout, or pass --source", skillName)
		}
		dir = parent
	}
}

// InstallSkill links source into each harness skill directory under home and returns the
// links it made. A link that is already there is replaced; anything else in the way is an
// error rather than something to delete, because it is not ours.
func InstallSkill(home, source string) ([]string, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(source, "SKILL.md")); err != nil {
		return nil, fmt.Errorf("%s is not a skill directory: %w", source, err)
	}

	var made []string
	for _, rel := range harnessSkillDirs {
		dir := filepath.Join(home, rel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return made, err
		}
		link := filepath.Join(dir, skillName)
		switch info, err := os.Lstat(link); {
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			if err := os.Remove(link); err != nil {
				return made, err
			}
		case err == nil:
			return made, fmt.Errorf("%s exists and is not a symlink; move it aside first", link)
		case !os.IsNotExist(err):
			return made, err
		}
		if err := os.Symlink(source, link); err != nil {
			return made, err
		}
		made = append(made, link)
	}
	return made, nil
}
