package sand

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// configHome points ConfigPath at a temp dir. os.UserConfigDir reads XDG_CONFIG_HOME
// first on linux and falls back to HOME/Library on darwin, so both get set.
func configHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
}

func TestSetEveryKey(t *testing.T) {
	configHome(t)

	// No file yet: set has to create it, not refuse like init does.
	if _, err := Set([][2]string{{"host", "ubuntu@box"}, {"remote_dir", "~/work/sand"}}); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "ubuntu@box" || c.RemoteDir != "~/work/sand" {
		t.Fatalf("read back %+v", c)
	}

	// Every key the struct exposes must be settable, or this test goes stale the moment
	// a field is added.
	for _, k := range ConfigKeys() {
		if _, err := Set([][2]string{{k, "value-for-" + k}}); err != nil {
			t.Errorf("set %s: %v", k, err)
		}
	}
}

// Setting one key must not disturb the other, and must not eat the comments.
func TestSetPreservesTheRestOfTheFile(t *testing.T) {
	configHome(t)
	if _, err := Set([][2]string{{"remote_dir", "~/elsewhere"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Set([][2]string{{"host", "other-box"}}); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{"host: other-box", "remote_dir: ~/elsewhere", "# remote_dir:"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q from:\n%s", want, got)
		}
	}
}

// An unset key must stay unset in the file rather than freeze today's default into it.
func TestSetDoesNotBakeInDefaults(t *testing.T) {
	configHome(t)
	if _, err := Set([][2]string{{"host", "box"}}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	// As a value, not as the comment naming it: the comment is how an empty key still
	// tells the reader what it will do.
	if strings.Contains(string(body), "remote_dir: "+defaultRemoteDir) {
		t.Errorf("wrote the default remote_dir into the file:\n%s", body)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.RemoteDir != defaultRemoteDir {
		t.Errorf("Load stopped defaulting remote_dir, got %q", c.RemoteDir)
	}
}

// Get is what the Makefile reads to find the box, so it has to answer with no config file
// at all, honour the file when there is one, and let env win over it.
func TestGetEffectiveValue(t *testing.T) {
	configHome(t)

	// The host has no default, so with no file the true answer is empty, and asking for
	// it must not be an error: the Makefile reads this and prints its own message.
	if v, err := Get("host"); err != nil || v != "" {
		t.Fatalf("with no config file: %q, %v; want empty", v, err)
	}
	// A key that does have a default still answers on a machine with no host at all.
	// This went through Resolve once, which meant an unset host failed every get.
	if v, err := Get("remote_dir"); err != nil || v != defaultRemoteDir {
		t.Fatalf("get remote_dir with no host: %q, %v; want the default", v, err)
	}
	if _, err := Set([][2]string{{"host", "ubuntu@box"}}); err != nil {
		t.Fatal(err)
	}
	if v, err := Get("host"); err != nil || v != "ubuntu@box" {
		t.Fatalf("with a config file: %q, %v", v, err)
	}
	t.Setenv("SAND_HOST", "env-box")
	if v, err := Get("host"); err != nil || v != "env-box" {
		t.Fatalf("with SAND_HOST set: %q, %v", v, err)
	}
	if _, err := Get("hots"); err == nil {
		t.Error("an unknown key came back without an error")
	}
}

func TestSetRejectsUnknownKey(t *testing.T) {
	configHome(t)
	_, err := Set([][2]string{{"hots", "box"}})
	if err == nil || !strings.Contains(err.Error(), "unknown config key") {
		t.Fatalf("err = %v, want a complaint about the key", err)
	}
	if _, statErr := os.Stat(ConfigPath()); !os.IsNotExist(statErr) {
		t.Error("a rejected key still wrote a file")
	}
}

// init has to be safe to run again: same file, same bytes, whatever state it starts from.
// A setup script that has to know whether the config already exists is a setup script that
// gets it wrong.
func TestInitIsIdempotent(t *testing.T) {
	configHome(t)

	if _, err := InitConfig("", strings.NewReader("first-box\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	first := read(t, ConfigPath())

	// Second run: a different answer waiting on stdin, which must never be read, because
	// the file already names a host.
	if _, err := InitConfig("", strings.NewReader("second-box\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	if second := read(t, ConfigPath()); second != first {
		t.Errorf("second init changed the file:\n%s\nwant:\n%s", second, first)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "first-box" {
		t.Errorf("host = %q, want the answer from the first run", c.Host)
	}
}

// The prompt is the only way in for the one key with no default, so it has to reach the
// file; and --host has to win over asking at all.
func TestInitAsksForTheHost(t *testing.T) {
	configHome(t)

	var prompt strings.Builder
	if _, err := InitConfig("", strings.NewReader("  ubuntu@box  \n"), &prompt); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load(); c.Host != "ubuntu@box" {
		t.Errorf("host = %q, want the trimmed answer", c.Host)
	}
	if !strings.Contains(prompt.String(), "sandbox ssh alias") {
		t.Errorf("no prompt was printed, got %q", prompt.String())
	}

	configHome(t)
	var quiet strings.Builder
	if _, err := InitConfig("flag-box", strings.NewReader("typed-box\n"), &quiet); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load(); c.Host != "flag-box" {
		t.Errorf("host = %q, want --host to win", c.Host)
	}
	if quiet.String() != "" {
		t.Errorf("asked anyway: %q", quiet.String())
	}
}

// Unattended: no answer available. Write the file, leave the host unset, do not block and
// do not invent a hostname.
func TestInitWithNothingToRead(t *testing.T) {
	configHome(t)
	for _, in := range []io.Reader{nil, strings.NewReader("")} {
		configHome(t)
		if _, err := InitConfig("", in, io.Discard); err != nil {
			t.Fatalf("in = %v: %v", in, err)
		}
		if c, _ := Load(); c.Host != "" {
			t.Errorf("in = %v: host = %q, want it left unset", in, c.Host)
		}
		if _, err := Resolve("", ""); err == nil {
			t.Error("Resolve accepted an empty host")
		}
	}
}

// A file written by an older sand has to gain the keys added since, and keep its values.
// This is the case the old "already exists, refusing" init had no answer for.
func TestInitBringsAnOldFileUpToDate(t *testing.T) {
	configHome(t)
	if err := os.MkdirAll(filepath.Dir(ConfigPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(), []byte("host: old-box\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := InitConfig("", nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := read(t, ConfigPath())
	if !strings.Contains(got, "host: old-box") {
		t.Errorf("lost the host that was there:\n%s", got)
	}
	for _, k := range ConfigKeys() {
		if !strings.Contains(got, "# "+k+":") || !strings.Contains(got, "\n"+k+":") {
			t.Errorf("no comment and key for %q in:\n%s", k, got)
		}
	}
}

// init must not freeze today's defaults into the file, for the same reason set must not:
// the value would outlive the default it copied. TestSetDoesNotBakeInDefaults is the twin.
func TestInitDoesNotBakeInDefaults(t *testing.T) {
	configHome(t)
	if _, err := InitConfig("box", nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := read(t, ConfigPath())
	if strings.Contains(got, "remote_dir: "+defaultRemoteDir) {
		t.Errorf("wrote the default remote_dir as a value:\n%s", got)
	}
	if !strings.Contains(got, "# Unset means "+defaultRemoteDir) {
		t.Errorf("did not say what the default is:\n%s", got)
	}
	if c, _ := Load(); c.RemoteDir != defaultRemoteDir {
		t.Errorf("remote_dir = %q, want Load to still default it", c.RemoteDir)
	}
}

// The config comment naming the harnesses comes off the harnesses table, so adding one
// cannot leave the file describing a set that no longer exists.
func TestConfigDocNamesEveryHarness(t *testing.T) {
	for _, name := range harnessNames() {
		if !strings.Contains(configDoc["harness"], name) {
			t.Errorf("harness comment does not mention %q: %s", name, configDoc["harness"])
		}
	}
}

// A value YAML would read back as something else has to survive the round trip.
func TestSetQuotesAwkwardValues(t *testing.T) {
	configHome(t)
	for _, v := range []string{"~", "user@host:2222", "yes", "#1", "~/dir with spaces"} {
		if _, err := Set([][2]string{{"remote_dir", v}}); err != nil {
			t.Fatal(err)
		}
		c, err := Load()
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		if c.RemoteDir != v {
			body, _ := os.ReadFile(ConfigPath())
			t.Errorf("set %q, read back %q; file:\n%s", v, c.RemoteDir, body)
		}
	}
}
