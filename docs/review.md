# PR review comments: `sand comments pull` / `push`

- `sand comments pull [pr]`: unresolved inline review threads plus review summary bodies for
  the PR, written to `<remote_dir>/<owner>/<repo>/pr-<n>/` on the box: `index.md` plus one
  `c-<comment-id>.md` per thread. Re-running is safe; drafts on the box survive.
- `sand comments push [pr]`: reads those files back, posts each non-empty `## reply` as a
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

The agent runs under `flock -n` on `<remote_dir>/locks/<repo>.lock`, held for the whole run.
Two agents in one checkout is not a race to lose, it is a corrupted working tree: they edit the
same files, commit over each other and answer the same thread twice. It is also easy to cause,
since `pull` is one command per PR, two PRs share a repo, and an operator who thinks a run has
stalled re-runs it. A missing `flock` on the box is a stop, not a fallback to running unlocked.
The lock is keyed by repo rather than by the checkout path, so two `--repo-dir` runs of one repo
wait on each other: over-locking costs a wait, under-locking costs the tree. `flock -n` is
silent on failure, so the remote line uses exit codes (111 taken, 112 no checkout, 113 no flock,
114 no harness) and `RunAgent` turns them into the message.

- **The codes are outside 64-78 on purpose.** They were sysexits values, and flock reports its
  own failures in that range: it exits 69, `EX_UNAVAILABLE`, when it cannot exec the command.
  That was this tool's code for "no flock", so a harness missing from the box's PATH printed
  `cannot lock <box>:<dir>, so no agent was started`, a lock that was never the problem, and no
  mention of the binary that was. 111 and up collides with neither sysexits nor the shell's
  126/127 nor an agent's own status.
- **The remote line prepends `$HOME/.local/bin:$HOME/bin:$HOME/go/bin` to PATH, then checks the
  harness is there.** `ssh box '<cmd>'` is a non-interactive, non-login shell: on a zsh box
  `.zshrc` is never sourced, so PATH is the shell's compiled-in default and every agent CLI,
  which installs under `$HOME`, is invisible over ssh however well it works in a terminal on the
  same box. The three dirs are a guess at the usual layout rather than a fix for every one, so
  the check comes before the lock and the error says what the operator can do about it: put the
  binary somewhere `~/.zshenv` exports, link it into `~/.local/bin`, or point `harness` at one
  that is installed. Guessing more paths (a node version directory, a pyenv shim) is guessing at
  another machine's install, which is what the message is for.

PR defaults to the one for the Mac's current branch; a number or a PR URL overrides.

**Which PR the current branch has is asked by repo and head, never guessed from the remotes.**
`gh pr view` with no argument works the head repo out of the local remotes, and the box is one of
them: in aperture it read `guy-llm-sandbox:projects/aperture` as owner `projects`, looked for a
head of `projects:<branch>`, and reported no open PR for a branch whose PR was open. `sand
comments pull`, `ci pull` and `comments push` all entered through that, while `status` and `up`
were fine, because they went through `currentBranchPR`, which passes `--repo` and `--head`.
`ResolveTarget` now calls the same function: one implementation, and nothing about the ring's own
remotes can change the answer.

## Things that are the way they are on purpose

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
  `comments push`, `ci pull`, `sign`, and `skill install --remote`, which writes the skill on
  the box. Plain `skill install` and `config init` write only local files.
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
- **A 422 on every reply is a pending review on the PR, not a bad thread.** The replies
  endpoint attaches each reply to a fresh pending review, GitHub allows one pending review
  per user per PR, and a review draft left unsubmitted in the web UI therefore rejects every
  reply with `user_id can only have one pending review per pull request`. The draft is
  visible only to its owner: `gh api repos/{o}/{r}/pulls/{n}/reviews`, check its comments
  are nothing worth keeping, `DELETE` it, re-run. The sub-error used to be dropped with the
  rest of the `errors` array; `httpError` now prints it.
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
