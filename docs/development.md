# Working on it

`make check` is the gate: `go vet`, `gofmt`, tests, a linux build and the darwin/arm64 build.

There is no `gh` and no GitHub API token on the box, and its git credential is read only, so the
tests fake both ends: a `gh` stub on `PATH` and `SAND_SSH` pointed at a shim that runs the "remote" command
locally (`internal/sand/e2e_test.go`). That covers the GraphQL decode, the file format, tar in
both directions, the merge on re-pull, the reply POST and the sent marking. What it cannot cover
is GitHub itself: claims about real API behaviour need a run on the Mac against a real PR.
