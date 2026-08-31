# `sand`

Helper CLI for the PR review loop between a Mac and a sandbox dev box. Internal tool.

The box writes the code and has no GitHub access. The Mac has `gh`, the ssh keys and the
signing key, and never edits code. `sand` runs on the Mac and moves work between them:

1. `sand comments pull` fetches the unresolved review threads for a PR, writes them to the
   box as markdown and starts an agent there to answer them.
2. The agent edits code, commits (unsigned, the box has no key) and drafts a reply per thread.
3. `sand up` signs those commits on the Mac, pushes, waits for GitHub to call them verified,
   then posts each reply to the review comment it answers.

Step 3 is one command because the order matters. Signing rewrites commits, so a reply that
quotes a hash has to be posted after the hash it quotes is final and on the remote.

## Requirements

On the Mac:

- Go 1.26+, `git` and `gh` (authenticated: `gh auth status`)
- `aif`, which is what imports a box branch into the Mac checkout for `sand sign`
- a commit signing key configured in git, and the same key added to your GitHub account as a
  signing key. GitHub reports commits as unverified without it and `sand up` stops rather than
  post replies quoting them.
- ssh to the box, by whatever alias you give `sand config init`

On the box: a checkout of each repo under `~/projects/<repo>`, and an agent CLI (`claude` or
`pi`) with the box-side skill installed.

## Install

On the box, from this repo:

    make install         # ~/go/bin/sand, or $GOBIN
    sand skill install   # writes ~/.agents/skills/sand.md and links the harnesses present

`skill install` is the only reason the box needs the binary. The skill text is compiled into
it, so the skill can never be a different version from the tool it describes.

On the Mac, first time:

    git clone git@github.com:guygrigsby/sand.git
    cd sand
    make install
    sand config init     # asks for the box, writes ~/.config/sand/config.yaml

After that, in the Mac's copy, on the branch you want from the box:

    make sync            # fetch that branch from the box, fast forward, install

`make sync` takes no argument: it reads the box from `sand config get host`, the same host the
tool itself uses, so `sand config set host <alias>` moves both. `BOX=<alias>` overrides. It
fast forwards only, so it stops rather than discarding a branch the Mac has signed; push that
back to the box instead.

Or push from the box, when the Mac accepts ssh: `make ship MAC=user@mac`. That is an rsync with
`--delete` into `src/sand`, a build copy rather than a checkout.

The box is the canonical source, so never edit the Mac's copy: `sync` refuses to overwrite the
edit, and nothing downstream of it will accept an uncommitted change anyway.

## Use

Start an issue from the Mac:

    sand new 1532                 # fetch issue, create its box data dir and switch both checkouts

This writes `issue.md` under `<remote_dir>/<owner>/<repo>/issue-1532/` and creates
`guy/1532-<issue-title>` from `origin/main` on the Mac and box. The box agent writes
`pr-description.md` beside the issue before handoff.

Everything else defaults the PR to the one for the current branch. A number or a PR URL overrides.

    sand comments pull            # threads to the box, agent starts, output streams back
    sand comments pull --no-agent # just write the files
    sand up                       # sign, push, open a missing PR, verify, post replies
    sand push                     # alias for sand up
    sand up --dry-run             # all four steps, changes nothing anywhere

Finer grained, if you want the steps apart:

    sand sign [branch]            # sign what is not signed yet, offer to push
    sand comments push            # post the drafted replies

Signing shows the branch diffstat before it asks, and refuses any commit whose author or
committer is not your git identity: your signature would be vouching for someone else's work.
`--allow-other-authors` overrides that, and `--yes` deliberately does not.

One agent per repo checkout on the box, enforced with `flock` there: a second `pull` for
another PR of the same repo refuses to start one while the first is working, rather than let
two agents edit one tree. `--no-agent` writes the files and leaves it alone.

With no PR for the current `guy/<issue>-...` branch, `up` reads the box-authored
`issue-<n>/pr-description.md` after signing and pushing, opens the PR, then verifies it. Missing
or empty prose is a stop rather than a generated body.

`comments pull` is safe to re-run. Replies already drafted on the box survive, and a thread
already posted stays `status: sent` and is not posted twice.

The files land in `<remote_dir>/<owner>/<repo>/pr-<n>/` on the box: `index.md` plus one
`c-<comment-id>.md` per thread. An agent on the box reads them through the installed skill.

## Config

`~/.config/sand/config.yaml`. Flags beat `SAND_<KEY>` in the environment, which beats the file,
which beats the defaults.

| key | default | what it is |
|---|---|---|
| `host` | none, required | ssh alias or `user@host` for the box |
| `remote_dir` | `~/.sand` | base dir on the box for the thread files |
| `harness` | `claude` | agent CLI `pull` starts on the box: `claude` or `pi` |
| `model` | the harness's own | model to pass it, in that harness's spelling |

`host` has no default because it names one machine on one tailnet. If the tailnet refuses your
Mac's local username, put the login user in the Mac's `~/.ssh/config` or in the host itself:
`sand config set host ubuntu@<box>`.

    sand config                   # print the file
    sand config init              # create it, or bring an existing one up to date
    sand config get host          # one effective value, for scripts
    sand config set harness pi    # set any key

`init` is safe to run again. It keeps every value the file already holds, adds any key added
since the file was written and refreshes the comments, so running it twice writes the same
bytes. It writes no defaulted value, only the keys with their defaults named in comments, so a
later change to a default still reaches a config that never set it.

## What it will not do

The point of the loop is that a reply on GitHub is evidence: it names a commit, and the commit
carries a signature. So `sand` fails closed wherever it cannot keep that true.

- No GitHub token ever enters the program. `gh` holds the auth, on the Mac only.
- The box gets no signing key and never talks to GitHub. The Mac signs and posts, and never
  edits code. Neither half can do the other's job by accident.
- `push` refuses to post while GitHub reports any commit of the PR as unverified, and holds back
  any single reply whose `commit:` it cannot resolve to exactly one commit on the pushed branch.
- `push` asks GitHub what it has already said on each thread before posting, so an interrupted
  run re-marks instead of posting twice. If that check cannot be made, it posts nothing.
- `sign` refuses commits whose author or committer is not your git identity, and shows you the
  diffstat before it asks.
- One agent per checkout on the box, under `flock`.
- Everything that writes anywhere but this Mac takes `--dry-run`: `comments pull`,
  `comments push`, `sign`, `up`.

## Development

Edit in this repo, on the box. Not on the Mac, not over ssh into a deploy target.

    make check   # the gate: go vet, gofmt, tests, linux build, darwin/arm64 build

`make check` is what CI would call and what to run before every commit. The darwin build is
part of it because the Mac is the real target.

The box has no `gh`, no GitHub token and no route to github.com, so the tests fake both ends: a
`gh` stub on `PATH` and `SAND_SSH` pointed at a shim that runs the "remote" command locally
(`internal/sand/e2e_test.go`). That covers the GraphQL decode, the file format, tar in both
directions, the merge on re-pull, the reply POST and the sent marking. It cannot cover GitHub
itself, so any claim about real API behaviour needs a run on the Mac against a real PR.

Two rules that are easy to miss:

- Every change updates `internal/sand/skill.md` in the same commit, or says in the commit
  message why the box side is untouched. The skill is the only thing telling an agent on the
  box how this loop works and that agent cannot read the source of a tool it never runs.
- `CLAUDE.md` carries the design decisions and the reasons behind them. Read it before changing
  behaviour, and keep it current when you do.
