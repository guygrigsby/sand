# Moving work: `status`, `new`, `up`, `shot`

## Where the work is: `sand status [pr]`

One read-only pass over all three machines, ending in one `next:` line. It decides nothing and
moves nothing.

The reason it exists is the ring's own failure mode: the box building on a lineage signing
already replaced is invisible in either machine's `git log`, because the two copies differ only
by hash. Before this, the first thing that said so was a rejected push three commands later, or
`sand sign` refusing after the operator had already committed to a round. `status` asks the same
question with nothing at stake.

- **The lineage check needs the box's commits, so it fetches them.** Whether a commit is a copy
  of a pushed one is a question about trees, and a hash on its own cannot answer it. `git fetch
  <box> <branch>` writes FETCH_HEAD and nothing else, so no ref this checkout sits on moves. It
  is the same `duplicatedOnRemote` the signing refusal uses, asked from the other side: two
  implementations of "is this commit already on the remote" would eventually disagree, and the
  one in `sand sign` is a hard stop.
- **One `git fetch <remote>` first, before the three groups.** Every count is measured against
  the remote-tracking refs, and two git fetches in one repository fight over the same lock for
  nothing. The three groups after it are three independent round trips and run at once. A failed
  fetch is a warning line, not a stop: a status that will not print because the network is down
  is a status nobody can use.
- **The box's counts come from the real parsers, not from grep over ssh.** `fetchDir` pulls the
  PR directory back (`ci/` rides along, being a subdirectory of it) and `loadThreadFiles` /
  `loadCIFiles` read it. A second implementation of "is this reply pending" is a second answer
  to it.
- **"New on GitHub" is decided by re-rendering, not by counting.** For each unresolved thread,
  merge the box's copy forward and render it the way `pull` would; different bytes mean a
  comment arrived since the pull. Comparing counts would miss a reply added to a thread that
  already has a file, which is the common case in a review loop.
- **The dirty count on the box excludes untracked files.** What blocks the realigning push at
  the end of signing is a modified tracked file. Counting a stray build artifact as a reason to
  stop would be a false alarm on every run.
- **A branch with no open PR is a normal thing to ask about**, unlike every other command here,
  so `status` uses `currentBranchPR` and skips the GitHub half rather than failing.
- **The `next:` order is the design.** The two states that stop everything (a duplicated
  lineage, an agent holding the lock) come before the work that is ready to publish, which comes
  before the work to bring over. Everything below the line it prints is still true and is what
  the next run will say; a list of five suggestions is what this command exists not to be.

## Starting an issue: `sand new <issue-number>`

`new` asks `gh` for the issue, derives `<branch_prefix>/<number>-<lowercase-title>`, fetches the configured
base and creates that branch in both the Mac checkout and `~/projects/<repo>` on the box. Both
checkouts must be clean and the branch must not exist. It writes the issue title, URL and body to
`<remote_dir>/<owner>/<repo>/issue-<n>/issue.md`; that directory is the durable handoff for
brainstorming and later holds `pr-description.md`.

The Mac branch exists so `sand up` has an unambiguous current issue before signing imports the
box commits. Creating a ref is bookkeeping, not source editing.

- **The prefix is config, and its default is `$USER`.** It was `guy/`, written into both
  `issueBranch` and `issueNumberFromBranch`, which is the one thing in here that could not
  work for a second person: their `sand new` named a branch after somebody else, and then
  their `sand up` could not find the issue number in a branch they had named themselves.
  `branch_prefix` defaults to `$USER` rather than being asked for like `host`, because unlike a
  ssh alias the machine already knows the answer, and an empty prefix is still a working
  branch name (`<issue>-<title>`) rather than a stop. One implementation reads and writes it
  (`branchPrefix`), so the name `new` creates is by construction the name `up` parses.

## Creating a PR: `sand pr create`

`pr create` closes the gap between finished code on the box and prose GitHub can open. It works from any current branch with no open PR. The configured agent starts in the box checkout under the same repo lock as review and CI agents, reads the full branch diff and commit history and uses the voice skill's `pr-description` register: `~/.claude/voice/rules.md`, `~/.claude/voice/voice.md` and matching corpus samples. A branch named for an issue also gets its existing `issue.md`; other branches need no issue. The agent writes a one-line `pr-title.txt` and byte-preserved GitHub Markdown to `pr-description.md` under the repo's sand directory. Code fences need no transport encoding or parsing because the body stays a file through `gh pr create --body-file`.

Once the files exist, it runs the same safe publish path as `up`: sign, push, open through `gh` on the Mac and ask GitHub to verify every commit. An existing PR is a stop. A missing title, multiline title, missing body or failed agent is also a stop before publication. `--dry-run` starts no agent and changes nothing. Generated prose cannot be previewed before it exists, so it reports the agent and publish steps it would run.

## The whole Mac side: `sand up [pr]`

One command for everything the Mac owes a review round, in the only order that is safe, each
step verified before the next runs and printed so a watching human can check it:

1. `sign` the branch the PR comes from (whatever is not signed yet), which also puts the rewrite
   back on the box once it is on the remote. A rewrite that reached GitHub and not the box gets
   its own warning line: it is the one outcome here that breaks the *next* round rather than this
   one, so it must not be left in the signing output for someone to notice.
2. `push` `--force-with-lease`, then re-read the remote ref to prove it moved. Signing pushes
   what it rewrote and a fully-signed branch the remote is behind, so on most runs this step
   reads "already at" and is the proof rather than the push. It still has work when something
   declined the push at step 1, or when the remote holds commits the branch does not.
3. `verify` that GitHub reports every commit of the PR as verified. A failure here is almost
   always the signing key missing from the GitHub account, so the error says that.
4. `replies`: `comments push`.

If the current branch has no open PR, its name must be `<branch_prefix>/<issue>-...`. After steps 1 and 2,
`up` reads `issue-<n>/pr-description.md` from the box and opens the PR with the issue title. It
then runs the same GitHub signature verification. A missing description stops the run. `push`
is an alias for `up` so both entry points have the same ordering and checks.

Flags: `--pr`, `--remote`, `--base`, `-y/--yes`, `--allow-other-authors`, `--dry-run`. The dry run covers all four steps
at once and changes nothing anywhere. Declining the rewrite at step 1 stops the run rather than
posting replies about commits that were never signed.

The order is the whole point: a reply quotes a commit hash, signing changes commit hashes, so
signing has to be finished and pushed and confirmed by GitHub before one reply goes out.

## Screenshots: `sand shot [file]`

The one thing that travels for a reason none of the others share: the screen is on the Mac and
whatever needs to look at it is not. `shot` runs `screencapture -i`, the cmd-shift-4 crop, sends
the image to `<remote_dir>/shots/` on the box and puts that path on the Mac's clipboard, which is
what makes it a command rather than an scp someone types.

- **The path on the clipboard is the box's, not the Mac's.** What it is for is pasting into a
  prompt for an agent running there, so it is the form that agent can open. `skill.md` tells the
  box side that `~/.sand/shots/...` is a real file it should read.
- **A cancelled selection sends nothing.** `screencapture` exits 0 and writes no file when the
  crop is escaped, so the file is what decides, not the status. Otherwise the box would collect
  empty images and the clipboard would hold a path to one.
- **A file argument skips the capture and sends that file.** It is the same trip, it is how the
  command works on a machine with no window server, and it is what the test drives: everything
  after "the image exists" is the part that can be wrong.
- **The name carries milliseconds, and the extension comes from the file.** `sendDir` writes by
  name, so two shots in one second would be one shot; a `.png` that is a jpeg is a worse answer
  than a `.jpg`, since what reads these goes by name.
- **Nothing expires them.** They sit outside every PR directory because a screenshot is context
  for a question, not part of a thread, and nothing here knows when a question is over.
