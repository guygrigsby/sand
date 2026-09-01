package sand

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The capture itself needs a window server, so the test drives the other half: the file
// argument. Everything after the image exists is the same code either way, and that is the
// part that can be wrong — the box path, the transport and what gets printed.
func TestShotSendsAnImageToTheBox(t *testing.T) {
	remoteBase, _ := harness(t)
	png := filepath.Join(t.TempDir(), "grab.png")
	if err := os.WriteFile(png, []byte("\x89PNG\r\n\x1a\nnot really"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cfg := Config{Host: "box", RemoteDir: remoteBase}
	if err := Shot(cfg, png, false, &out); err != nil {
		t.Fatalf("shot: %v", err)
	}

	got, err := filepath.Glob(filepath.Join(remoteBase, shotDir, "ss-*.png"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("files on the box: %v", got)
	}
	if body, err := os.ReadFile(got[0]); err != nil || !strings.HasPrefix(string(body), "\x89PNG") {
		t.Fatalf("image did not arrive intact: %q, %v", body, err)
	}
	// The path it prints is the path it wrote, in the form an agent on the box can open.
	if want := "box:" + filepath.Join(remoteBase, shotDir, filepath.Base(got[0])); !strings.Contains(out.String(), want) {
		t.Errorf("output %q does not name %q", out.String(), want)
	}

	// A second shot in the same second must not land on the first one's name: sendDir writes
	// by name, so a collision is a lost screenshot rather than a duplicate.
	if err := Shot(cfg, png, false, &out); err != nil {
		t.Fatalf("second shot: %v", err)
	}
	if got, _ := filepath.Glob(filepath.Join(remoteBase, shotDir, "ss-*.png")); len(got) != 2 {
		t.Errorf("want both shots on the box, got %v", got)
	}

	// The extension follows the file it was given: an agent opens these by name, and a .png
	// that is a jpeg is a worse answer than a .jpg.
	jpg := filepath.Join(t.TempDir(), "grab.jpg")
	if err := os.WriteFile(jpg, []byte("\xff\xd8\xff"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Shot(cfg, jpg, false, &out); err != nil {
		t.Fatalf("jpg shot: %v", err)
	}
	if got, _ := filepath.Glob(filepath.Join(remoteBase, shotDir, "ss-*.jpg")); len(got) != 1 {
		t.Errorf("want the jpg to keep its extension, got %v", got)
	}
}

func TestShotDryRunSendsNothing(t *testing.T) {
	remoteBase, _ := harness(t)
	png := filepath.Join(t.TempDir(), "grab.png")
	if err := os.WriteFile(png, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Shot(Config{Host: "box", RemoteDir: remoteBase}, png, true, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(remoteBase, shotDir)); !os.IsNotExist(err) {
		t.Errorf("the dry run created %s/%s", remoteBase, shotDir)
	}
	if !strings.Contains(out.String(), "dry run") {
		t.Errorf("output %q", out.String())
	}
}
