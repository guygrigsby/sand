package sand

// The skill is the box side of the tool: the only thing telling an agent there how the
// comment files work. It ships inside the binary, because the box that needs it may hold
// nothing but the binary, and install writes one copy to a harness-neutral path and links
// the harnesses that are actually present at it. Links, not copies, so a re-install
// updates every harness at once.

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed skill.md
var skillDoc []byte

const (
	skillName = "sand"
	skillFile = skillName + ".md"
)

// canonicalSkillPath is where the skill itself lives, relative to $HOME. ~/.agents/skills
// belongs to no single harness, which is the reason to keep it there.
var canonicalSkillPath = filepath.Join(".agents", "skills", skillFile)

// A harness is one agent CLI on the box. marker is the directory that says it is installed
// here at all; link is the path its loader reads, which differs per harness: pi discovers
// top-level .md files in its skills dir, Claude Code only reads <name>/SKILL.md. Neither
// discovers a top-level .md in ~/.agents/skills, so both get a link.
type agentHarness struct {
	Name   string
	marker string
	link   string
}

var harnesses = []agentHarness{
	{"pi", ".pi", filepath.Join(".pi", "agent", "skills", skillFile)},
	{"claude code", ".claude", filepath.Join(".claude", "skills", skillName, "SKILL.md")},
}

// SkillInstall is what an install did, so the caller can print it and a test can assert it.
type SkillInstall struct {
	Path    string         // the canonical file
	Updated bool           // false when it was already byte-identical
	Links   []string       // links created or repointed
	Absent  []agentHarness // harnesses not installed on this machine, so not linked
}

// InstallSkill writes the embedded skill under home and links every harness present at it.
// A harness that is not installed is reported, not an error: this same binary runs on the
// Mac, where nothing may read skills at all.
func InstallSkill(home string) (SkillInstall, error) {
	out := SkillInstall{Path: filepath.Join(home, canonicalSkillPath)}

	if err := os.MkdirAll(filepath.Dir(out.Path), 0o755); err != nil {
		return out, err
	}
	old, err := os.ReadFile(out.Path)
	if err != nil && !os.IsNotExist(err) {
		return out, err
	}
	if !bytes.Equal(old, skillDoc) {
		if err := os.WriteFile(out.Path, skillDoc, 0o644); err != nil {
			return out, err
		}
		out.Updated = true
	}

	for _, h := range harnesses {
		if _, err := os.Stat(filepath.Join(home, h.marker)); err != nil {
			if !os.IsNotExist(err) {
				return out, err
			}
			out.Absent = append(out.Absent, h)
			continue
		}
		link := filepath.Join(home, h.link)
		if err := linkSkill(out.Path, link); err != nil {
			return out, fmt.Errorf("%s: %w", h.Name, err)
		}
		out.Links = append(out.Links, link)
	}
	return out, nil
}

// linkSkill points link at target, replacing a link that is already there. Anything else in
// the way is an error: a real file there is somebody's own skill, not ours to delete.
func linkSkill(target, link string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	switch info, err := os.Lstat(link); {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		if err := os.Remove(link); err != nil {
			return err
		}
	case err == nil:
		return fmt.Errorf("%s exists and is not a symlink; move it aside first", link)
	case !os.IsNotExist(err):
		return err
	}
	return os.Symlink(target, link)
}
