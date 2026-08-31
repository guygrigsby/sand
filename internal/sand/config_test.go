package sand

import (
	"os"
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
	if strings.Contains(string(body), defaultRemoteDir) {
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

	if v, err := Get("host"); err != nil || v != defaultHost {
		t.Fatalf("with no config file: %q, %v; want the default", v, err)
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
