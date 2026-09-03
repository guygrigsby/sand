# The skill for the box side

`internal/sand/skill.md` teaches an agent on the box how to answer the pulled threads: where the
files are, that only `## reply` and `commit:` are its to edit, and that push happens on the Mac.
It is `go:embed`ed into the binary, which is why it lives in the package and not in a `skills/`
directory: the text travels with the tool, so it can never be a different version from the tool
it describes.

## How it gets to the box

Over ssh, out of the Mac's binary. The box runs no `sand`, so `sand new`, `sand comments pull`
and `sand ci pull` each install the skill there before they hand the box any work, `sand init`
does it while setting the machine up (and reports it, since a box with no harness is a gap it
names), and `sand skill install --remote` does the same on demand, before ssh'ing in to work by
hand. The Mac pipes the embedded text into a script that makes three decisions there:
write the canonical file, link a harness that is present, refuse to overwrite anything that is
not our own symlink. `InstallSkillRemote` and `remoteSkillScript` in `skill.go`, generated from
the same `harnesses` table as everything else, so the two ends cannot come to know different
harnesses.

The sending end is also the end that writes the agent's prompt, which is the point:

- **The version question stops existing.** One binary writes both, in the same command, so they
  cannot be different releases and there is nothing to compare.
- **The box needs nothing installed**, and nothing kept current: no `sand`, no release download,
  no checkout, no `make`. A shell and a harness is the whole requirement.
- **A pull cannot start an agent with stale instructions**, which is what this was losing to
  when the box carried its own copy: an agent working a review out of whatever skill someone
  downloaded in June, with nothing anywhere saying so.

The text lands through a sibling file, checked against the byte count the Mac sent before it
replaces anything. A connection that drops mid-copy reaches `cat` on the box as an ordinary end
of input, so without the count half a skill installs clean, loads clean, and is missing whichever
rule came after the cut, which is a worse failure than no skill at all.

The install is quiet when it changes nothing, which is every run after the first: it compares
before writing, and reports a link only when it had to create or repoint one. A pull that prints
a skill line is telling you something moved. It fails the pull when it fails, because the
prompt's first sentence is "use the sand skill" and an agent that cannot read it edits a
checkout without knowing that pushing from there breaks the ring. When no harness on the box has
a skills directory the file is written anyway and a warning says nothing will load it.

## The harnesses

| harness | present when | link | headless run |
|---|---|---|---|
| pi | `~/.pi` exists | `~/.pi/agent/skills/sand.md` | `pi --print`, `--model` |
| claude | `~/.claude` exists | `~/.claude/skills/sand/SKILL.md` | `claude --print --verbose --output-format stream-json --permission-mode bypassPermissions`, `--model` |

One table (`harnesses` in `skill.go`) answers "which harnesses does this tool know", "where does
each read its skill" and "how do I start one", because they are the same question asked three
times and three lists would disagree. Both installs and the agent start read it, and the remote
script is generated from it. The name is the `harness` config value as well as what an install
prints. `bypassPermissions` is there because an unattended headless run denies every tool it
would otherwise ask about, including the edits and the `make check` it is being told to do.

Neither harness discovers a top-level `.md` in `~/.agents/skills/` (pi ignores root files there
and reads nested ones; Claude Code does not look there at all), so the canonical path needs the
links rather than replacing them. A harness that is not installed is reported and skipped rather
than an error: a box may have one of the two, and the Mac may have neither. `sand skill show`
prints what the binary carries.

Editing an installed copy is pointless, the next install overwrites it. Edit `skill.md` here.
A running harness only picks the change up on its next start.

## The local install

`sand skill install`, with no `--remote`, does the same three things on the machine it runs on,
in Go rather than in shell (`InstallSkill`). That is for a machine that has the binary for its
own reasons and an agent that could use the skill; the box is not that machine and does not need
to become one. It prints the version it wrote from, because "the skill disagrees with the tool"
is only diagnosable if both ends can be named, and locally the other end is `sand --version`.

The program owns this, not the Makefile: the binary carries the text, and a machine that wants
the skill may have no checkout and no `make`.

## The rule

**Every change to this repo updates `skill.md` in the same commit.** The skill is the only thing
telling an agent on the box how the loop works, and that agent cannot read the source of a tool it
never runs. If a change moves a path, renames a field, changes what `push` reads, or adds a
command, the skill says so before the change lands. If a change genuinely does not touch the box
side, say that in the commit message rather than leaving it unsaid.
