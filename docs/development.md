# Working on it

`make check` is the gate: `go vet`, `gofmt`, tests, a linux build and the darwin/arm64 build.

`bench_test.go` holds the benchmarks, which the gate does not run (`go test ./internal/sand/
-run xxx -bench .` does). They all measure the same thing: reading a branch. Every way of
getting that wrong is one `git` process per commit, which is invisible on the three-commit
branches the tests use and linear in the real ones, so the benchmarks build a 60-commit branch
and the numbers in `docs/signing.md` come from them.

There is no `gh` and no GitHub API token on the box, and its git credential is read only, so the
tests fake both ends: a `gh` stub on `PATH` and `SAND_SSH` pointed at a shim that runs the "remote" command
locally (`internal/sand/e2e_test.go`). That covers the GraphQL decode, the file format, tar in
both directions, the merge on re-pull, the reply POST and the sent marking. What it cannot cover
is GitHub itself: claims about real API behaviour need a run on the Mac against a real PR.

The shim earns its keep twice over on the remote skill install, where the decisions are in shell
on the box rather than in Go here: with `HOME` pointed at a temp directory, `skill_test.go` runs
the real script against a real filesystem, and the fake agent in the e2e test reads the skill
through the harness's own path, which is the assertion that a pull installs it ahead of the
agent rather than after it.

`install.sh` is tested too (`internal/sand/install_test.go`), by serving a fake release over
localhost through `SAND_BASE_URL` and running the real script against it. Everything a coworker
depends on is after the download: that the platform mapping names an asset, that the file lands
executable, and that a failed download does not overwrite a working binary. The script also
installs the skill when it finds a harness on the machine it is running on, which is a
convenience for a machine that has the binary anyway, not the way the box gets the skill: that
is ssh, from the Mac, on every pull. The one thing the test cannot say is that the GitHub URL
resolves, which only a real release proves.

## Releases

    make release VERSION=v0.1.0

Runs the gate, then `git tag -s` and pushes the tag. Everything after that is
`.github/workflows/release.yml`: the gate again, `make dist` for darwin and linux on both arches,
and `gh release create` attaching the four binaries that `install.sh` downloads.

Three decisions worth keeping:

- **The tag is what triggers it, and the build happens on GitHub.** What ships is then the tag as
  GitHub sees it, not whatever the Mac's working tree held. A `gh release create` run by hand from
  the Mac would upload a build nobody can reproduce from the tag.
- **Only the Mac can cut one**, and by construction rather than by a check: `release` ends in a
  push, and the box has no credential that can push. Same reason as the rest of the ring.
- **The tag is signed** (`-s`), because every commit this tool produces is, and the key is on the
  Mac. The workflow does not verify the signature: a GitHub checkout has no allowed-signers file,
  so `git verify-tag` cannot succeed there. The signature is for GitHub's UI and for a human.

`VERSION` is also the only thing that stamps a version into a binary
(`-X .../internal/sand.version`). Every other build reads its own: Go embeds a pseudo-version off
the revision for `go build` in a checkout, with `+dirty` when the tree is modified, so `sand
--version` after `make install` on the box already says what it was built from.

CI is `.github/workflows/ci.yml`, on every push and pull request, and it calls `make check`
rather than restating what checking means. It also means `sand ci pull` finally has checks to
pull for this repo.
