package sand

// The benchmarks exist for one reason: every one of these reads a branch, and the way to read a
// branch wrongly is one `git` process per commit. `go test` does not run them, so they cost the
// gate nothing; `go test -bench . -run xxx ./internal/sand/` is the measurement.

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

// benchBranch is a branch of n commits over a pushed main, which is the shape signing and
// status both read. Built once per benchmark, outside the timed loop.
func benchBranch(b *testing.B, n int) (dir string, g gitCmd) {
	b.Helper()
	dir, _ = signRepo(b)
	for i := range n {
		commit(b, dir, fmt.Sprintf("f%d.txt", i), fmt.Sprintf("line %d\n", i),
			fmt.Sprintf("feature: commit %d", i))
	}
	return dir, gitCmd{out: io.Discard}
}

func BenchmarkBranchCommits(b *testing.B) {
	_, g := benchBranch(b, 60)
	for b.Loop() {
		commits, err := g.branchCommits("feature", "origin/main")
		if err != nil {
			b.Fatal(err)
		}
		if len(commits) < 60 {
			b.Fatalf("read %d commits", len(commits))
		}
	}
}

func BenchmarkDuplicatedOnRemote(b *testing.B) {
	_, g := benchBranch(b, 60)
	commits, err := g.branchCommits("feature", "origin/main")
	if err != nil {
		b.Fatal(err)
	}
	dirty, _ := splitBySigning(commits)
	for b.Loop() {
		if _, err := duplicatedOnRemote(g, dirty, "origin/feature", "origin/main"); err != nil {
			b.Fatal(err)
		}
	}
}

// One `commit:` lookup per reply is what `comments push` does, and it does it after signing has
// replaced those hashes, so the recorded ones are not on the branch and every lookup has to
// match by tree and subject. That is the path that reads the branch; the two benchmarks are the
// same work with the read hoisted out of the loop and left in it, which is what was fixed.
func BenchmarkReplyLookupsAfterSigning(b *testing.B) {
	recorded, g := benchReplies(b, 10)
	for b.Loop() {
		branch := newBranchIndex(g, "origin/feature")
		for _, sha := range recorded {
			if _, state := commitOnBranch(g, sha, branch); state != commitGone {
				b.Fatalf("%s: state %v", sha, state)
			}
		}
	}
}

func BenchmarkReplyLookupsRereadingTheBranch(b *testing.B) {
	recorded, g := benchReplies(b, 10)
	for b.Loop() {
		for _, sha := range recorded {
			if _, state := commitOnBranch(g, sha, newBranchIndex(g, "origin/feature")); state != commitGone {
				b.Fatalf("%s: state %v", sha, state)
			}
		}
	}
}

// benchReplies is the state `push` runs in: a pushed branch, and n hashes an agent recorded
// that are no longer on it.
func benchReplies(b *testing.B, n int) ([]string, gitCmd) {
	dir, g := benchBranch(b, 60)
	mustRun(b, dir, "git", "push", "--quiet", "--force", "origin", "feature")
	mustRun(b, dir, "git", "switch", "--quiet", "-c", "recorded")
	for i := range n {
		commit(b, dir, fmt.Sprintf("r%d.txt", i), fmt.Sprintf("reply %d\n", i),
			fmt.Sprintf("fix: thread %d", i))
	}
	recorded := strings.Fields(mustRun(b, dir, "git", "log", "--format=%h", "-"+fmt.Sprint(n), "recorded"))
	mustRun(b, dir, "git", "switch", "--quiet", "feature")
	return recorded, g
}
