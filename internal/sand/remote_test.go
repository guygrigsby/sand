package sand

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSynchronizationRefusesActiveAgent(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func([]string) error
	}{
		{"comments pull", runPull}, {"ci pull", runCIPull}, {"comments push", runPush},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, ghLog := harness(t)
			captureStdout(t)
			if err := runPull(nil); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(base, "o", "r", "pr-42", "c-2043881.md")
			draft := read(t, p) + "\nActive agent draft.\n"
			if err := os.WriteFile(p, []byte(draft), 0644); err != nil {
				t.Fatal(err)
			}
			lock := agentLock(base, "r")
			if err := os.MkdirAll(filepath.Dir(lock), 0700); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			err = tc.run(nil)
			if err == nil || !strings.Contains(err.Error(), "already working") {
				t.Fatalf("want lock refusal, got %v", err)
			}
			if read(t, p) != draft {
				t.Fatal("overwrote active draft")
			}
			if strings.Contains(read(t, ghLog), "/replies") {
				t.Fatal("posted while agent held the lock")
			}
		})
	}
}

func TestPushPreservesUnrelatedCIFiles(t *testing.T) {
	remoteBase, _ := harness(t)
	captureStdout(t)
	if err := runPull(nil); err != nil {
		t.Fatal(err)
	}
	prDir := filepath.Join(remoteBase, "o", "r", "pr-42")
	p := filepath.Join(prDir, "c-2043881.md")
	if err := os.WriteFile(p, []byte(read(t, p)+"\nClosed the fd.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ci := filepath.Join(prDir, "ci")
	if err := os.MkdirAll(ci, 0o755); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(ci, "ci-build.md")
	if err := os.WriteFile(notes, []byte("old notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REVIEW_CI_PATH", notes)
	ghPath := filepath.Join(os.Getenv("HOME"), "bin", "gh")
	hook := "case \"$*\" in *\"--method POST\"*) printf 'new agent notes\\n' > \"$REVIEW_CI_PATH\";; esac\n"
	stub := strings.Replace(fakeGH, "#!/bin/sh\n", "#!/bin/sh\n"+hook, 1)
	if err := os.WriteFile(ghPath, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runPush(nil); err != nil {
		t.Fatal(err)
	}
	if got := read(t, notes); got != "new agent notes\n" {
		t.Fatalf("comments push overwrote newer CI work with its fetched snapshot: %q", got)
	}
}

func TestSynchronizationHoldsLockThroughFileTransfer(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func([]string) error
	}{
		{"comments pull", runPull}, {"ci pull", runCIPull}, {"comments push", runPush},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, _ := harness(t)
			captureStdout(t)
			if err := runPull(nil); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(base, "o", "r", "pr-42", "c-2043881.md")
			if err := os.WriteFile(p, []byte(read(t, p)+"\nClosed the fd.\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			shim := filepath.Join(t.TempDir(), "ssh")
			body := `#!/bin/sh
shift
case "$*" in
  *"tar czf"*|*"tar xzf"*)
    if flock -n "$SAND_TEST_LOCK" true; then
      echo 'file transfer ran without the agent lock' >&2
      exit 1
    fi
    ;;
esac
exec /bin/sh -c "$*"
`
			if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SAND_SSH", shim)
			t.Setenv("SAND_TEST_LOCK", agentLock(base, "r"))
			if err := tc.run(nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRemoteLockReportsLostConnection(t *testing.T) {
	base, _ := harness(t)
	lock, err := lockRemote(Config{Host: "box", RemoteDir: base}, "r")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	// EOF simulates the SSH connection disappearing between two operations.
	lock.input.Close()
	<-lock.done
	if err := lock.check(); err == nil {
		t.Fatal("accepted a lost lock")
	}
	if err := lock.Close(); err == nil {
		t.Fatal("reported success after the lock was lost")
	}
}
