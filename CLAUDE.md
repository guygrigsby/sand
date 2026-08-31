# `sand`

`sand` is a helper CLI for development on the sandbox box. It runs **on the Mac**.

## Two machines, one direction of authority

| | Mac | sandbox (whatever `sand config get host` says) |
|---|---|---|
| runs `sand` | yes | no |
| `gh` auth, GitHub access | yes | no |
| edits code | no | yes |
| canonical source of `sand` | no, a build copy | yes, this repo |

Edit here, never on the Mac. There is no git remote, so source reaches the Mac by rsync:

- On the Mac, in its copy: `make sync` (rsyncs from here, then installs). No argument needed:
  `BOX` comes from `sand config get host`, i.e. the same host the tool itself uses, so
  `sand config set host <alias>` moves both. `BOX=<alias>` and `SAND_HOST` still override.
- From here, if the Mac accepts ssh: `make ship MAC=user@mac`. No default: the Mac's address
  is not in sand's config and guessing it would point an rsync `--delete` at whatever answered.

Both use `--delete`, so a stray edit on the Mac is overwritten rather than left to diverge.

The `BOX` lookup runs `$(GO) run . config get host` from the checkout, not an installed `sand`:
`make sync` is what installs the new binary, so the installed one is by definition the old one,
and an older one with no `config get` answers with the whole config file. The recipe refuses a
`BOX` that is empty or more than one word rather than handing that to rsync. It is expanded
inside the `sync` recipe only, so `make build` and `make check` never pay for the compile.

If the tailnet refuses the Mac's local username (`tailnet policy does not permit you to SSH as
user "<local-user>"`), the login user belongs in the Mac's `~/.ssh/config`, or in the host itself
as `sand config set host ubuntu@<box>`. Not in the source: a machine-specific login is not a
thing to hardcode for every machine.

## Commands

`sand` is where the Mac-side scripts for this workflow are being consolidated, so each one is a
subcommand rather than a shell file: `comments` (below), `up`, `sign`, `skill`, `config`
(`init`, `get`, `set`).

## The whole Mac side: `sand up [pr]`

One command for everything the Mac owes a review round, in the only order that is safe, each
step verified before the next runs and printed so a watching human can check it:

1. `sign` the branch the PR comes from (whatever is not signed yet).
2. `push` `--force-with-lease`, then re-read the remote ref to prove it moved. Signing pushes
   what it rewrote itself, so this step only has work when there was nothing to sign: a branch
   signed in an earlier round, or by hand, that never reached the remote.
3. `verify` that GitHub reports every commit of the PR as verified. A failure here is almost
   always the signing key missing from the GitHub account, so the error says that.
4. `replies`: `comments push`.

Flags: `--pr`, `--remote`, `--base`, `-y/--yes`, `--allow-other-authors`, `--dry-run`. The dry run covers all four steps
at once and changes nothing anywhere. Declining the rewrite at step 1 stops the run rather than
posting replies about commits that were never signed.

The order is the whole point: a reply quotes a commit hash, signing changes commit hashes, so
signing has to be finished and pushed and confirmed by GitHub before one reply goes out.

## Signing: `sand sign [branch]`

The box has no keys, so commits land unsigned and get signed on the Mac. `sand sign` imports the
branch with `aif`, then re-creates the commits the branch adds over `<remote>/<base>` that are
not signed already, with `git commit-tree -S` under `git filter-branch`, verifies the result and
offers to push with `--force-with-lease`. Flags: `--remote` (origin), `--base` (main), `--yes`,
`--push`, `--dry-run`, `--allow-other-authors`.

- **Only what is unsigned, and what sits on top of it.** Review is a loop, so most runs meet a
  branch that is already partly signed, and re-signing a commit moves its hash, which kills
  every reply already posted quoting it. The already-signed commits go to filter-branch as
  negative revs, so it never sees them. A commit is rewritten when it has no signature or when
  anything below it in the branch does: its parent hash changes, so its own bytes do.
  Everything unique to the branch already signed means the run is a no-op, with no recovery
  branch made. `SignResult` reports total, rewritten and kept, which is what `up` prints.
- **A signature is the `gpgsig` header, not `%G?`.** `%G?` answers "can this machine verify
  it", which needs a keyring and a trust config; what decides re-signing is whether the header
  is there at all.
- **Verification includes the kept hashes.** After the rewrite, every already-signed commit
  must still be on the branch, on top of the existing "all signed, count unchanged" checks.
- **The recovery branch name gets a `-2`, `-3` suffix if taken.** Two runs in the same second
  are normal in a review loop and the second one must not fail on the first one's name.

- **filter-branch, not rebase.** It replays the original trees with rewritten parents, so merge
  commits survive and no content conflict is possible. A rebase would flatten or stall.
- **Refusals come before the rewrite:** protected branch (`main`, `master`, `develop`, `trunk`,
  `release/*`), a rebase/merge/cherry-pick in progress, a dirty tree, a missing `aif`, a missing
  base ref, no common history. A missing `aif` is a stop, never a fallback to another sync route.
- **Verification comes after it:** every branch-unique commit must carry a `gpgsig` header and the
  commit count must be unchanged, or nothing is pushed. A recovery branch
  (`<branch>-before-signing-<timestamp>`) is made before the rewrite and kept afterwards.
- **Refuses commits this machine did not make.** A signature says the signer vouches for the
  commit, and `aif` imports whatever the box's branch holds: a merge of another branch, a
  cherry-pick, an agent with a different git config all put someone else's commits in front of
  the key. filter-branch keeps their author and committer, so the result would read "written by
  them, vouched for by you". Every commit being rewritten must have `user.email` as both author
  and committer, or the run stops before the recovery branch, naming them.
  `--allow-other-authors` is the way to say you do vouch for them. Deliberately not covered by
  `--yes`: that flag skips a question about work the operator already knows about, it does not
  widen what their key attests to. Only the commits being rewritten are checked, since the kept
  ones keep whatever signature they arrived with. skill.md tells the box agent to commit as
  itself for this reason.
- **The prompt shows the diffstat, not just the subjects.** The operator is being asked to
  attest to work another machine did, and hashes plus subject lines are a summary written by the
  thing being vouched for. `git diff --stat <base>..HEAD` is the cheapest answer to "what am I
  putting my name on", and it prints in the dry run too.
- **Both prompts read one stdin through one reader.** A `bufio.Reader` per question swallows the
  next answer, which silently turned a "yes, push" into "not pushed" (`sign_test.go` covers it).
- **Signing changes hashes.** A hash already quoted in a posted reply stops existing upstream, so
  sign before `comments push`, not after. `comments push` enforces that order (below).
- **`--dry-run` stops before the rewrite**, so no history moves, no recovery branch is made and
  nothing is pushed. It still runs `aif` and `git fetch`: what would be signed is not knowable
  without the branch and the base.
- `SAND_AIF` overrides the `aif` binary, which is how the tests import a branch without it.

## First feature: PR review comments

- `sand comments pull [pr]` — unresolved inline review threads plus review summary bodies for
  the PR, written to `<remote_dir>/<owner>/<repo>/pr-<n>/` on the box: `index.md` plus one
  `c-<comment-id>.md` per thread. Re-running is safe; drafts on the box survive.
- `sand comments push [pr]` — reads those files back, posts each non-empty `## reply` as a
  threaded reply to the review comment it answers, marks it `status: sent`.

`pull` then starts an agent on the box on what it just wrote, over one held-open ssh, in the
repo checkout (`~/projects/<repo>`, or `--repo-dir`), and streams its output back. The files are
useless until something reads them and making a human ssh in to type the same prompt is the step
that gets skipped, so this is the default rather than a flag. Nothing pending means no agent.
`--no-agent` skips it, `--agent '<cmd>'` replaces the command for one run. Claude Code's
`stream-json` is decoded into progress lines and anything else is passed through as is, so
another harness still reports. Whatever happens, the threads are read back afterwards and
printed as answered or left: an agent that died halfway is the case that matters.

The prompt names only the PR, the directory and the thread count, and points at the skill for
everything else. Two copies of the rules is two versions of the rules.

PR defaults to the one for the Mac's current branch; a number or a PR URL overrides.

`host` is the one setting with no default and the one thing a new Mac has to be told: it names
one machine on one tailnet, so a compiled-in alias is either someone else's box or a name that
resolves nowhere. `remote_dir` defaults to `~/.sand`, `harness` to `claude` and `model` to
nothing at all. Set them in `~/.config/sand/config.yaml`, or with `--host` / `--remote-dir`, or
`SAND_<KEY>` in the environment, in that order of precedence. `harness` is which agent CLI
`pull` starts (from the one harness table) and `model` is what to pass it, in that harness's own
spelling. An empty model is a real answer: the harness picks, which is the only answer that does
not go stale every time a model ships. Neither has a flag, because only `pull` reads them and it
already has `--agent` for a one-off.

`sand config` prints the file, `sand config init` creates or updates it, `sand config set <key>
<value>...` sets any key, and `sand config get <key>` prints one effective value and nothing
else, for scripts and the Makefile (`get` applies env and the defaults; `config` only shows what
the file says).

`set` and `init` both render the whole file from the struct: the keys, their order and the
settable list all come from `Config`'s yaml tags (`configFields`), with the per-key comments in
`configDoc`, so a new config field is settable and documented without touching the command.
Values go through `yaml.Marshal`, because `~`, `yes` and `user@host:2222` do not all survive
being printed by hand.

- **Neither writes a defaulted value.** A `remote_dir: ~/.sand` line is indistinguishable from
  one someone chose, so writing today's default would stop tomorrow's from ever reaching this
  machine. Every key is present and empty, with its default named in the comment above it.
- **`init` is idempotent, and is also the upgrade path.** It creates the file when missing and
  otherwise re-renders it: same values, plus any key added since the file was written, plus
  this version's comments. Running it twice writes the same bytes, so a setup script never has
  to ask which case it is in. It used to refuse a file that existed, which left no command at
  all for adding a new key to an old config.
- **`init` asks for the host, and takes no answer for an answer.** `--host` wins if given; an
  unattended run (no tty, EOF, empty line) writes the file with the host unset and warns,
  rather than blocking on a prompt nobody will answer or inventing a hostname.
- **Only `Resolve` requires a host, and `Get` does not go through it.** `Get` answers per key
  off the file, the environment and the defaults. It used to call `Resolve`, which meant that
  once `host` lost its default, `sand config get harness` failed on any machine that had not
  set a host, and `make sync` reads `config get host` on exactly those machines.

### Things that are the way they are on purpose

- **`gh` is the GitHub client.** No token ever enters this program. Threads come from one
  GraphQL query (`isResolved` exists nowhere else); replies go through
  `POST /repos/{o}/{r}/pulls/{n}/comments/{id}/replies`.
- **Replies target the first comment of a thread.** The API rejects replies to replies, which is
  why files are named after that comment id.
- **Replies post one at a time, with a delay and `Retry-After`-aware backoff.** That endpoint
  sends notifications and is the documented way to trip secondary rate limits. Do not
  parallelise it.
- **`## reply`, `commit:` and `status:` belong to the box.** Everything above the reply heading is
  regenerated by every pull; those three are merged forward. Breaking that loses an agent's work
  mid-review.
- **Review summary bodies are read-only context.** GitHub has no threaded reply for them.
- **The diff fence outruns the hunk.** Nothing in a thread file can be written with a fixed
  three-backtick fence: review a markdown file and the hunk arrives with fences in it. An added
  line is safe, `+` cannot start a fence, but an unchanged line is prefixed with one space and
  CommonMark allows three, so a context ``` closes the block and the rest of the hunk renders as
  prose. `fence()` picks a run longer than the longest one inside. Comment bodies are safe for a
  different reason, `quote()` blockquotes every line of them, which is also why a comment cannot
  forge a bare `## reply`.
- **`push` refuses to post while any commit on the PR is unverified**, listing the offenders and
  the `sand sign <branch>` that fixes them. GitHub's own `commit.verification` is the authority
  (`repos/{o}/{r}/pulls/{n}/commits`, paged by hand), not the Mac's checkout: what a reply quotes
  is the pushed history. Only checked when there is actually a reply to post, so a re-run with
  everything sent stays a no-op, and `--dry-run` warns instead of failing so the preview works.
- **Every command that writes anywhere but this Mac takes `--dry-run`:** `comments pull`,
  `comments push`, `sign`. `skill install` and `config init` write only local files.
- **`push` re-points a `commit:` that signing moved.** The agent on the box commits without a
  key and records that hash; signing then re-creates the commit, so the recorded hash stops
  existing exactly when the branch becomes postable, and every earlier round's replies would
  link to nothing. Before posting, each recorded commit is looked up on `<remote>/<branch>`:
  on it already, quote it as is; missing but matched by tree and subject, quote the replacement
  and say so; missing with no match, hold that one reply back and count it failed; unknowable
  here (no such ref, no such object), warn and post anyway, because a hash this checkout cannot
  reason about is not evidence that it is wrong; matched by more than one commit, hold that reply
  too, because tree plus subject is an identity claim and a duplicate on the branch (a
  cherry-pick, a merge that brought a copy back) makes it false, so quoting either is a coin
  flip. The corrected hash is written back to the box
  by the front matter rewrite that marks it sent. This is why `push` takes `--remote`.
- **`push` rewrites only the front matter** of a file it has posted, so the conversation text
  stays byte-identical to what pull wrote.
- **"Already posted" is answered by GitHub, not by `status:` on the box.** The POST goes out and
  then the box is told, and everything in between is a way to lose the telling: a dropped ssh, a
  rebooted box, a read-only disk, Ctrl-C during the second between two replies. The operator then
  re-runs, which is what every message here tells them to do, and the reply used to go out again
  and notify every subscriber again. So before posting anything, `push` reads the threads once
  more and collects what the authenticated account (`gh api user`) has already said on each of
  them; a draft contained in one of those is marked sent and skipped, counted as "already posted
  and re-marked". Containment rather than equality, because what gets posted is the draft plus a
  commit link and re-pointing can change that hash between runs. No second file, no local ledger:
  the state that decides is the state that cannot be lost. `e2e_test.go` proves it by making the
  box dir unwritable at the moment of the POST.
  - **That check failing is a stop, not a warning.** Not knowing what is on a thread is exactly
    the state where posting duplicates, so a fetch error refuses the whole run and says nothing
    was posted. `--dry-run` warns instead, because a preview that cannot reach GitHub is still a
    useful preview.
  - **Losing the marking is a warning, not an error.** By then the replies are on GitHub and
    exiting non-zero says the opposite. The message names what happens next: nothing was posted
    twice, the next run re-marks them.

## The skill for the box side

`internal/sand/skill.md` teaches an agent on the box how to answer the pulled threads: where the
files are, that only `## reply` and `commit:` are its to edit, and that push happens on the Mac.
It is `go:embed`ed into the binary, so a box holding nothing but `sand` can still install it, and
so the skill can never be a different version from the tool it describes. That is why it lives in
the package and not in a `skills/` directory.

`sand skill install` writes it to `~/.agents/skills/sand.md` and links the harnesses that are
actually installed on that machine at that one file. The program owns this, not the Makefile:
the binary is what carries the text, and the machine that needs the skill may have no checkout
and no `make`.

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

## Working on it

`make check` is the gate: `go vet`, `gofmt`, tests, a linux build and the darwin/arm64 build.

There is no `gh`, no GitHub token and no route to github.com from the box, so the tests fake
both ends: a `gh` stub on `PATH` and `SAND_SSH` pointed at a shim that runs the "remote" command
locally (`internal/sand/e2e_test.go`). That covers the GraphQL decode, the file format, tar in
both directions, the merge on re-pull, the reply POST and the sent marking. What it cannot cover
is GitHub itself — claims about real API behaviour need a run on the Mac against a real PR.
