# The skill for the box side

`internal/sand/skill.md` teaches an agent on the box how to answer the pulled threads: where the
files are, that only `## reply` and `commit:` are its to edit, and that push happens on the Mac.
It is `go:embed`ed into the binary, so a box holding nothing but `sand` can still install it, and
so the skill can never be a different version from the tool it describes. That is why it lives in
the package and not in a `skills/` directory.

`sand skill install` writes it to `~/.agents/skills/sand.md` and links the harnesses that are
actually installed on that machine at that one file. The program owns this, not the Makefile:
the binary is what carries the text, and the machine that needs the skill may have no checkout
and no `make`. That is not hypothetical any more: a coworker's box gets its binary from
`install.sh`, which downloads a release and, finding a harness there, runs `skill install`
itself, so the box never sees this repo.

Install prints the version it wrote from, because "the skill disagrees with the tool" is only
diagnosable if both ends can be named. The other end is `sand --version`.

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
