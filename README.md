# `sand`

Helper CLI for the PR review loop between a Mac and a sandbox dev box. Internal tool.

The box writes the code, merges, and can pull from GitHub but not push to it. The Mac has `gh`,
the ssh keys and the signing key, never edits code and never merges: it fast forwards, signs and
pushes. Git goes one way around the ring, `GitHub -> box -> Mac -> GitHub`, with one arrow back:
signing re-creates commits, so `sand sign` pushes the signed branch to the box after GitHub has
it, or the box would keep building on a chain that no longer exists. `sand` runs on the Mac and
moves work between them:

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

On the box: a checkout of each repo under `~/projects/<repo>`, and an agent
CLI (`claude` or `pi`) with the box-side skill installed, findable over ssh. `ssh box '<cmd>'` gets none of an
interactive shell's PATH, so a harness that only `.zshrc` puts on PATH is not there when `pull`
looks for it; `~/.local/bin`, `~/bin` and `~/go/bin` are added for you, `~/.zshenv` covers the
rest. `pull` says which binary it could not find rather than starting nothing quietly.

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
fast forwards only, so it stops rather than discarding a branch the Mac has signed; `sand sign`
is what puts that branch on the box.

Or push from the box, when the Mac accepts ssh: `make ship MAC=user@mac`. That is an rsync with
`--delete` into `src/sand`, a build copy rather than a checkout.

The box is the canonical source, so never edit the Mac's copy: `sync` refuses to overwrite the
edit, and nothing downstream of it will accept an uncommitted change anyway.

## Use

Start an issue from the Mac:

    sand new 1532                 # fetch issue, create its box data dir and switch both checkouts

This writes `issue.md` under `<remote_dir>/<owner>/<repo>/issue-1532/` and creates
`<you>/1532-<issue-title>` from `origin/main` on the Mac and box. The box agent writes
`pr-description.md` beside the issue before handoff.

The `<you>` is `branch_prefix`, your `$USER` unless you set it. It is the name `sand up` reads
the issue number back out of when there is no PR yet, so a branch made by hand wants the same
shape: `sand config set branch_prefix <yours>` if `$USER` is not what you branch under.

Everything else defaults the PR to the one for the current branch. A number or a PR URL overrides.

    sand status                   # where the work is, on all three machines, and what to run next
    sand comments pull            # threads to the box, agent starts, output streams back
    sand comments pull --no-agent # just write the files
    sand ci pull                  # the PR's failing checks and their logs, same trip
    sand up                       # sign, push, open a missing PR, verify, post replies
    sand push                     # alias for sand up
    sand up --dry-run             # all four steps, changes nothing anywhere

Finer grained, if you want the steps apart:

    sand sign [branch]            # sign what is not signed yet, offer to push, realign the box
    sand comments push            # post the drafted replies

Signing shows the branch diffstat before it asks, and refuses any commit whose author or
committer is not your git identity: your signature would be vouching for someone else's work.
`--allow-other-authors` overrides that, and `--yes` deliberately does not.

Signing then pushes the result to the box, after GitHub has taken it. It leases against the head
it just read there, and stops rather than force pushing when the box has commits of its own since
the import, printing the `git rebase --onto` that puts them on top. Two things about that push
are handled for you rather than left as setup. `receive.denyCurrentBranch=updateInstead` is set
in the box's checkout, because git otherwise refuses a push into the branch that is checked out;
with it the working tree updates too, and the push is refused while that tree is dirty, which is
the behaviour worth having. And it goes with `--no-verify`, because a Mac that signs has a
pre-push hook refusing commits it cannot verify, which is right for the push to GitHub and wrong
for this one: the branch it hands the box on the recovery path is unsigned by design, and the
range such a hook measures against a URL with no tracking ref is the whole history. Nothing
reaches GitHub unchecked either way, since the box has no credential that can push there. A branch that was built on a
lineage an earlier signing round replaced is refused outright, before anything is rewritten: its
commits are unsigned copies of commits already on the remote, and pushing a second copy is how
the two histories drift apart unnoticed.

One agent per repo checkout on the box, enforced with `flock` there: a second `pull` for
another PR of the same repo, or a `ci pull` for the same one, refuses to start an agent while
the first is working, rather than let two edit one tree. `--no-agent` writes the files and
leaves it alone.

With no PR for the current `<you>/<issue>-...` branch, `up` reads the box-authored
`issue-<n>/pr-description.md` after signing and pushing, opens the PR, then verifies it. Missing
or empty prose is a stop rather than a generated body.

`sand status` is the one to run when you do not know which of those you want. It reads this Mac,
the box and GitHub at once and prints one `next:` line: the branch and unsigned count here, the
box's branch, dirty count and whether an agent holds the lock there, how many replies are drafted
and how many checks have notes, and what GitHub says is unresolved, failing or unverified. It
changes nothing anywhere. It does fetch, from the remote and from the box, because the thing it
is really looking for is commits on the box that are unsigned copies of commits already pushed,
and that is a question about trees rather than hashes. A branch with no PR is fine.

`comments pull` is safe to re-run. Replies already drafted on the box survive, and a thread
already posted stays `status: sent` and is not posted twice.

The files land in `<remote_dir>/<owner>/<repo>/pr-<n>/` on the box: `index.md` plus one
`c-<comment-id>.md` per thread. An agent on the box reads them through the installed skill.

`sand ci pull` is the same trip for a red PR: what `gh pr checks` calls failed, plus the tail of
each Actions run's failed steps, into `pr-<n>/ci/` as one file per check, then an agent on the
box under the same lock. `--log-lines` (300) bounds the log and the file says how many lines were
cut; a check that is not an Actions run (Buildkite and friends) gets its link and no log, because
the Mac has no client for it.

There is no `ci push`. A red check is answered by a commit, not a comment, so the fix leaves the
box through `sand up` like everything else and CI running again is the verdict. The agent writes
what it did under `## notes` in the check's file, which is for the next round to read, not for
GitHub.

## Config

`~/.config/sand/config.yaml`. Flags beat `SAND_<KEY>` in the environment, which beats the file,
which beats the defaults.

| key | default | what it is |
|---|---|---|
| `host` | none, required | ssh alias or `user@host` for the box |
| `remote_dir` | `~/.sand` | base dir on the box for the thread files |
| `harness` | `claude` | agent CLI `pull` starts on the box: `claude` or `pi` |
| `model` | the harness's own | model to pass it, in that harness's spelling |
| `branch_prefix` | `$USER` | what `sand new` puts before `<issue>-<title>` |

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
- `sign` will not force push onto the box over commits only the box has, and will not sign a
  branch whose commits are unsigned copies of commits already on the remote.
- One agent per checkout on the box, under `flock`.
- Everything that writes anywhere but this Mac takes `--dry-run`: `comments pull`,
  `comments push`, `ci pull`, `sign`, `up`.
- `ci pull` only ever reads from GitHub. Nothing it produces is posted anywhere.

## Development

Edit in this repo, on the box. Not on the Mac, not over ssh into a deploy target.

    make check   # the gate: go vet, gofmt, tests, linux build, darwin/arm64 build

`make check` is what CI would call and what to run before every commit. The darwin build is
part of it because the Mac is the real target.

The box has no `gh` and no GitHub API token, so the tests fake both ends: a
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
