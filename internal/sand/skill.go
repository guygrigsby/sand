package sand

// The skill is the box side of the tool: the only thing telling an agent there how the
// comment files work. It ships inside the binary, and install writes one copy to a
// harness-neutral path and links the harnesses that are actually present at it. Links, not
// copies, so a re-install updates every harness at once.
//
// It installs two ways, because the machine that needs the skill is the one machine this tool
// is never run on. Locally, when there is a binary on that machine to run. Over ssh from the
// Mac, when there is not: same three decisions, made by the box's shell instead of this code,
// so a box with no `sand`, no checkout and no release download still gets the skill. The
// remote way is the one the loop uses, and every `pull` does it before starting an agent, which
// also settles the version question: the binary writing the prompt is the binary that wrote the
// skill, in the same command.

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
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

// RemoteSkill is what an install on the box did, reported back a line at a time by the script
// that did it. Same shape as SkillInstall, with the paths as the box named them: they are its
// $HOME, not this one's, and Absent is names because the harness table is over here.
type RemoteSkill struct {
	Host    string
	Path    string
	Updated bool     // the skill text there was not already this text
	Linked  []string // links that had to be created or repointed
	Current []string // links already pointing at the skill, so untouched
	Absent  []string // harnesses not installed on the box, so not linked
}

// Changed is whether the box came away different, which is what decides whether a pull says
// anything about the skill at all. Every run after the first changes nothing.
func (r RemoteSkill) Changed() bool { return r.Updated || len(r.Linked) > 0 }

// InstallSkillRemote installs the skill on the box over ssh: the Mac carries the text, so the
// box needs no `sand` of its own to hold the only file that tells its agent how any of this
// works. The skill goes over stdin rather than inside the command line, because it is a
// document and a remote command line is a shell's to re-parse.
func InstallSkillRemote(host string) (RemoteSkill, error) {
	got := RemoteSkill{Host: host}
	cmd := exec.Command(sshBin(), host, remoteSkillScript())
	cmd.Stdin = bytes.NewReader(skillDoc)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		return got, fmt.Errorf("installing the skill on %s: %w (%s)", host, err, strings.TrimSpace(errs.String()))
	}

	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		// Verb first and the path last, unsplit: a $HOME with a space in it is not this
		// tool's problem to have, but it is not worth a parse that mangles it either.
		verb, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch verb {
		case "written":
			got.Path, got.Updated = rest, true
		case "unchanged":
			got.Path = rest
		case "linked":
			got.Linked = append(got.Linked, rest)
		case "current":
			got.Current = append(got.Current, rest)
		case "absent":
			got.Absent = append(got.Absent, rest)
		}
	}
	if got.Path == "" {
		return got, fmt.Errorf("%s did not say where it put the skill: %q", host, strings.TrimSpace(out.String()))
	}
	return got, nil
}

// remoteSkillScript is InstallSkill written for the box's shell, generated from the same
// harness table so the two ends cannot come to know different harnesses. It reads the skill
// on stdin and reports what it did, one line per decision, verb first.
//
// $HOME rather than ~, because this is a command string a shell is handed, and the tilde only
// expands unquoted, which is the one thing a path in a shell must not be. Everything else is
// POSIX sh: the box's login shell is not this tool's to assume.
func remoteSkillScript() string {
	var b strings.Builder
	fmt.Fprintf(&b, "set -eu; c=\"$HOME\"/%s; mkdir -p \"$(dirname \"$c\")\"; ", shellQuote(canonicalSkillPath))
	// Land it through a sibling: an unchanged install then never touches the file a harness may
	// be reading, and a short read never becomes the installed skill. The byte count is the
	// check that the stream arrived whole, because a connection that drops mid-copy reaches
	// `cat` as an ordinary end of input, and half a skill is worse than none: it would install
	// clean, load clean, and be missing whichever rule came after the cut.
	// A box with no `cmp` fails the comparison and rewrites, which is the safe way to be wrong.
	fmt.Fprintf(&b, `cat > "$c.new"; `+
		// tr, because BSD wc pads its count with spaces and the Mac runs this script too, in
		// the test that is the only place either end of it is checked.
		`n=$(wc -c < "$c.new" | tr -d ' '); `+
		`[ "$n" -eq %d ] || { rm -f "$c.new"; echo "the skill arrived as $n bytes of %d" >&2; exit 1; }; `+
		`if cmp -s "$c.new" "$c" 2>/dev/null; then rm -f "$c.new"; echo "unchanged $c"; `+
		`else mv "$c.new" "$c"; echo "written $c"; fi; `,
		len(skillDoc), len(skillDoc))
	for _, h := range harnesses {
		fmt.Fprintf(&b, `if [ -d "$HOME"/%s ]; then l="$HOME"/%s; mkdir -p "$(dirname "$l")"; `+
			// Same refusal as linkSkill: a real file there is somebody's own skill.
			`if [ -e "$l" ] && [ ! -L "$l" ]; then echo "$l exists and is not a symlink; move it aside first" >&2; exit 1; fi; `+
			`if [ "$(readlink "$l" 2>/dev/null)" = "$c" ]; then echo "current $l"; else ln -sfn "$c" "$l"; echo "linked $l"; fi; `+
			`else echo "absent %s"; fi; `,
			shellQuote(h.marker), shellQuote(h.link), h.Name)
	}
	return b.String()
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
