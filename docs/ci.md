# Failing CI: `sand ci pull [pr]`

The same shape as `comments pull`, for the other thing that makes a PR sit: the Mac can see what
failed and only the box can fix it. `ci pull` writes one markdown file per failing check to
`<remote_dir>/<owner>/<repo>/pr-<n>/ci/`, plus `index.md`, starts an agent on the box under the
same lock, and reads the files back afterwards. Flags mirror `comments pull`, plus `--log-lines`
and `--all`.

- **There is no `ci push`, and that is the design, not a missing half.** A review thread is a
  conversation and a reply belongs on it. A red check is not: the answer to it is a commit, which
  leaves the box the way every other commit does, through `sand up`, and CI running again on the
  pushed head is the evidence. A comment saying "fixed" while the check is still red is worse
  than silence. So the whole feature only reads from GitHub, which is also why it needs nothing
  from the box's credential.
- **`gh pr checks` exits non-zero on exactly the case this command is for:** 1 when a check has
  failed, 8 while any are pending. `ghJSON` returns on any error, so it cannot read this one;
  `FetchChecks` reads stdout and only reports the error when there is no JSON to decode. A run
  against a real red PR is the only way to confirm that, since the box has no `gh`.
- **The log is the tail, and the file says how much was cut.** `gh run view --log-failed` on a
  real job runs to megabytes of setup noise with the reason at the end. `--log-lines` (300)
  bounds it, a second cap bounds the bytes (one base64 line beats a line-only cap), and the count
  of dropped lines is printed in the file so nobody reasons from a log they think is whole.
- **A check that is not a GitHub Actions run gets its link and no log.** Buildkite (aperture has
  both) posts a legacy commit status; the Mac has no client for it and guessing at one would be
  inventing a second integration. `actionsRun()` reads the run id out of `/actions/runs/<id>` and
  everything else is link-only, with the file saying why.
- **Files are keyed by check name, not by run id.** The same check failing again next round has
  to land on the same file or the notes from this round have nothing to merge onto.
- **A check that goes green keeps its file, refreshed.** `sendDir` adds and never deletes, so the
  alternative is a file on the box still saying `bucket: fail` with a superseded log, which is
  what an agent would read next. Its notes and `commit:` survive, its bucket becomes what GitHub
  now says, and the index lists it under "not failing any more".
- **`## notes`, `commit:` and `status:` belong to the box**, merged forward exactly as the reply
  slot is. `status: fixed` is a claim, not a verdict: nothing here can verify it, the next CI run
  does.
- **The notes slot is parsed from the end of the file, unlike a reply.** A comment body is
  blockquoted and a diff line is prefixed, so neither can forge `## reply`. A log is verbatim: a
  build that prints `## notes` at the start of a line would take the slot and hand the rest of
  the log back as the agent's notes. `lastSectionAfter` costs a notes body that quotes its own
  heading, which is the rarer of the two.
