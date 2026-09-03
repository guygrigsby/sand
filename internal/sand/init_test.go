package sand

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// The gpg path, which is the one in use: a real key in a real keyring, and GitHub's answer
// faked at the only place it can be. The cases are states of one account, and only the first
// two sign anything GitHub will call verified.
//
// It signs by user.email with no user.signingkey set, which is legal for gpg and normal, and is
// the case a check keyed on user.signingkey reported as "no signing key" when nothing was
// wrong.
func TestInitChecksTheGPGKeyIsOnTheAccount(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("no gpg on this machine")
	}
	dir, _ := signRepo(t)
	harness(t)
	configHome(t)

	const email = "sand@example.invalid" // signRepo's git identity
	t.Setenv("GNUPGHOME", t.TempDir())
	mustRun(t, dir, "gpg", "--batch", "--passphrase", "", "--quick-generate-key",
		"Sand Test <"+email+">", "ed25519", "sign", "0")
	mustRun(t, dir, "git", "config", "--unset", "gpg.format")      // signRepo signs with ssh
	mustRun(t, dir, "git", "config", "--unset", "user.signingkey") // gpg finds it by email

	// The fingerprint, read straight out of gpg rather than through the parser under test.
	var fpr string
	for _, line := range strings.Split(mustRun(t, dir, "gpg", "--batch", "--with-colons", "--list-secret-keys"), "\n") {
		if f := strings.Split(line, ":"); f[0] == "fpr" && fpr == "" {
			fpr = f[9]
		}
	}
	if len(fpr) != 40 {
		t.Fatalf("gpg gave a %d character fingerprint: %q", len(fpr), fpr)
	}
	long := fpr[len(fpr)-16:] // what GitHub returns as key_id

	account := func(t *testing.T, body string) {
		t.Helper()
		p := filepath.Join(t.TempDir(), "gpg-keys.json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GH_GPG_KEYS", p)
	}
	check := func(t *testing.T) string {
		t.Helper()
		var out strings.Builder
		g := &gaps{out: &out}
		checkSigning(g)
		return out.String()
	}

	// GitHub names the key by its long id while gpg here knows it by fingerprint, so the match
	// is the whole point of the case.
	t.Run("on the account with the address verified", func(t *testing.T) {
		account(t, `[{"key_id":"`+long+`","revoked":false,"expires_at":null,
			"emails":[{"email":"`+email+`","verified":true}],"subkeys":[]}]`)
		printed := check(t)
		if !strings.Contains(printed, "gpg key "+long+" is on your GitHub account") {
			t.Errorf("did not match the key GitHub has:\n%s", printed)
		}
		if strings.Contains(printed, "TODO") {
			t.Errorf("reported a gap for a key that is there:\n%s", printed)
		}
	})

	// The half that neither the keyring nor `git log --show-signature` can see: the key is
	// there, the address on it is not, and GitHub calls the commits unverified anyway.
	t.Run("on the account with a different address", func(t *testing.T) {
		account(t, `[{"key_id":"`+long+`","revoked":false,"expires_at":null,
			"emails":[{"email":"someone@else.invalid","verified":true}],"subkeys":[]}]`)
		printed := check(t)
		for _, want := range []string{"does not list " + email, "someone@else.invalid", "TODO"} {
			if !strings.Contains(printed, want) {
				t.Errorf("output missing %q:\n%s", want, printed)
			}
		}
	})

	t.Run("a different key entirely", func(t *testing.T) {
		account(t, `[{"key_id":"DEADBEEFDEADBEEF","revoked":false,"expires_at":null,
			"emails":[{"email":"`+email+`","verified":true}],"subkeys":[]}]`)
		printed := check(t)
		for _, want := range []string{"not on your GitHub account", "gh gpg-key add"} {
			if !strings.Contains(printed, want) {
				t.Errorf("output missing %q:\n%s", want, printed)
			}
		}
	})

	// A signature can come from a subkey, and then the primary is what GitHub lists the
	// addresses under, so the match has to look at both and report the entry it found.
	t.Run("matched on a subkey", func(t *testing.T) {
		account(t, `[{"key_id":"DEADBEEFDEADBEEF","revoked":false,"expires_at":null,
			"emails":[{"email":"`+email+`","verified":true}],
			"subkeys":[{"key_id":"`+long+`","revoked":false}]}]`)
		printed := check(t)
		if !strings.Contains(printed, "is on your GitHub account") || strings.Contains(printed, "TODO") {
			t.Errorf("subkey match missed:\n%s", printed)
		}
	})

	t.Run("revoked there", func(t *testing.T) {
		account(t, `[{"key_id":"`+long+`","revoked":true,"expires_at":null,
			"emails":[{"email":"`+email+`","verified":true}],"subkeys":[]}]`)
		if printed := check(t); !strings.Contains(printed, "revoked") {
			t.Errorf("a revoked key passed:\n%s", printed)
		}
	})

	t.Run("expired there", func(t *testing.T) {
		account(t, `[{"key_id":"`+long+`","revoked":false,"expires_at":"2020-01-02T03:04:05Z",
			"emails":[{"email":"`+email+`","verified":true}],"subkeys":[]}]`)
		if printed := check(t); !strings.Contains(printed, "expired on 2020-01-02") {
			t.Errorf("an expired key passed:\n%s", printed)
		}
	})
}

// The parse and the comparison, without a keyring: gpg's colon format is a positional contract
// and the id lengths are the thing that makes a naive == wrong.
func TestGPGKeyNamesMatchAcrossLengths(t *testing.T) {
	const listing = `sec:u:255:22:2252A2A72586FB9C:1788471757:::u:::scSC:::+::ed25519:::0:
fpr:::::::::C1F828A9E57872719A1E1AB62252A2A72586FB9C:
grp:::::::::BA08385BA96DB888A837665638820ED72855D0DB:
uid:u::::1788471757::1162B13999D21A9758010C1D1FB2BABA1BE6865B::Sand Test <sand@example.invalid>::::::::::0:
ssb:u:255:18:9BE9F4A1C0FFEE00:1788471757::::::esa:::+::cv25519::
fpr:::::::::AAAA1111BBBB2222CCCC33339BE9F4A1C0FFEE00:
`
	ids := parseGPGColons(listing)
	for _, want := range []string{
		"2252A2A72586FB9C",                         // the primary's long id
		"C1F828A9E57872719A1E1AB62252A2A72586FB9C", // and its fingerprint
		"9BE9F4A1C0FFEE00",                         // the subkey, which is what signs
		"AAAA1111BBBB2222CCCC33339BE9F4A1C0FFEE00",
	} {
		if !slices.Contains(ids, want) {
			t.Errorf("parsed ids %v, missing %s", ids, want)
		}
	}
	// The keygrip is not a name for the key, and neither is anything on a uid line.
	if slices.Contains(ids, "BA08385BA96DB888A837665638820ED72855D0DB") {
		t.Errorf("took the keygrip for a key id: %v", ids)
	}

	// Every length GitHub or a config might name it by, in either direction.
	for _, name := range []string{
		"2252A2A72586FB9C", "2586FB9C", "C1F828A9E57872719A1E1AB62252A2A72586FB9C",
		"9be9f4a1c0ffee00", // and case is not part of a key id
	} {
		if !sameGPGKey(name, ids) {
			t.Errorf("%s did not match the same key", name)
		}
	}
	for _, name := range []string{"DEADBEEFDEADBEEF", "FB9C", "", "0000000000000000"} {
		if sameGPGKey(name, ids) {
			t.Errorf("%q matched a key it is not", name)
		}
	}
}
