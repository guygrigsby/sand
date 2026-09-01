# Signing: `sand sign [branch]`

The box has no keys, so commits land unsigned and get signed on the Mac. `sand sign` imports the
branch with `aif`, then re-creates the commits the branch adds over `<remote>/<base>` that are
not signed already, with `git commit-tree -S` under `git filter-branch`, verifies the result,
offers to push with `--force-with-lease` and, once that push is on the remote, puts the same
history on the box. Flags: `--remote` (origin), `--base` (main), `--yes`, `--push`, `--dry-run`,
`--allow-other-authors`.

- **Nothing to sign is not nothing to do.** A branch can arrive here fully signed with the remote
  still behind it: `git rebase` on a Mac with `commit.gpgsign` signs what it replays, so the
  recovery from a duplicated lineage produces signed commits before signing ever sees them.
  Returning at "nothing to sign" left aperture's branch five commits behind GitHub with `--push`
  on the command line, and it was pushed by hand, which is the one step of the ring that has to
  be a signed push from this machine. Both endings go through `publish`, which pushes when the
  remote is behind, realigns the box either way, and pushes nothing when the remote is already at
  this head or holds commits the branch does not: a lease would let that second case rewind the
  remote, and a remote ahead of a fully-signed branch is something this run cannot account for.
- **The box gets the rewrite, from this command, right after the remote does.** Not a separate
  step someone remembers, because the cost of forgetting is not visible until the round after
  next (see `docs/ring.md`). `alignBox` runs only after the push to GitHub returned, so the box
  is never moved to a history the remote rejected, and it never force pushes on faith:
  `git ls-remote` reads the box's head first, the push leases against exactly that hash, and the
  box's ref is only overwritten when it is either what `aif` imported (the rewrite is the sole
  difference) or an ancestor of the signed head. Anything else means the box committed while
  signing ran, those commits exist nowhere else, and a force push would destroy the work this
  tool exists to carry: it stops, says so, and prints the `git fetch` + `git rebase --onto` that
  puts them back on top. `SignResult.BoxAligned` carries the outcome, which is what lets `up`
  say "GitHub has it, the box does not" in a line of its own instead of burying it.
- **The box is made able to receive the rewrite before the rewrite exists.** `alignBox` has to
  run after the push to the remote, so the two things that stop it were both discovered when
  GitHub already held the signed history, which is the two-lineage state itself.
  `checkBoxCanReceive` asks the box the same two questions in one ssh call, before the rewrite,
  and answers them differently on purpose. `receive.denyCurrentBranch` is set: git defaults it to
  `refuse`, `warn` and `ignore` take the push and leave the working tree behind, and the setting
  exists only so this tool's own push can land, which makes it sand's business and not the box's
  source. A modified tracked file is refused instead, because what becomes of the box's
  uncommitted work is not this machine's call, and it is the stop `sand status` already prints. An
  unreachable box or a missing checkout is neither: neither says anything about whether the branch
  should be signed, and `alignBox` reports both after the push with nothing lost. It runs before
  the lineage check because the recovery that check prints ends in a push to the box, so a box
  that cannot take one makes that advice useless too.
- **Every push to the box is `--no-verify`; the push to the remote is not.** A Mac that signs has
  a `pre-push` hook refusing commits it cannot verify a signature on, and git runs a hook per
  push, not per remote. That is the right gate for GitHub and wrong for the box twice over: on
  the recovery path the branch handed over is unsigned by construction, that being the point of
  handing it over, and the range the hook measures is the whole history, because there is no
  remote-tracking ref for the box to bound it against. In aperture it counted 53 commits from
  before that repo signed anything, none of them the branch's, and blocked the push. Nothing
  reaches GitHub unchecked as a result: the box has no credential that can push there, and the
  one push that does go to GitHub keeps the hook. This was the second reason no realigning push
  had ever landed for aperture, independent of `receive.denyCurrentBranch`, and it also made the
  printed recovery unrunnable, which is why that line carries the flag too.
- **A branch built on the replaced lineage is refused, before anything is rewritten.**
  `checkPreSigningLineage` keys every commit about to be signed by tree plus subject and looks
  for the same key on `<remote>/<branch>`. A hit says this commit's signed twin is already
  pushed, so signing would put a second copy of the same work on the remote. That is the state
  the realignment exists to prevent, and this is the tripwire for the runs that got past it (a
  box realigned by hand, a `--no-push` round, a branch someone rebased). It refuses by name and
  pair, before the recovery branch is made and before any push. Not a warning: the operator
  cannot see this from either machine's `git log`.
- **It checks `<remote>/<base>` as well as `<remote>/<branch>`, and the base is the worse of the
  two.** A twin on the branch stops existing when the branch is replaced. A twin on `main` is
  merged: permanent, already signed, and nothing can take it back. This repo grew seven such
  pairs, identical trees, both copies signed, one set on `main` and one on the branch, from two
  rounds signing the same box originals; nothing said a word until the two refused to merge and
  `git` called it a divergence of 7 against 13. So a duplicate of merged work is refused with a
  different fix: `git rebase <remote>/<base> <branch>`, which drops what is already upstream by
  patch id, rather than the replay that only moves it.
- **The recovery it prints runs on the Mac and ends with a push to the box**, in that order, and
  the order is not a style choice. `aif` resets this checkout to the box's branch at the top of
  every run, so a rebase done here and not pushed to the box is undone by the next `sand sign`
  before it looks at anything. The rebase itself can only happen here: `<remote>/<branch>` is a
  ref the box has never seen. So the three lines are `git rebase --onto <remote>/<branch>
  <boundary> <branch>`, a leased push of the result to the box, and `sand sign --push`. The
  boundary is computed, not left as `<old-head>`: `dirty` is oldest first, so the last commit
  with a twin is the last one already on the remote, and everything above it is the work that
  exists nowhere else.
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
- **A branch argument is checked before `aif` sees it.** `aif` takes the branch as its only
  argument, so a word that is not a branch is a word aif is free to read as one of its own
  subcommands: `sand sign push`, a slip for `--push`, pushed the Mac's HEAD to the box and only
  then failed on `git switch push`. The name has to resolve locally, on `<remote>`, or on the
  box, and an unreachable box does not count as evidence against it.
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
  sign before `comments push`, not after. `comments push` enforces that order (`docs/review.md`).
- **Tree plus subject is commit identity across a rewrite**, and there is one implementation of
  it (`identity`, `identities`). Signing changes a commit's hash and nothing else about it, so
  that pair is what survives. `push` already used it to re-point a reply's `commit:` at the
  replacement; the lineage check asks the same question in the other direction, and two copies
  of the key would eventually disagree about which one is right.
- **`--dry-run` stops before the rewrite**, so no history moves, no recovery branch is made and
  nothing is pushed, to the remote or to the box. It still runs `aif` and `git fetch`: what would be signed is not knowable
  without the branch and the base.
- `SAND_AIF` overrides the `aif` binary, which is how the tests import a branch without it.
