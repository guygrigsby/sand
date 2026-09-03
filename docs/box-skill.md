# The skill for the box side

`internal/sand/skill.md` teaches an agent on the box how to answer the pulled threads: where the
files are, that only `## reply` and `commit:` are its to edit, and that push happens on the Mac.
It is `go:embed`ed into the binary, so a box holding nothing but `sand` can still install it, and
so the skill can never be a different version from the tool it describes. That is why it lives in
the package and not in a `skills/` directory.

`sand skill install` writes it to `~/.agents/skills/sand.md` and links the harnesses that are
actually installed on that machine at that one file. The program owns this, not the Makefile:
the binary is what carries the text, and the machine that needs the skill may have no checkout
and no `make`.

Install prints the version it wrote from, because "the skill disagrees with the tool" is only
diagnosable if both ends can be named. The other end is `sand --version`.

## `--remote`: the box, which has no binary

The machine that needs the skill is the one machine this tool is never run on. `sand skill
install --remote` is the answer: the Mac pipes the embedded text over ssh into a script that
makes the same three decisions there (write the canonical file, link a harness that is present,
refuse to overwrite anything that is not our own symlink), because the box has a shell and no
`sand`. `InstallSkillRemote` and `remoteSkillScript` in `skill.go`, generated from the same
`harnesses` table as the local install, so the two ends cannot come to know different harnesses.

It is not a command anyone should have to remember. `sand new`, `sand comments pull` and `sand
ci pull` each run it before they hand the box work, and that is how the box gets the skill at
all now. Three things fall out of doing it there rather than expecting the box to keep itself
current:

- **The version question stops existing.** The binary that writes the agent's prompt writes the
  skill, in the same command, so they cannot be different releases. That was the reason for
  printing versions at both ends, and on the box there is now no other end to print.
- **The box needs nothing installed.** No `sand`, no release download, no checkout, no
  `install.sh`. `install.sh` still installs the skill when it finds a harness, which is right
  for a machine that has the binary for some other reason, but the box is not that machine.
- **A pull cannot start an agent with stale instructions**, which is the failure this was
  actually losing to: the box quietly running whatever skill someone downloaded in June while
  the tool had moved on, with nothing anywhere saying so.

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

| harness | present when | link | headless run |
|---|---|---|---|
| pi | `~/.pi` exists | `~/.pi/agent/skills/sand.md` | `pi --print`, `--model` |
| claude | `~/.claude` exists | `~/.claude/skills/sand/SKILL.md` | `claude --print --verbose --output-format stream-json --permission-mode bypassPermissions`, `--model` |

One table (`harnesses` in `skill.go`) answers both "which harnesses does this tool know" and
"how do I start one", because they are the same question and two lists would disagree. The name
is the `harness` config value as well as what `skill install` prints. `bypassPermissions` is
there because an unattended headless run denies every tool it would otherwise ask about,
including the edits and the `make check` it is being told to do.

Neither harness discovers a top-level `.md` in `~/.agents/skills/` (pi ignores root files there
and reads nested ones; Claude Code does not look there at all), so the canonical path needs the
links rather than replacing them. A harness that is not installed is reported and skipped, not an
error: the same binary runs on the Mac. `sand skill show` prints what the binary carries.

Editing an installed copy is pointless, the next install overwrites it. Edit `skill.md` here.
A running harness only picks the change up on its next start.

**Every change to this repo updates `skill.md` in the same commit.** The skill is the only thing
telling an agent on the box how the loop works, and that agent cannot read the source of a tool it
never runs. If a change moves a path, renames a field, changes what `push` reads, or adds a
command, the skill says so before the change lands. If a change genuinely does not touch the box
side, say that in the commit message rather than leaving it unsaid.
