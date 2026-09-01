package sand

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// shotDir is where screenshots land on the box, under the same base dir as everything else
// this tool writes there. One tree per config, so `remote_dir` still moves all of it.
const shotDir = "shots"

// Shot captures the screen and puts the image on the box, because that is the direction the
// picture has to travel: the screen is on the Mac and the agent that needs to look at it is
// not. The box path is what goes to the clipboard, in the form an agent running there can
// open, which is the whole point of the command over an scp by hand.
//
// A path argument skips the capture and sends that file instead. Same trip, and it is what
// makes this testable without a window server.
func Shot(cfg Config, file string, dryRun bool, out io.Writer) error {
	// Milliseconds, not seconds: the name is the only thing keeping two shots apart, sendDir
	// overwrites by name, and two in one second is a loop over a directory rather than two
	// crops by hand.
	name := fmt.Sprintf("ss-%s.png", strings.ReplaceAll(time.Now().Format("2006-01-02-150405.000"), ".", ""))
	if file != "" {
		// Keep the extension the file actually has: an agent opening it goes by name, and a
		// .png that is a jpeg is a worse lie than a timestamped .jpg.
		if ext := filepath.Ext(file); ext != "" {
			name = strings.TrimSuffix(name, ".png") + ext
		}
	}

	dir, err := os.MkdirTemp("", "sand-shot-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	local := filepath.Join(dir, name)

	switch file {
	case "":
		if err := capture(local); err != nil {
			return err
		}
	default:
		b, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if err := os.WriteFile(local, b, 0o644); err != nil {
			return err
		}
	}

	remote := path.Join(cfg.RemoteDir, shotDir)
	remotePath := path.Join(remote, name)
	if dryRun {
		fmt.Fprintf(out, "dry run: would send %s to %s:%s\n", local, cfg.Host, remotePath)
		return nil
	}
	if err := sendDir(cfg.Host, dir, remote); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s:%s\n", cfg.Host, remotePath)
	if err := clip(remotePath); err != nil {
		fmt.Fprintf(out, "not copied to the clipboard: %v\n", err)
	}
	return nil
}

// capture is `screencapture -i`: the interactive crop, the same one cmd-shift-4 runs. It
// exits 0 when the selection is cancelled and simply writes nothing, so the file is what
// decides whether there is a screenshot, not the status.
func capture(to string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("no screencapture on %s: pass a file to send instead", runtime.GOOS)
	}
	cmd := exec.Command("screencapture", "-i", to)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("screencapture: %w", err)
	}
	switch fi, err := os.Stat(to); {
	case os.IsNotExist(err), err == nil && fi.Size() == 0:
		return fmt.Errorf("no screenshot taken (selection cancelled), nothing sent")
	case err != nil:
		return err
	}
	return nil
}

// clip puts the box-side path on the clipboard, which is what gets pasted into a prompt. Only
// pbcopy, because this command only runs where there is a screen to capture; a missing one is
// reported and the path is printed either way, so nothing is lost with it.
func clip(s string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(s)
	return cmd.Run()
}
