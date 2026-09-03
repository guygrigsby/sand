# `sand`

`sand` is a helper CLI for development on the sandbox box. It runs **on the Mac**, and this repo
is the canonical copy of it: the box edits and merges, the Mac fast forwards, signs and pushes.

The design notes live under `docs/`, one file per area, because there is too much of them to read
for a one-line change. This file is the index and the rules that hold whatever you are changing.

## Read this first, then the one file you need

| read | when |
|---|---|
| [docs/ring.md](docs/ring.md) | before anything that moves git history, pushes, or touches `make sync` / `make ship`. Why the ring goes one way, why signing needs a return arrow, and the afternoon it cost to find out. |
| [docs/workflow.md](docs/workflow.md) | `sand status`, `sand new`, `sand up`, `sand shot`: the commands that move work between the three machines. |
| [docs/signing.md](docs/signing.md) | `sand sign`. The longest file here, and the one with the most ways to lose work: read it before changing anything about signing, pushing or realigning the box. |
| [docs/review.md](docs/review.md) | `sand comments pull` / `push`: the thread files, what belongs to the box, how "already posted" is decided, and the agent lock. |
| [docs/ci.md](docs/ci.md) | `sand ci pull`, and why there is no `ci push`. |
| [docs/config.md](docs/config.md) | the config keys, precedence, and how `config init` / `set` render the file. |
| [docs/box-skill.md](docs/box-skill.md) | `internal/sand/skill.md`, the harness table, and how the skill reaches a box that runs no `sand`. |
| [docs/development.md](docs/development.md) | `make check`, how the tests fake `gh` and the box, and how a release is cut. |

`README.md` is the coworker-facing version: what to install, what to run, what it refuses to do.
Keep it true when behaviour changes, but the reasons belong in `docs/`.

## The ring, in one diagram

    GitHub --pull--> box --make sync (fetch, --ff-only)--> Mac --push--> GitHub
                      ^                                    |
                      +------ sand sign, after the push ----+

Every step is the only machine that can do its step. The box has no signing key, no `gh`, no API
token, a read-only git credential and no `sand`: the skill its agent works from is written there
over ssh by the Mac. The Mac never edits code and never merges. Signing
re-creates commits, so the rewrite has to go back to the box in the same command that made it, or
the box builds on a lineage that no longer exists. Details and the failure modes: `docs/ring.md`.

## Commands

Each Mac-side script for this workflow is a subcommand rather than a shell file: `init`, `new`,
`status`, `comments`, `ci`, `up` (`push` is an alias), `sign`, `shot`, `skill`, `config` (`init`,
`get`, `set`). `sand init` is the whole setup of a Mac and the only command a new one needs;
`sand config init` is still the file half of it, for scripts.

## Rules for changing this repo

- **Never edit source on the Mac or on any other deploy target.** Edit here, commit, and let the
  Mac's `make sync` fast forward to it. A file edited over there is drift with no way back.
- **Every change updates `internal/sand/skill.md` in the same commit.** The skill is the only
  thing telling an agent on the box how the loop works, and that agent cannot read the source of
  a tool it never runs. If a change moves a path, renames a field, changes what `push` reads or
  adds a command, the skill says so before the change lands. If a change genuinely does not touch
  the box side, say that in the commit message rather than leaving it unsaid.
- **`make check` is the gate**, before every commit: `go vet`, `gofmt`, tests, a linux build and
  the darwin/arm64 build. The darwin build is in it because the Mac is the real target.
- **No GitHub token, ever.** `gh` holds the auth, on the Mac only. No secret goes on the box.
- **A decision with a reason gets the reason written down**, in the `docs/` file for its area,
  next to the behaviour it explains. Most of what is in there was learned by losing something;
  the note is what stops it being re-learned. A behaviour change updates its note in the same
  commit as the code.
