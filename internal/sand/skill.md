---
name: sand
description: Work GitHub issues, PR reviews, review comments and failing CI checks on the sandbox box, where sand puts issue context, new review drafts, review threads and CI failures under ~/.sand/<owner>/<repo>/. Use when asked to review the current branch, draft or write a new GitHub issue, brainstorm or implement the issue named by the current <user>/<issue>-... branch, address review feedback, reply to reviewer comments, work through a pulled PR review, or fix the failing checks pulled to pr-<n>/ci/.
---

# Working GitHub issues and PR reviews on the box

This box has no `gh` and no GitHub API token. Issues and review threads get here as files, and
replies leave the same way. `sand` itself runs on the Mac; never try to run `sand` or `gh` here.

Git goes one way around a ring, and this box owns two of its four steps:

    GitHub --pull--> this box --the Mac's `make sync`--> Mac --push--> GitHub
                      ^                                  |
                      +---- the Mac's `sand sign` --------+

So: `git pull` and `git fetch` here, read only, that is the box's credential. Every merge happens
here too, including bringing `main` into a branch that has fallen behind. Never `git push` from
here, to GitHub or anywhere else: the Mac pulls from this box, and what leaves for GitHub has to
be signed, which needs a key this box does not have.

**Your branch gets rewritten under you, and that is normal.** Signing cannot add a signature
without re-creating the commit, so after the Mac runs `sand sign` every commit it signed has a new
hash. It pushes that history straight back into this checkout, updating the working tree, so the
branch you are on can change hash between one round and the next without you doing anything. Two
things follow, and both are on you:

- **Commit before you stop.** The Mac's push refuses to land while this tree is dirty, so `sand
  sign` now checks this checkout before it rewrites anything and refuses the whole round when a
  tracked file is modified here. Uncommitted work at handoff is the one way to cause that, and it
  stops the round with a named file rather than halfway through. Untracked files do not count, so
  a build artifact is not a problem. Nothing else about receiving the push is yours to arrange:
  `receive.denyCurrentBranch` is set from the Mac when it is missing.
- **Stay on the branch you were given, and say which one it is when you report.** The branch
  this checkout has out is what a bare `sand sign` on the Mac takes the round to be about, and
  it refuses to guess when the Mac is on a different one, naming both. Switching away here to
  look at something else and leaving it switched is what causes that. If you do have to end on
  another branch, the last thing you say should be which.
- **Never try to fix a hash mismatch by hand.** No rebase, no amend, no reset onto an older
  commit, and no branch built on what the branch used to be. Copies of commits that are already
  signed and on the remote are the failure this arrow exists to prevent, and the Mac refuses to
  sign a branch carrying them. If the branch here looks behind or wrong, say so and stop.

## New issues

When the user asks you to write or draft a new issue, create
`<remote_dir>/<owner>/<repo>/issue-draft/issue.md`. A sand-started prompt names the exact path. In
an ordinary harness conversation, use the sand base and owner/repo already named by the task; if
none is named, derive the GitHub owner/repo from this checkout's `origin` and use the default base
`~/.sand`. Create the directory if needed.

The file has YAML front matter with one non-empty, single-line `title`, then the proper GitHub
Markdown body:

```markdown
---
title: A concise issue title
---

The issue body.
```

No fix needs to exist, and the current branch is not part of the issue; consult existing code and
docs only when they help verify or clarify the request. Load the voice skill's rules, voice and
matching corpus samples. Preserve useful code fences. Describe the problem, desired outcome and
material constraints; do not invent facts or prescribe an implementation the request does not
require. Do not edit code, commit or try to publish the issue. Tell the user where you wrote the
draft and that `sand issue create` on the Mac will publish it. The Mac consumes the file after
creating the issue through authenticated `gh`; this box still gets no GitHub credential.

As a convenience, `sand issue create <brief>` may start you only to do this drafting job. Follow
the same file contract and restrictions from its prompt.

`sand new <issue-number>` puts the issue at `~/.sand/<owner>/<repo>/issue-<n>/issue.md` and
checks out `<prefix>/<n>-<title>` in this repo, the prefix being whoever runs sand on the Mac
(their configured `branch_prefix`; sand requires one). Read `issue.md` before brainstorming or
changing code. Before handing work back, write a concise PR body to `pr-description.md` beside it. Lead
with what changed, include any risk that remains and end with `Fixes: #<n>`. Do not include a
test plan. `sand up` on the Mac refuses to open the PR without this file.

`sand pr create` on the Mac may start you only to draft that PR. Its prompt names the optional issue path and required output files. Load the voice skill's `pr-description` register, including `~/.claude/voice/rules.md`, `~/.claude/voice/voice.md` and matching corpus samples. Inspect the complete branch diff and commit history plus the issue when one is linked. Write one line to `pr-title.txt` and proper GitHub Markdown to `pr-description.md`, preserving useful code fences. Do not edit code or commit during that run.

## Writing a new PR review

When you are asked to review the current branch, act as a reviewer rather than as the author.
Inspect the complete diff from its merge base with the base branch, relevant surrounding code,
and tests. Use a base named by the user; otherwise read `refs/remotes/origin/HEAD`, falling back to
`origin/main` only when needed. Do not edit code, commit, answer an existing review thread or write
under `pr-<n>/`. Findings here become new inline review comments, not responses.

Derive the GitHub owner/repo from `origin`, read the exact current branch and full commit with
`git branch --show-current` and `git rev-parse HEAD`, and use the configured sand base named in the
conversation or `~/.sand` by default. Write the draft under
`<remote_dir>/<owner>/<repo>/reviews/<branch>/`; slashes in the branch are directory separators.
Before writing, remove only an earlier `review.md` and comment Markdown files in that exact branch
directory so findings from an older pass cannot leak into this one.

Write `reviews/<branch>/review.md` with YAML front matter recording the exact branch, full commit
and comparison base:

```markdown
---
branch: person/topic
commit: 0123456789abcdef0123456789abcdef01234567
base: origin/main
---
```

Then write one `comment-NNN.md` per finding. The body is the exact GitHub comment and the front
matter locates it:

```markdown
---
path: internal/example.go
line: 42
side: RIGHT
---

This error path returns before the descriptor is closed.
```

Only report concrete, actionable defects introduced by the branch. Put each comment on a line
that is part of the PR diff: `side: RIGHT` and the new-file line number for added or modified
code, or `side: LEFT` and the old-file line number for a deleted line. Use repository-relative
paths. Do not add praise, a review summary, speculative concerns or a reply/commit/status field.
If there are no findings, write only `review.md` and report that; `sand pr review` will create
nothing from an empty draft.

Finish by naming the branch, reviewed commit, and files written. The user then checks out that
branch on the Mac and runs `sand pr review`. The Mac verifies that the commit still equals the
GitHub PR head, creates all comments in one pending review, and leaves it unsubmitted for the user
to inspect and submit. Never run `sand`, `gh`, or try to publish the review from this box.

## Review files

`~/.sand/<owner>/<repo>/pr-<n>/` (base dir is `~/.sand` unless the Mac's config overrides
`remote_dir`):

- `index.md` — PR title, url, branch, thread table, plus review summary bodies. Read-only:
  regenerated by every pull, and GitHub has no threaded reply for a review body. A summary can
  be the only feedback on a PR: a reviewer whose findings all fall outside the diff (CodeRabbit
  does this often) cannot open a thread, so the thread table says None and the work is in the
  summary. Fix and commit what it asks for like any thread, but write no reply file for it and
  say what you did in your final report, because nothing of yours goes back for a review body.
- `c-<comment-id>.md` — one per unresolved thread: YAML front matter, the conversation
  (blockquoted), the diff hunk, then a `## reply` slot.
- `ci/` — the failing CI checks, when `sand ci pull` has been run. Its own `index.md` links to
  one file per workflow and check. New files use `ci-<check>-<digest>.md`; older files keep
  their names. See "Failing CI" below; nothing about it goes back to GitHub.

Start at `index.md`, and read its review summaries as well as its thread table. Anything with
`status: pending` needs work; `status: sent` is already posted, leave it alone. A new reviewer follow-up reopens a sent thread with an empty reply
slot. Read the earlier reply in the conversation before drafting the next one.

## You may have been started by the Mac

`sand comments pull` and `sand ci pull` start an agent here as soon as they have written the
files, in the repo checkout the PR is about (`~/projects/<repo>`), with a prompt naming the
directory. If that is you, nobody is watching the terminal: work every pending item, then finish
by saying which you dealt with and which you left and why. The Mac reads the files back and
prints that list either way. Nothing changes about the loops below; a human who ran
`pull --no-agent` and then asked you the same thing gets the same work.

You hold a lock on that checkout for as long as you run (`flock` on
`~/.sand/locks/<repo>.lock`). Comment pull, CI pull and comment push hold the same lock while
syncing files, including `--no-agent` pulls. They refuse while you are working, so finish what
you were given and stop rather than staying open. An agent started manually should hold this
same lock while editing the review or CI files.

## The loop

1. Read the thread file: the conversation and the `## diff` hunk say what the reviewer
   wants. `path` and `line` in the front matter point at the code.
2. Fix the code in the actual repo checkout, not in `~/.sand`. Prove bugs with a failing
   test first, run the project gate (`make check`).
3. Commit the fix.
4. Edit the thread file: write the reply under the `## reply` heading, and put the fixing
   commit's short hash in `commit:` in the front matter.
5. Repeat for every pending thread, then say so. The rest happens on the Mac, with
   `sand up`: it signs the branch, pushes it, checks GitHub agrees the commits are verified,
   and posts the replies. You cannot do any of it from here, and you do not need to.

## What you may edit in a thread file

Only two things: the body under `## reply`, and `commit:`. Everything above the reply
heading is regenerated on the next pull, so edits there are lost, and `status:` belongs to
push. An empty reply slot means "say nothing" and is a valid answer for a thread that
needed no change; the HTML hint comment in the slot is stripped, so leaving it is fine.

Re-running `sand comments pull` preserves unsent drafts and their `commit:` once the lock is
free. Sent markings survive until a reviewer follows up. Everything else is refetched.

`status: pending` on a thread does not always mean unposted. Push decides what is already
posted by reading the thread on GitHub and matching your reply text against it, not by trusting
`status:`, so a reply that went out while the box was unreachable is re-marked on the next run
instead of posted twice. Two things follow: do not edit `status:` to make something re-post, and
do not reword a reply that has already been posted unless you mean to post a second one, because
changed text no longer matches what is on the thread. Follow-ups are new replies on purpose.

## Reply content

Name what changed and link the commit that changed it, backticked short hash. No bare
"done", no "fixed in a follow-up" without the hash. If you disagree with the comment, say
what you did instead and why, in one or two sentences. If a thread is `outdated: true` the
code moved under it, so check the current file before answering.

## Signatures, and the hash you record

Commits made here are unsigned: this box has no keys and is not getting any. They are signed on
the Mac, which re-creates the commit with a signature and so gives it a new hash, then pushes
that history back into this checkout. Do not try to sign or rewrite history here.

Commit as yourself, meaning this box's configured `user.name` and `user.email`. Never set
`GIT_AUTHOR_*` or `GIT_COMMITTER_*`, never `--author`, and do not merge or cherry-pick anyone
else's commits onto the branch. The Mac refuses to sign a commit whose author or committer is
not its own identity, because its signature would then say it vouches for work it did not do,
and that refusal stops the whole round: no replies get posted until a human sorts it out.

Record your own hash in `commit:` anyway. The Mac looks the recorded commit up on the pushed
branch when it posts, and when signing has replaced it, it posts the replacement's hash instead
(same tree, same subject, new hash). Nothing is expected of you about signing, and re-pulling to
chase a hash is wasted work.

If a commit never reaches the branch or gets dropped later, the Mac cannot match it and posts the
reply without a dead link. Commit fixes on the PR branch and do not amend or drop them afterwards
when the link matters. Signing before posting is still not optional, which is why the Mac runs
`sand up` rather than posting on its own. Recovery branches from signing and importing belong to
the Mac; `sand cleanup` lists and deletes them there when they are no longer needed.

## Failing CI

`sand ci pull` on the Mac writes the PR's failing checks to `~/.sand/<owner>/<repo>/pr-<n>/ci/`:
`index.md`, then one file per workflow and check with front matter, the tail of the failed
steps' log and a `## notes` slot. Follow the index links; filenames include a digest to keep
different workflows and check names from overwriting each other.

Same shape as a review thread, with one difference that decides everything else: **nothing here
is posted to GitHub.** There is no `ci push`. A red check is answered by a commit, not by a
comment, so the fix leaves this box the way every other one does, and CI running again on what
the Mac pushed is the verdict. Do not try to re-run a workflow, comment on the PR, or reach
GitHub from here.

The loop per check:

1. Read the log in the file. It is the tail, cut to the last few hundred lines, and the file
   says how many were dropped; the `link` in the front matter is the whole thing, for a human.
2. Reproduce the failure in the repo checkout. A test that fails here for the reason CI says is
   the only proof the fix is the right one; a "should be fixed now" is not.
3. Fix it, run `make check` (or whatever gate the repo has), commit.
4. Write what you changed under `## notes`, put the commit's short hash in `commit:`, and set
   `status: fixed`.

Only those three are yours: `## notes`, `commit:`, `status:`. Everything else is regenerated by
the next pull. Nothing verifies `status: fixed` here, the next CI run does, so it means "I
believe this passes now", not "this passed".

Notes alone do not mark a check fixed. If a later run fails, pull resets the status to pending
and clears the fixing commit, but keeps your notes so the next attempt knows what was tried.
`completed_at` distinguishes reruns of the same Actions run; leave that field to pull.

A check with no log is not a bug in the pull. Buildkite and anything else that posts a commit
status rather than an Actions run has no log the Mac can fetch, and the file says so: work from
the check's name, the diff and the link, or say plainly that you cannot without the log.

A file whose `bucket:` is not `fail` is a check that went green between pulls. Nothing to do,
and its notes were kept only so you can see what was already tried.

## Screenshots

`sand shot` on the Mac grabs the screen and drops the image in `~/.sand/shots/` here, then puts
that path on the Mac's clipboard. So a path like `~/.sand/shots/ss-2026-09-01-142530481.png`
pasted into a prompt is a real file on this box: read it. Nothing pulls or expires them, and
they are outside every PR directory, so a shot is context for whatever is being asked, not part
of a thread.

## This file

It lives at `~/.agents/skills/sand.md`, and what you are reading may be a symlink to it from a
harness skill directory. The Mac writes it there over ssh: the text is compiled into `sand`, and
`sand new`, `sand comments pull` and `sand ci pull` each install it before they hand this box
any work, so it is the same version as the tool that wrote the prompt you were started with.

Nothing about it is yours to maintain. There is no `sand` on this box to re-install it with,
this repo needs no checkout here, and editing the file or its link is pointless because the next
pull overwrites it. If it disagrees with what actually happens, say so and stop rather than
working around it; the fix is `sand skill install --remote` on the Mac, and it is one command.

## Multiple PRs

One directory per PR, so `~/.sand/*/*/pr-*/index.md` is the list of what has been pulled.
Nothing here tracks freshness: if the thread list looks stale, ask for a re-pull rather
than guessing at what the reviewer said.
