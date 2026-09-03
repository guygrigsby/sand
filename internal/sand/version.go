package sand

import "runtime/debug"

// version is stamped by the release build:
//
//	-ldflags "-X github.com/guygrigsby/sand/internal/sand.version=v0.1.0"
//
// Empty in every other build, which is the normal case and not an error: what a local build was
// made from is already in the binary, so Version falls back to reading it rather than making the
// Makefile stamp something it would have to invent.
var version string

// Version is what `sand --version` prints, and it exists because two machines have to agree
// about one binary. The skill installed on the box is text compiled into a `sand`, so "the skill
// disagrees with the tool" can only be diagnosed if both ends can be named; `skill install`
// prints this, and so does the flag.
//
// A tagged release says `v0.1.0`. Anything else says what it was built from, because a coworker
// running a binary they built from a branch is the case where the question actually gets asked.
func Version() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	// Go fills this in on its own: the tag for `go install ...@v0.1.0`, and for a plain
	// `go build` in a checkout a pseudo-version off the revision, with its own `+dirty` when
	// the tree is modified. So `make install` on the box already says what it was built from
	// and there is nothing here to compute. `go run` is the one that says nothing.
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return "dev"
}
