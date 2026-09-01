# The ring: two machines, one direction of authority

| | Mac | sandbox (whatever `sand config get host` says) |
|---|---|---|
| runs `sand` | yes | no |
| `gh`, the GitHub API | yes | no |
| `git pull` from GitHub | no | yes, read only |
| `git push` to GitHub | yes | no |
| merges | no | yes |
| edits code | no | yes |
| canonical source of `sand` | no, a build copy | yes, this repo |

Git flows one way around a ring, and every step is the only machine that can do its step:

    GitHub --pull--> box --make sync (fetch, --ff-only)--> Mac --push--> GitHub
                      ^                                    |
                      +------ sand sign, after the push ----+

The box edits, pulls and merges. The Mac fast forwards, signs and pushes, and never pulls from
GitHub and never merges: a merge or a pull there is a second place history can be decided, and
then the two copies diverge in a way only a human can untangle. The reason the Mac holds the
GitHub push and the box does not is the signing key: what gets pushed has to be signed, and only
the Mac can sign.

**Signing is not a forward move, and that is what the return arrow is for.** A signature is part
of a commit's bytes, so adding one cannot amend history in place: `sand sign` re-creates every
commit it touches with a new hash. The chain the box is sitting on stops existing at that moment.
The box then commits on top of a dead lineage, and the next round arrives with unsigned copies of
commits that are already signed and pushed: two chains, identical trees, different hashes,
invisible in either machine's `git log` unless someone compares tree ids by hand. That happened,
and it cost an afternoon.

The fix is that the rewrite goes back where the code is written, in the same command that made
it. `sand sign` pushes the signed branch to the box after the push to GitHub succeeds (see
`alignBox`). The box does not pull it from GitHub, for one reason per repo and both of them
permanent enough: this repo has no read-only credential on the box at all, and no secret is going
on that box to give it one; aperture can pull from GitHub, but making the realignment depend on
which repo it is means two code paths where one works. So the Mac pushes, always, and every repo
behaves the same.

The box needs one setting for that push to land, once per checkout:

    git config receive.denyCurrentBranch updateInstead

Without it git refuses a push into the branch that is checked out. With it, the push updates the
working tree too, and it refuses when that tree is dirty, which is the behaviour worth having: it
cannot land on top of work the box has not committed.

**`sand sign` sets it, and nobody has to remember it.** It used to be a manual step, and the cost
of skipping it was invisible for a whole round: aperture's checkout never had it, so every
realigning push since `alignBox` existed was rejected, said so in one line at the tail of a long
signing run, and left GitHub holding signed history the box had never seen. Sixteen commits and
three refused rounds later that is what it cost. See `checkBoxCanReceive`.

The repo is [guygrigsby/sand](https://github.com/guygrigsby/sand), private, default branch
`main`. The box needs a read-only credential for it to pull, and no more than that: no `gh`, no
API token, no push. So the box's work still reaches the Mac directly rather than through GitHub:

- On the Mac, in its copy: `make sync`, which fetches the branch it is on from this box, fast
  forwards to it and installs. No argument needed: `BOX` comes from `sand config get host`, i.e.
  the same host the tool itself uses, so `sand config set host <alias>` moves both.
  `BOX=<alias>` and `SAND_HOST` still override.
- From here, if the Mac accepts ssh: `make ship MAC=user@mac`. That one is still an rsync,
  because its target is `src/sand`, a build copy and not a checkout. No default: the Mac's
  address is not in sand's config and guessing it would point an rsync `--delete` at whatever
  answered.

`sync` fetches rather than rsyncs because the Mac's copy is a git checkout, and rsync fought it
from both ends. With `.git` included, `--delete` replaced the Mac's git state with the box's,
which has no remotes, so it deleted the `origin` that `gh repo create` had just made, along with
the pre-push hook, the signing config and the reflog. With `.git` excluded, it laid the box's
working tree over whatever commit the Mac was on, so everything committed on the box since read
as an uncommitted Mac-side change, and nothing could be checked out or signed until those were
thrown away. Committed history is the only thing the Mac has any use for: it signs and pushes,
and an uncommitted file has nothing to sign.

The fetch names the box by URL rather than through a named remote, since `BOX` already names it
and a remote is one more thing to keep pointed at the same place. `--ff-only` is the ring written
down: a Mac that cannot fast forward is a Mac being asked to merge, which is the box's job, and a
Mac-side branch that has moved on its own has moved for one reason, signing, which the box picks
up on its next pull rather than losing to a rewrite. A dirty tree stops it for the same reason. A
fresh Mac starts with a `git clone`, not a sync.

The `BOX` lookup prefers `$(GO) run . config get host` from the checkout over an installed
`sand`: `make sync` is what installs the new binary, so the installed one is by definition the
old one, and an older one with no `config get` answers with the whole config file. It falls back
to the installed `sand config get host` when the checkout does not compile, because the checkout
is also what sync fast forwards: a Mac copy sitting on a commit that does not build cannot answer
where the box is, and that is exactly when someone runs sync. The recipe refuses a `BOX` that is
empty or more than one word rather than pasting it into a git URL, which also catches an
installed `sand` too old to know `config get`, and says to pass `BOX=<alias>`. It is expanded
once, inside the `sync` recipe, so `make build` and `make check` never pay for the compile and
sync does not pay for it three times.

If the tailnet refuses the Mac's local username (`tailnet policy does not permit you to SSH as
user "<local-user>"`), the login user belongs in the Mac's `~/.ssh/config`, or in the host itself
as `sand config set host ubuntu@<box>`. Not in the source: a machine-specific login is not a
thing to hardcode for every machine.
