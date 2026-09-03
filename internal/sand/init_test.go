package sand

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Mac with everything in place: the run answers every check and says so, and the config it
// wrote holds what was typed. The point of the test is the last assertion as much as the
// first — an empty answer must leave the key empty rather than write today's default into the
// file, or the default can never change again on this machine.
func TestInitChecksEverythingAndWritesOnlyWhatWasTyped(t *testing.T) {
	dir, _ := signRepo(t)
	boxAtURL(t, dir, "feature") // the box, reachable by git as `box:projects/repo`
	harness(t)                  // the gh stub, the ssh shim
	configHome(t)               // and a config path that is not this machine's real one

	// The box has a harness, which is what makes the skill worth writing there.
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	// The account has the key this checkout signs with, which is the pair `up` step 3 fails on
	// when it is only half true.
	pub := filepath.Join(filepath.Dir(dir), "id.pub")
	body := strings.Fields(read(t, pub))
	keys := filepath.Join(t.TempDir(), "signing-keys")
	if err := os.WriteFile(keys, []byte(body[1]+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_SIGNING_KEYS", keys)

	var out strings.Builder
	// host from the flag, then Enter for every other key.
	if err := Init(InitOpts{Host: "box", In: strings.NewReader("\n\n\n\n\n"), Out: &out}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	printed := out.String()
	for _, want := range []string{
		"gh authenticated as guy",
		"is on your GitHub account",
		"origin is ",
		"ssh box answers",
		"box has a checkout at box:projects/repo, on feature",
		"skill at box:",
		"Ready.",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("output missing %q:\n%s", want, printed)
		}
	}
	if strings.Contains(printed, "TODO") {
		t.Errorf("reported something missing on a Mac that has everything:\n%s", printed)
	}

	file := read(t, ConfigPath())
	if !strings.Contains(file, "host: box") {
		t.Errorf("host not written:\n%s", file)
	}
	// The defaults were accepted, so they are named in the comments and absent as values.
	for _, unwanted := range []string{"harness: claude", "remote_dir: ~/.sand"} {
		if strings.Contains(file, unwanted) {
			t.Errorf("wrote a defaulted value (%q), which freezes it on this machine:\n%s", unwanted, file)
		}
	}
}

// The other half: a Mac that is missing things says all of them, with the command for each,
// and does not stop at the first. Setup is a list, and a setup command that reports one item
// per run is a setup command run four times.
func TestInitCollectsEveryGapRatherThanStoppingAtOne(t *testing.T) {
	signRepo(t)
	harness(t)
	configHome(t)
	t.Setenv("GH_AUTH_EXIT", "1") // gh installed, not logged in

	var out strings.Builder
	// No host, and nobody there to answer: the file is written as it stands and the box
	// checks say what they could not do rather than being skipped in silence.
	if err := Init(InitOpts{Out: &out}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	printed := out.String()
	for _, want := range []string{
		"not authenticated", "gh auth login",
		"no host", "sand config set host",
		"thing(s) left",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("output missing %q:\n%s", want, printed)
		}
	}
	if strings.Contains(printed, "Ready.") {
		t.Errorf("called a Mac with no host ready:\n%s", printed)
	}
	if got := strings.Count(printed, "TODO"); got < 2 {
		t.Errorf("reported %d gap(s), want the whole list:\n%s", got, printed)
	}
}
