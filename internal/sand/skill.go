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
	"strings"
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

// A harness is one agent CLI on the box: where its skill goes, and how to run it headless.
// One table, because "which harnesses does this tool know about" is one question — the skill
// install and the agent `comments pull` starts have to agree on the answer.
//
// marker is the directory that says the harness is installed here at all; link is the path
// its loader reads, which differs per harness: pi discovers top-level .md files in its skills
// dir, Claude Code only reads <name>/SKILL.md. Neither discovers a top-level .md in
// ~/.agents/skills, so both get a link.
type agentHarness struct {
	Name      string   // the `harness` config value, and the name install prints
	marker    string   // ~/<marker> exists when this harness is installed
	link      string   // where its loader reads the skill
	run       []string // headless invocation; the prompt is appended as one argument
	modelFlag string   // the flag it takes a model with
}

var harnesses = []agentHarness{
	{
		Name: "pi", marker: ".pi", link: filepath.Join(".pi", "agent", "skills", skillFile),
		run: []string{"pi", "--print"}, modelFlag: "--model",
	},
	{
		Name: "claude", marker: ".claude", link: filepath.Join(".claude", "skills", skillName, "SKILL.md"),
		// stream-json is what lets the held-open tunnel report progress rather than sit
		// silent for twenty minutes. bypassPermissions because an unattended headless run
		// denies every tool it would otherwise ask about — including the edits and the
		// `make check` it is being asked to do — and a box with no keys, no token and no
		// route to GitHub is the case that mode is documented for.
		run: []string{"claude", "--print", "--verbose", "--output-format", "stream-json",
			"--permission-mode", "bypassPermissions"},
		modelFlag: "--model",
	},
}

// harnessNames is every harness this tool knows, in table order, for error messages and the
// `harness` comment in the config file. Reading them off the table is the point: a second
// hand-written list is a list that ends up naming a set that is no longer the set.
func harnessNames() []string {
	names := make([]string, 0, len(harnesses))
	for _, h := range harnesses {
		names = append(names, h.Name)
	}
	return names
}

// findHarness resolves the `harness` config value.
func findHarness(name string) (agentHarness, error) {
	for _, h := range harnesses {
		if h.Name == name {
			return h, nil
		}
	}
	return agentHarness{}, fmt.Errorf("unknown harness %q; known: %s (`sand config set harness <name>`)",
		name, strings.Join(harnessNames(), ", "))
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
