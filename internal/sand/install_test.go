package sand

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// install.sh is a coworker's first contact with this tool and the one piece of it nobody runs
// twice, so it gets the same treatment as the rest: the download is faked over localhost and
// everything after it is the real script. What is actually being checked is that the platform
// mapping picks an asset, that the binary lands executable, that the script refuses to call it
// installed unless it runs, and that a box with a harness gets the skill without a second
// command.
func TestInstallScriptInstallsAndLinksTheSkill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash script")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl")
	}

	// The "release binary": a shell script, because the test asserts what the installer does
	// with it, not what sand does. It answers the two calls install.sh makes.
	fake := "#!/bin/sh\ncase \"$1 $2\" in\n" +
		"'--version ') echo 'sand version v0.0.0-fake' ;;\n" +
		"'skill install') echo \"skill installed under $HOME\" ;;\n" +
		"*) echo \"unexpected: $*\" >&2; exit 2 ;;\nesac\n"

	asset := fmt.Sprintf("sand-%s-%s", runtime.GOOS, runtime.GOARCH)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/latest/download/"+asset {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, fake)
	}))
	defer srv.Close()

	home := t.TempDir()
	// A harness present, so the skill branch runs. On a Mac with neither, it is skipped.
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, ".local", "bin")

	run := func(version string) (string, error) {
		cmd := exec.Command("bash", "install.sh")
		cmd.Dir = repoRoot(t)
		cmd.Env = append(os.Environ(),
			"HOME="+home, "BIN_DIR="+bin, "SAND_BASE_URL="+srv.URL, "SAND_VERSION="+version)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run("latest")
	if err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	installed := filepath.Join(bin, "sand")
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatalf("nothing installed at %s: %v\n%s", installed, err, out)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("%s is not executable: %v", installed, info.Mode())
	}
	for _, want := range []string{"v0.0.0-fake", "skill installed", "sand config init"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}

	// A tag with no build for this platform must fail loudly and leave the working install
	// alone. Writing an HTML 404 page over a binary is the one outcome worth a test.
	before, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	out, err = run("v0.0.0-does-not-exist")
	if err == nil {
		t.Errorf("a missing release succeeded:\n%s", out)
	}
	if !strings.Contains(out, "could not download") {
		t.Errorf("output does not say what went wrong:\n%s", out)
	}
	if after, err := os.ReadFile(installed); err != nil || string(after) != string(before) {
		t.Errorf("the failed install replaced the working binary: %v", err)
	}
}

// repoRoot is where install.sh lives: two directories up from this package.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd))
}
