package sand

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreePullUsesSandboxBranchAndDirectory(t *testing.T) {
	for _, command := range []string{"comments", "ci"} {
		t.Run(command, func(t *testing.T) {
			dir, _ := signRepo(t)
			base, log := harness(t)
			box := boxWorkingCheckout(t, dir, "feature")
			worktree := filepath.Join(t.TempDir(), "topic's worktree")
			mustRun(t, box, "git", "worktree", "add", "-b", "topic", worktree)
			agent := filepath.Join(t.TempDir(), "agent")
			cwd := filepath.Join(t.TempDir(), "cwd")
			t.Setenv("WORKTREE_CWD", cwd)
			if err := os.WriteFile(agent, []byte("#!/bin/sh\npwd > \"$WORKTREE_CWD\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			c := root()
			c.SetArgs([]string{command, "pull", "--host", "box", "--remote-dir", base, "--worktree", worktree, "--agent", agent})
			if err := c.Execute(); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(read(t, cwd)); got != worktree {
				t.Fatalf("agent ran in %q, want %q", got, worktree)
			}
			if got := read(t, log); !strings.Contains(got, "--head\ntopic\n") || strings.Contains(got, "--head\nfeature\n") {
				t.Fatalf("PR lookup did not use worktree branch:\n%s", got)
			}
			if currentBranch() != "feature" || mustRun(t, box, "git", "branch", "--show-current") != "feature" {
				t.Fatal("pull switched a primary checkout")
			}
		})
	}
}

func TestWorktreeRefusesConflictingPR(t *testing.T) {
	for _, args := range [][]string{{"comments", "pull"}, {"comments", "push"}, {"ci", "pull"}, {"up"}, {"push"}, {"pr", "review"}, {"status"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir, _ := signRepo(t)
			base, log := harness(t)
			box := boxWorkingCheckout(t, dir, "feature")
			c := root()
			c.SetArgs(append(args, "42", "--host", "box", "--remote-dir", base, "--worktree", box))
			err := c.Execute()
			if err == nil || !strings.Contains(err.Error(), "worktree") || !strings.Contains(err.Error(), "topic") {
				t.Fatalf("want conflicting PR refusal, got %v", err)
			}
			if _, err := os.Stat(filepath.Join(base, "o", "r", "pr-42")); !os.IsNotExist(err) {
				t.Fatal("conflicting PR wrote review files")
			}
			if got := read(t, log); strings.Contains(got, "--method\nPOST\n") {
				t.Fatal("conflicting PR posted to GitHub")
			}
		})
	}
}

func TestWorktreeSignReturnsHistoryToSelectedCheckout(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(map[bool]string{false: "temporary local worktree", true: "existing local worktree"}[local], func(t *testing.T) {
			testWorktreeSign(t, local)
		})
	}
}

func testWorktreeSign(t *testing.T, local bool) {
	t.Helper()
	dir, remote := signRepo(t)
	harness(t)
	box := boxWorkingCheckout(t, dir, "feature")
	worktree := filepath.Join(t.TempDir(), "topic's worktree")
	mustRun(t, box, "git", "worktree", "add", "-b", "topic", worktree)
	localWorktree := ""
	if local {
		localWorktree = filepath.Join(t.TempDir(), "local topic")
		mustRun(t, dir, "git", "worktree", "add", "-b", "topic", localWorktree)
	}
	localBefore := mustRun(t, dir, "git", "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("local edits\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{{"user.name", "Box Agent"}, {"user.email", "sand@example.invalid"}} {
		mustRun(t, box, "git", "config", kv[0], kv[1])
	}
	commit(t, worktree, "box.txt", "from box\n", "topic: from box")
	mustRun(t, dir, "git", "config", "url."+worktree+".insteadOf", "box:"+worktree)
	before := mustRun(t, box, "git", "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(box, "a.txt"), []byte("box edits\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := root()
	c.SetArgs([]string{"sign", "--worktree", worktree, "--host", "box", "--yes", "--push"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	head := mustRun(t, dir, "git", "rev-parse", "topic")
	if got := mustRun(t, remote, "git", "rev-parse", "topic"); got != head {
		t.Fatal("remote did not receive worktree branch")
	}
	if got := mustRun(t, worktree, "git", "rev-parse", "HEAD"); got != head {
		t.Fatal("sandbox worktree did not receive signed HEAD")
	}
	if got := mustRun(t, worktree, "git", "status", "--porcelain"); got != "" {
		t.Fatalf("sandbox worktree index was left behind: %s", got)
	}
	if mustRun(t, box, "git", "rev-parse", "HEAD") != before || read(t, filepath.Join(box, "a.txt")) != "box edits\n" {
		t.Fatal("sandbox primary checkout changed")
	}
	if currentBranch() != "feature" || mustRun(t, dir, "git", "rev-parse", "HEAD") != localBefore || read(t, filepath.Join(dir, "a.txt")) != "local edits\n" {
		t.Fatal("local primary checkout changed")
	}
	if local && (mustRun(t, localWorktree, "git", "rev-parse", "HEAD") != head || read(t, filepath.Join(localWorktree, "box.txt")) != "from box\n") {
		t.Fatal("existing local worktree did not receive the imported and signed commits")
	}
	wantWorktrees := 1
	if local {
		wantWorktrees++
	}
	if got := strings.Count(mustRun(t, dir, "git", "worktree", "list", "--porcelain"), "worktree "); got != wantWorktrees {
		t.Fatalf("temporary worktree was not removed: %d worktrees remain", got)
	}
	if got := mustRun(t, worktree, "git", "cat-file", "commit", "HEAD"); !strings.Contains(got, "gpgsig ") {
		t.Fatal("worktree HEAD was not signed")
	}
}

func TestWorktreeRejectsInvalidSelection(t *testing.T) {
	for _, kind := range []string{"empty", "missing", "detached", "branch conflict", "repo-dir conflict"} {
		t.Run(kind, func(t *testing.T) {
			dir, _ := signRepo(t)
			harness(t)
			box := boxWorkingCheckout(t, dir, "feature")
			selector := box
			args := []string{"sign"}
			want := "worktree"
			switch kind {
			case "empty":
				selector = ""
			case "missing":
				selector = filepath.Join(box, "missing")
			case "detached":
				mustRun(t, box, "git", "checkout", "--detach")
			case "branch conflict":
				args = append(args, "main")
			case "repo-dir conflict":
				args = []string{"comments", "pull", "--repo-dir", box}
			}
			c := root()
			c.SetArgs(append(args, "--host", "box", "--worktree", selector))
			err := c.Execute()
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want refusal for %s, got %v", kind, err)
			}
			if currentBranch() != "feature" {
				t.Fatal("invalid worktree changed the local checkout")
			}
			if selectedWorktree != nil {
				t.Fatal("worktree selection leaked after failure")
			}
		})
	}
}

func TestWorktreePushKeepsSelectedPRAfterSigning(t *testing.T) {
	for _, args := range [][]string{{"up", "--yes"}, {"push", "--yes"}, {"comments", "push"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir, remote := signRepo(t)
			base, log := harness(t)
			box := boxWorkingCheckout(t, dir, "feature")
			worktree := filepath.Join(t.TempDir(), "topic")
			mustRun(t, box, "git", "worktree", "add", "-b", "topic", worktree)
			mustRun(t, dir, "git", "config", "url."+worktree+".insteadOf", "box:"+worktree)
			common := []string{"--host", "box", "--remote-dir", base, "--worktree", worktree}
			pull := root()
			pull.SetArgs(append([]string{"comments", "pull", "--no-agent"}, common...))
			if err := pull.Execute(); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(base, "o", "r", "pr-42", "c-2043881.md")
			if err := os.WriteFile(file, []byte(read(t, file)+"\nFixed in the selected worktree.\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			c := root()
			c.SetArgs(append(args, common...))
			if err := c.Execute(); err != nil {
				t.Fatal(err)
			}
			if got := read(t, log); !strings.Contains(got, "repos/o/r/pulls/42/comments/2043881/replies") || strings.Contains(got, "--head\nfeature\n") {
				t.Fatalf("wrong PR for reply:\n%s", got)
			}
			if !strings.Contains(read(t, file), "status: sent") {
				t.Fatal("reply was not marked sent")
			}
			if args[0] != "comments" && mustRun(t, remote, "git", "rev-parse", "topic") != mustRun(t, worktree, "git", "rev-parse", "HEAD") {
				t.Fatal("push did not publish and realign selected branch")
			}
			if currentBranch() != "feature" {
				t.Fatal("push switched the primary checkout")
			}
		})
	}
}

func TestWorktreePRCreateUsesSandboxBranch(t *testing.T) {
	dir, _ := signRepo(t)
	base, log := harness(t)
	box := boxWorkingCheckout(t, dir, "feature")
	worktree := filepath.Join(os.Getenv("HOME"), "topic")
	mustRun(t, box, "git", "worktree", "add", "-b", "topic", worktree)
	ssh := filepath.Join(t.TempDir(), "ssh-login-home")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nshift\ncd \"$HOME\" || exit 1\nexec /bin/sh -c \"$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SAND_SSH", ssh)
	t.Setenv("GH_PR_MISSING", "1")
	for _, selector := range []string{"~/topic", "topic"} {
		c := root()
		c.SetArgs([]string{"pr", "create", "--host", "box", "--remote-dir", base, "--worktree", selector, "--dry-run"})
		if err := c.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if got := read(t, log); !strings.Contains(got, "--head\ntopic\n") || strings.Contains(got, "--head\nfeature\n") {
		t.Fatalf("PR create did not query worktree branch:\n%s", got)
	}
	if _, err := os.Stat(os.Getenv("GH_CREATED")); !os.IsNotExist(err) {
		t.Fatal("dry run created a PR")
	}
}
