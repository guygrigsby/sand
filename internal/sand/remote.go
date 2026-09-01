package sand

// Transport is ssh plus tar: one round trip per direction, no daemon on the box, and
// nothing to install there. Both ends have tar (bsdtar on the Mac, GNU tar on the box)
// and both accept `-C dir` and `-` for the archive itself.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
)

// sshBin is the ssh command to use. SAND_SSH overrides it, which is how the transport
// gets exercised in tests without a second machine.
func sshBin() string {
	if v := os.Getenv("SAND_SSH"); v != "" {
		return v
	}
	return "ssh"
}

// boxDirty is the probe line that answers whether a push can land in the box's checkout:
// tracked files with uncommitted changes, which is what receive.denyCurrentBranch=updateInstead
// refuses rather than overwrite. Untracked files are left out on purpose, a stray build artifact
// is not a reason to stop a round. Both probes share the line because `status` saying the round
// cannot finish and `sign` refusing to start one have to be the same answer.
const boxDirty = `echo "dirty=$(git status --porcelain --untracked-files=no 2>/dev/null | grep -c '')"`

// RemotePath is where a PR's files live on the box: <base>/<owner>/<repo>/pr-<n>.
func (t Target) RemotePath(base string) string {
	return path.Join(base, segment(t.Owner), segment(t.Repo), fmt.Sprintf("pr-%d", t.Number))
}

// CIPath is where a PR's failing checks live: a subdirectory of the PR's own directory, so
// one place on the box holds everything about a PR. A sibling of it would have the two halves
// of a review sharing a parent with `sendDir`, which adds and never deletes, and no way to
// tell a thread file from a check file when clearing one out.
func (t Target) CIPath(base string) string { return path.Join(t.RemotePath(base), "ci") }

var unsafeSegment = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// segment keeps repo and owner names to something that cannot mean anything to a shell.
func segment(s string) string {
	s = unsafeSegment.ReplaceAllString(s, "-")
	if s == "" || strings.HasPrefix(s, ".") {
		s = "_" + s
	}
	return s
}

// sendDir copies the contents of localDir into remoteDir on the box, creating it if
// needed. Existing remote files not present locally are left alone.
func sendDir(host, localDir, remoteDir string) error {
	q := remoteQuote(remoteDir)
	tar := exec.Command("tar", "czf", "-", "-C", localDir, ".")
	ssh := exec.Command(sshBin(), host, fmt.Sprintf("mkdir -p %s && tar xzf - -C %s", q, q))

	pipe, err := tar.StdoutPipe()
	if err != nil {
		return err
	}
	ssh.Stdin = pipe
	var sshErr, tarErr bytes.Buffer
	ssh.Stderr = &sshErr
	tar.Stderr = &tarErr

	if err := ssh.Start(); err != nil {
		return fmt.Errorf("ssh %s: %w", host, err)
	}
	if err := tar.Run(); err != nil {
		return fmt.Errorf("tar %s: %w (%s)", localDir, err, strings.TrimSpace(tarErr.String()))
	}
	pipe.Close()
	if err := ssh.Wait(); err != nil {
		return fmt.Errorf("ssh %s: %w (%s)", host, err, strings.TrimSpace(sshErr.String()))
	}
	return nil
}

// fetchDir copies remoteDir's contents into a fresh local temp dir. A missing remote dir
// is not an error: the answer is simply an empty directory, which is what a first pull
// wants to see.
func fetchDir(host, remoteDir string) (string, error) {
	localDir, err := os.MkdirTemp("", "sand-fetch-")
	if err != nil {
		return "", err
	}

	q := remoteQuote(remoteDir)
	ssh := exec.Command(sshBin(), host, fmt.Sprintf("cd %s 2>/dev/null && tar czf - . || true", q))
	var archive, stderr bytes.Buffer
	ssh.Stdout = &archive
	ssh.Stderr = &stderr
	if err := ssh.Run(); err != nil {
		os.RemoveAll(localDir)
		return "", fmt.Errorf("ssh %s: %w (%s)", host, err, strings.TrimSpace(stderr.String()))
	}
	if archive.Len() == 0 {
		return localDir, nil // nothing there yet
	}

	untar := exec.Command("tar", "xzf", "-", "-C", localDir)
	untar.Stdin = &archive
	untar.Stderr = &stderr
	if err := untar.Run(); err != nil {
		os.RemoveAll(localDir)
		return "", fmt.Errorf("unpacking %s from %s: %w (%s)", remoteDir, host, err, strings.TrimSpace(stderr.String()))
	}
	return localDir, nil
}

// remoteQuote makes a path safe for the remote shell while still letting a leading ~
// expand, which is the whole point of writing remote_dir as ~/.sand.
func remoteQuote(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return "~/" + shellQuote(rest)
	}
	return shellQuote(p)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
