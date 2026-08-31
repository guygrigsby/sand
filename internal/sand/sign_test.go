package sand

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// signRepo builds a repository that looks like the Mac's after a sandbox branch landed: a
// pushed main, a feature branch with unsigned commits and a merge commit, an ssh signing
// key, and an aif that has nothing left to do because the branch is already here.
func signRepo(t *testing.T) (dir, remote string) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available, cannot sign")
	}
	root := t.TempDir()
	dir = filepath.Join(root, "repo")
	remote = filepath.Join(root, "remote.git")
	key := filepath.Join(root, "id")

	mustRun(t, root, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "sand-test", "-f", key)
	mustRun(t, root, "git", "init", "--quiet", "--bare", "--initial-branch=main", remote)
	mustRun(t, root, "git", "init", "--quiet", "--initial-branch=main", dir)

	for _, kv := range [][2]string{
		{"user.name", "Sand Test"},
		{"user.email", "sand@example.invalid"},
		{"gpg.format", "ssh"},
		{"user.signingkey", key + ".pub"},
		{"commit.gpgsign", "false"}, // the box cannot sign, so nothing arrives signed
	} {
		mustRun(t, dir, "git", "config", kv[0], kv[1])
	}
	mustRun(t, dir, "git", "remote", "add", "origin", remote)

	commit(t, dir, "base.txt", "base\n", "main: base")
	mustRun(t, dir, "git", "push", "--quiet", "-u", "origin", "main")

	// Feature branch: a commit, a side branch, and a merge, so the rewrite has a topology
	// to preserve rather than a straight line.
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "feature")
	commit(t, dir, "a.txt", "a\n", "feature: a")
	mustRun(t, dir, "git", "switch", "--quiet", "-c", "feature-side")
	commit(t, dir, "b.txt", "b\n", "feature: b")
	mustRun(t, dir, "git", "switch", "--quiet", "feature")
	mustRun(t, dir, "git", "merge", "--quiet", "--no-ff", "-m", "feature: merge side", "feature-side")

	// aif is required and must be found; here the branch is already local, so a stub that
	// does nothing is the honest fake.
	aif := filepath.Join(root, "aif")
	if err := os.WriteFile(aif, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAND_AIF", aif)
	t.Chdir(dir)
	return dir, remote
}

func mustRun(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	c := exec.Command(name, args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file, body, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "add", file)
	mustRun(t, dir, "git", "commit", "--quiet", "-m", message)
}

func signOpts(out *strings.Builder, answer string) SignOpts {
	return SignOpts{Remote: "origin", Base: "main", Yes: true, In: strings.NewReader(answer), Out: out}
}

func TestSignSignsEveryBranchCommitAndKeepsTheMerge(t *testing.T) {
	dir, remote := signRepo(t)
	before := mustRun(t, dir, "git", "log", "--format=%P %s", "feature", "--not", "origin/main")
	var out strings.Builder

	// "n" declines the push, so this asserts the rewrite alone.
	if err := Sign(signOpts(&out, "n\n")); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	shas := strings.Fields(mustRun(t, dir, "git", "rev-list", "feature", "--not", "origin/main"))
	if len(shas) != 3 {
		t.Fatalf("signed %d commits, want 3\n%s", len(shas), out.String())
	}
	for _, sha := range shas {
		if raw := mustRun(t, dir, "git", "cat-file", "commit", sha); !hasSignature(raw) {
			t.Errorf("%s is not signed", sha)
		}
	}

	// Same shape, same messages, same parent counts: only the signatures are new.
	if after := mustRun(t, dir, "git", "log", "--format=%P %s", "feature", "--not", "origin/main"); countParents(after) != countParents(before) {
		t.Errorf("topology changed:\nbefore\n%s\nafter\n%s", before, after)
	}
	if merges := mustRun(t, dir, "git", "rev-list", "--merges", "feature", "--not", "origin/main"); len(strings.Fields(merges)) != 1 {
		t.Errorf("merge commit did not survive, --merges gave %q", merges)
	}

	if backups := mustRun(t, dir, "git", "branch", "--list", "feature-before-signing-*"); backups == "" {
		t.Error("no recovery branch left behind")
	}
	if refs := mustRun(t, dir, "git", "--git-dir", remote, "branch", "--list", "feature"); refs != "" {
		t.Errorf("declining the push still pushed: %q", refs)
	}
	if !strings.Contains(out.String(), "Verified: all 3") {
		t.Errorf("output did not report the verification:\n%s", out.String())
	}
}

func TestSignPushesWhenAsked(t *testing.T) {
	dir, remote := signRepo(t)
	var out strings.Builder

	o := signOpts(&out, "")
	o.Push = true
	if err := Sign(o); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	local := mustRun(t, dir, "git", "rev-parse", "feature")
	pushed := mustRun(t, dir, "git", "--git-dir", remote, "rev-parse", "feature")
	if local != pushed {
		t.Fatalf("remote at %s, local at %s", pushed, local)
	}
}

// Both answers come from one stdin, so the reader behind the first prompt must not swallow
// the second: piping "y\ny\n" has to sign and push.
func TestSignAnswersBothPromptsFromOneStdin(t *testing.T) {
	dir, remote := signRepo(t)
	var out strings.Builder
	o := signOpts(&out, "y\ny\n")
	o.Yes = false

	if err := Sign(o); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	local := mustRun(t, dir, "git", "rev-parse", "feature")
	pushed := mustRun(t, dir, "git", "--git-dir", remote, "rev-parse", "feature")
	if local != pushed {
		t.Fatalf("second answer lost: remote at %q, local at %s\n%s", pushed, local, out.String())
	}
}

func TestSignRefusals(t *testing.T) {
	t.Run("protected branch", func(t *testing.T) {
		dir, _ := signRepo(t)
		mustRun(t, dir, "git", "switch", "--quiet", "main")
		var out strings.Builder
		err := Sign(signOpts(&out, ""))
		if err == nil || !strings.Contains(err.Error(), "protected") {
			t.Fatalf("err = %v, want a refusal to rewrite main", err)
		}
	})

	t.Run("dirty tree", func(t *testing.T) {
		dir, _ := signRepo(t)
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("edited\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		err := Sign(signOpts(&out, ""))
		if err == nil || !strings.Contains(err.Error(), "uncommitted") {
			t.Fatalf("err = %v, want a refusal on a dirty tree", err)
		}
	})

	t.Run("nothing to sign", func(t *testing.T) {
		dir, _ := signRepo(t)
		mustRun(t, dir, "git", "switch", "--quiet", "-c", "empty", "origin/main")
		var out strings.Builder
		if err := Sign(signOpts(&out, "")); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "nothing to sign") {
			t.Errorf("output was %q", out.String())
		}
		if backups := mustRun(t, dir, "git", "branch", "--list", "empty-before-signing-*"); backups != "" {
			t.Errorf("made a recovery branch for nothing: %q", backups)
		}
	})

	t.Run("missing aif", func(t *testing.T) {
		signRepo(t)
		t.Setenv("SAND_AIF", filepath.Join(t.TempDir(), "not-installed"))
		var out strings.Builder
		err := Sign(signOpts(&out, ""))
		if err == nil || !strings.Contains(err.Error(), "not on PATH") {
			t.Fatalf("err = %v, want a stop on the missing import tool", err)
		}
	})

	t.Run("declining the rewrite changes nothing", func(t *testing.T) {
		dir, _ := signRepo(t)
		before := mustRun(t, dir, "git", "rev-parse", "feature")
		var out strings.Builder
		o := signOpts(&out, "no\n")
		o.Yes = false
		if err := Sign(o); err != nil {
			t.Fatal(err)
		}
		if after := mustRun(t, dir, "git", "rev-parse", "feature"); after != before {
			t.Errorf("branch moved from %s to %s after declining", before, after)
		}
		if !strings.Contains(out.String(), "Cancelled") {
			t.Errorf("output was %q", out.String())
		}
	})
}

func TestHasSignatureIgnoresTheMessage(t *testing.T) {
	commit := "tree abc\nauthor A <a@b> 1 +0000\ncommitter A <a@b> 1 +0000\n\ngpgsig this is prose\n"
	if hasSignature(commit) {
		t.Error("a message mentioning gpgsig counted as a signature")
	}
	if !hasSignature("tree abc\ngpgsig -----BEGIN SSH SIGNATURE-----\n\nmessage\n") {
		t.Error("a real header was missed")
	}
}

// countParents summarises "%P %s" as "<parent count> <subject>" per commit: the shape a
// rebase would flatten and commit-tree preserves, with the rewritten hashes dropped.
func countParents(log string) string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		fields := strings.Fields(line)
		n := 0
		for _, f := range fields {
			if len(f) == 40 && !strings.ContainsAny(f, " :") && isHex(f) {
				n++
				continue
			}
			break
		}
		lines = append(lines, fmt.Sprintf("%d %s", n, strings.Join(fields[n:], " ")))
	}
	return strings.Join(lines, "\n")
}

func isHex(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
