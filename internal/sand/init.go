package sand

// Setup is one command because it was seven, and the seventh was always the one nobody ran.
// Getting a Mac ready meant the config file, `gh auth login`, a signing key, that same key added
// to the GitHub account as a *signing* key, ssh to the box, a checkout there and the skill
// written into it — each documented in a different place, each discovered by a command failing
// halfway through a review round. `sand init` asks the questions and then answers every one of
// those checks in one run, naming the exact command for whatever it cannot do itself.
//
// It only writes two things: this Mac's config file, and the skill on the box (which is the
// Mac's to own, since the Mac carries its text). Everything else it reports, because a setup
// command that installs a signing key or logs a `gh` in is a setup command doing things nobody
// asked it to.

import (
	"bufio"
	"cmp"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// InitOpts is one `sand init` run. In and Out carry the questions, so a test can answer them
// and an unattended run (no tty, EOF) keeps every value the file already had.
type InitOpts struct {
	Host string // --host, which wins over the file and skips that question
	In   io.Reader
	Out  io.Writer
}

// gaps is what one check found: its lines, and what of it is still missing. Collected rather
// than returned one at a time, because a setup run that stops at the first thing missing makes
// the operator run it once per problem and the problems are independent.
//
// Buffered rather than written straight out, because the checks run concurrently. Every one of
// them is round trips — to GitHub, to the box over ssh — and serially a run costs the sum of
// every latency between here and two other machines. The buffers are printed in order
// afterwards, so a concurrent run reads exactly like a serial one.
type gaps struct {
	buf   strings.Builder
	items []string
}

// ok reports a check that passed, one line, so the run reads as a list of answers rather than
// silence punctuated by complaints.
func (g *gaps) ok(format string, a ...any) {
	fmt.Fprintf(&g.buf, "  ok    %s\n", fmt.Sprintf(format, a...))
}

// gap reports one that did not, with the fix it needs. Kept as well as printed: the fixes are
// what the operator does next, and scrolling back for them is what makes people skip one, so
// they are repeated in the summary.
func (g *gaps) gap(what, fix string) {
	fmt.Fprintf(&g.buf, "  TODO  %s\n        %s\n", what, fix)
	g.items = append(g.items, what+"\n    "+fix)
}

// absorb takes what a nested set of concurrent checks found, in the order they were listed.
func (g *gaps) absorb(others ...*gaps) {
	for _, o := range others {
		g.buf.WriteString(o.buf.String())
		g.items = append(g.items, o.items...)
	}
}

// concurrently runs each check with its own buffer and returns them in the order given. One
// place, because both the top level and the box's own checks want the same thing: independent
// latency overlapped, output that still reads top to bottom.
func concurrently(checks ...func(*gaps)) []*gaps {
	found := make([]*gaps, len(checks))
	var wg sync.WaitGroup
	for i, check := range checks {
		found[i] = &gaps{}
		wg.Add(1)
		go func() { defer wg.Done(); check(found[i]) }()
	}
	wg.Wait()
	return found
}

// Init asks for the config, writes it, then checks everything else a Mac needs and says what is
// missing. The order is the order the loop uses them in, so a run reads top to bottom: the
// config, then GitHub, then the box.
func Init(o InitOpts) error {
	var answers *bufio.Reader
	if o.In != nil {
		answers = bufio.NewReader(o.In)
	}

	cfg, path, err := askConfig(o, answers)
	if err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "\nwrote %s\n\nChecking what else this Mac needs:\n", path)

	g := &gaps{}
	g.absorb(concurrently(
		checkGitHub,
		checkSigning,
		checkThisCheckout,
		func(g *gaps) { checkBox(g, cfg) },
	)...)
	io.WriteString(o.Out, g.buf.String())

	if len(g.items) == 0 {
		fmt.Fprintf(o.Out, "\nReady. `sand new <issue>` starts one, `sand status` says where it is.\n")
		return nil
	}
	fmt.Fprintf(o.Out, "\n%d thing(s) left, none of them this command's to do:\n", len(g.items))
	for i, item := range g.items {
		fmt.Fprintf(o.Out, "  %d. %s\n", i+1, item)
	}
	fmt.Fprintf(o.Out, "\nRe-run `sand init` after: it keeps every answer and re-checks the rest.\n")
	return nil
}

// askConfig asks for every key in the file, in the file's own order, taking the current value
// (or nothing) as the answer to an empty line. Generated from Config's fields for the reason
// `set` and the file rendering are: a key added to the struct is a key this asks about, with no
// second list to forget to update.
//
// An accepted default is written as empty, not as today's default. A `harness: claude` line the
// operator never chose is indistinguishable from one they did, and tomorrow's default would
// never reach this machine. The prompt shows what the empty answer means, which is the part
// they actually need to see.
func askConfig(o InitOpts, answers *bufio.Reader) (Config, string, error) {
	path := ConfigPath()
	cfg, err := loadFile()
	if err != nil {
		return cfg, path, err
	}
	if o.Host != "" {
		cfg.Host = o.Host
	}

	fmt.Fprintf(o.Out, "Config: %s\nEnter keeps the value in brackets, or the default where there is one.\n\n", path)
	defaults := configDefaults()
	for _, f := range configFields(&cfg) {
		question := f.Key
		if d := defaults[f.Key]; d != "" && *f.ptr == "" {
			question = fmt.Sprintf("%s (default %s)", f.Key, d)
		}
		// current is the file's value, so an empty answer keeps it, and an empty file value
		// stays empty rather than becoming today's default in writing.
		*f.ptr = ask(answers, o.Out, question, *f.ptr)
	}

	// The one key with no default and nothing to fall back on. Asked again rather than
	// accepted empty, because every command that talks to the box fails without it, and this
	// is the command whose whole job is to not leave that discovery to them.
	if cfg.Host == "" && answers != nil {
		fmt.Fprintln(o.Out, "\nhost names the one machine this Mac drives, and has no default.")
		cfg.Host = ask(answers, o.Out, "sandbox ssh alias or user@host", "")
	}
	if err := writeConfig(cfg); err != nil {
		return cfg, path, err
	}
	if cfg.Host == "" {
		warn("no host: `sand config set host <alias>` before anything that talks to the box")
	}
	return cfg, path, nil
}

// checkGitHub is `gh`, which holds the only GitHub credential in this whole tool. No token ever
// enters sand, so an unauthenticated `gh` is the end of the road for three of `up`'s four steps.
func checkGitHub(g *gaps) {
	if _, err := exec.LookPath("gh"); err != nil {
		g.gap("gh is not installed, and it is the only GitHub credential this tool has",
			"brew install gh && gh auth login")
		return
	}
	if out, err := exec.Command("gh", "auth", "status").CombinedOutput(); err != nil {
		g.gap("gh is installed but not authenticated: "+firstLine(string(out), 200), "gh auth login")
		return
	}
	login, err := ViewerLogin()
	if err != nil {
		g.gap("gh is authenticated but did not say who as: "+err.Error(), "gh auth status")
		return
	}
	g.ok("gh authenticated as %s", login)
}

// checkSigning is the failure `up` stops on most, and the one that is worst to discover there:
// the commits are signed, GitHub calls them unverified anyway, and the run stops at step 3 with
// the replies unposted. What makes that state so easy to reach is that it is two halves and
// only the first is visible locally — a key to sign with, and the *same key on the GitHub
// account* — so this checks both, in both formats git can sign in.
//
// git's default format is openpgp, so an unset gpg.format is the gpg case and not a missing
// answer. Both paths end at the same question asked of GitHub, because GitHub is the authority
// on what it will call verified; nothing on this Mac can answer it.
func checkSigning(g *gaps) {
	c := gitCmd{out: io.Discard}
	key, _ := c.capture("config", "user.signingkey")
	email, _ := c.capture("config", "user.email")
	if email == "" {
		g.gap("no git user.email, so there is no identity to sign as and `sand sign` refuses",
			"git config --global user.email <you@example.com>")
		return
	}
	if format, _ := c.capture("config", "gpg.format"); format == "ssh" {
		checkSSHSigning(g, key)
		return
	}
	checkGPGSigning(g, key, email)
}

// checkSSHSigning matches the key file's own bytes against the account's signing keys. The
// base64 middle field is the comparison: the comment differs per machine and the algorithm name
// is shared by every key of its kind.
func checkSSHSigning(g *gaps, key string) {
	if key == "" {
		g.gap("gpg.format is ssh but no user.signingkey, and ssh signing has no keyring to "+
			"search: git will refuse to sign at all",
			"git config --global user.signingkey ~/.ssh/id_ed25519.pub")
		return
	}
	body, err := sshKeyBody(key)
	if err != nil {
		g.gap(fmt.Sprintf("user.signingkey is %s and cannot be read: %v", key, err),
			"point user.signingkey at the public key file, e.g. ~/.ssh/id_ed25519.pub")
		return
	}
	onAccount, err := gh("api", "user/ssh_signing_keys", "--jq", ".[].key")
	if err != nil {
		g.gap("could not ask GitHub which signing keys the account has: "+firstLine(err.Error(), 200),
			"gh api user/ssh_signing_keys")
		return
	}
	if !strings.Contains(onAccount, body) {
		g.gap("the signing key is on this Mac but not on your GitHub account as a signing key, "+
			"so GitHub will call every commit unverified and `sand up` will stop before posting",
			"gh ssh-key add "+key+" --type signing")
		return
	}
	g.ok("ssh signing key %s is on your GitHub account", key)
}

// checkGPGSigning is the same two halves for a gpg key, which takes more asking because a gpg
// key is not one key and not one name for itself. The signature may come from a subkey, and
// `user.signingkey` may hold a short id, a long id, a fingerprint or a `!`-pinned subkey, while
// GitHub's `key_id` is documented only as "a string". So both sides are collected as sets of
// names and matched by suffix, which is the one comparison that is right for every pairing of
// those lengths.
//
// There is a third half here that ssh does not have: GitHub only calls a gpg-signed commit
// verified when the committer's address is one of the addresses it has verified *on that key*.
// A key that is on the account with the wrong address on it verifies nothing, and neither the
// local keyring nor `git log --show-signature` says a word about it.
func checkGPGSigning(g *gaps, key, email string) {
	if _, err := exec.LookPath("gpg"); err != nil {
		g.gap("git is set to sign with gpg (the default) and gpg is not installed",
			"brew install gnupg, or `git config --global gpg.format ssh` to sign with an ssh key")
		return
	}

	// An empty user.signingkey is legal and common for gpg: git signs with the secret key
	// matching user.email. So the lookup is by whichever of the two there is, and only a
	// keyring with no answer at all is a gap.
	want := cmp.Or(key, "<"+email+">")
	ids, err := gpgSecretKeyIDs(want)
	if err != nil || len(ids) == 0 {
		g.gap(fmt.Sprintf("gpg has no secret key for %s, so this Mac cannot sign at all", want),
			"gpg --list-secret-keys --keyid-format=long, then `git config --global user.signingkey <id>`")
		return
	}

	var account []ghGPGKey
	if err := ghJSON(&account, "api", "user/gpg_keys"); err != nil {
		g.gap("could not ask GitHub which gpg keys the account has: "+firstLine(err.Error(), 200),
			"gh api user/gpg_keys")
		return
	}
	match, matched, ok := matchGPGKey(account, ids)
	if !ok {
		g.gap(fmt.Sprintf("the gpg key for %s is on this Mac but not on your GitHub account, so "+
			"GitHub will call every commit unverified and `sand up` will stop before posting", want),
			fmt.Sprintf("gpg --armor --export %s > key.asc && gh gpg-key add key.asc", cmp.Or(key, email)))
		return
	}
	switch {
	case match.Revoked:
		g.gap(fmt.Sprintf("GitHub has gpg key %s but it is revoked there, and a revoked key "+
			"verifies nothing", matched), "upload the current key: gh gpg-key add key.asc")
		return
	case expiredAt(match.ExpiresAt):
		g.gap(fmt.Sprintf("GitHub has gpg key %s and it expired on %s", matched, *match.ExpiresAt),
			"extend the key (gpg --quick-set-expire), then re-upload it: gh gpg-key add key.asc")
		return
	case !verifiedFor(match, email):
		g.gap(fmt.Sprintf("GitHub has gpg key %s but does not list %s as a verified address on "+
			"it, so it will call the commits unverified however well they are signed. It lists: %s",
			matched, email, cmp.Or(strings.Join(verifiedAddresses(match), ", "), "nothing")),
			fmt.Sprintf("verify %s on your GitHub account, or set user.email to an address the "+
				"key carries", email))
		return
	}
	g.ok("gpg key %s is on your GitHub account, with %s verified on it", matched, email)
}

// ghGPGKey is one entry of `GET /user/gpg_keys`, kept to the fields that decide whether a
// signature of ours verifies: which key it is, whether GitHub still honours it, and which
// addresses it will accept on a commit signed by it. `emails` lives on the primary key even
// when the signing subkey is what matched, which is why the match returns the whole entry.
type ghGPGKey struct {
	KeyID     string  `json:"key_id"`
	Revoked   bool    `json:"revoked"`
	ExpiresAt *string `json:"expires_at"`
	Emails    []struct {
		Email    string `json:"email"`
		Verified bool   `json:"verified"`
	} `json:"emails"`
	Subkeys []struct {
		KeyID   string `json:"key_id"`
		Revoked bool   `json:"revoked"`
	} `json:"subkeys"`
}

// matchGPGKey finds the account entry naming one of this Mac's key ids, primary or subkey, and
// returns the entry, the name that matched (for the message: it is the one both machines agree
// on) and whether there was one.
func matchGPGKey(account []ghGPGKey, ids []string) (ghGPGKey, string, bool) {
	for _, k := range account {
		names := []string{k.KeyID}
		for _, sub := range k.Subkeys {
			names = append(names, sub.KeyID)
		}
		for _, name := range names {
			if sameGPGKey(name, ids) {
				return k, strings.ToUpper(name), true
			}
		}
	}
	return ghGPGKey{}, "", false
}

// sameGPGKey compares two names for one key when neither length is guaranteed: a fingerprint is
// 40 hex, a long id its last 16, a short id its last 8. A suffix in either direction is right
// for every pairing of those and wrong only for two keys that end the same way, which is the
// collision gpg itself lives with. Under eight characters is not a name, it is a coincidence
// waiting to happen, so it never matches.
func sameGPGKey(name string, ids []string) bool {
	name = strings.ToUpper(name)
	if len(name) < 8 {
		return false
	}
	for _, id := range ids {
		if len(id) < 8 {
			continue
		}
		if strings.HasSuffix(id, name) || strings.HasSuffix(name, id) {
			return true
		}
	}
	return false
}

// gpgSecretKeyIDs is every name gpg answers to for one key or one address: the long ids of the
// primary and each subkey, and their fingerprints. A `!`-pinned subkey is asked about unpinned,
// since the pin says which key signs and this is asking which keys exist.
func gpgSecretKeyIDs(want string) ([]string, error) {
	out, err := exec.Command("gpg", "--batch", "--with-colons", "--list-secret-keys",
		strings.TrimSuffix(want, "!")).Output()
	if err != nil {
		return nil, err
	}
	return parseGPGColons(string(out)), nil
}

// parseGPGColons pulls the ids out of gpg's `--with-colons` listing: field 5 of a `sec` or
// `ssb` line is that key's long id, and field 10 of the `fpr` line under it is its fingerprint.
// Separate from the exec so the parse is testable without a keyring, which is the only part of
// this that can be wrong in a way a machine notices.
func parseGPGColons(out string) []string {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		switch {
		case (f[0] == "sec" || f[0] == "ssb") && len(f) > 4 && f[4] != "":
			ids = append(ids, strings.ToUpper(f[4]))
		case f[0] == "fpr" && len(f) > 9 && f[9] != "":
			ids = append(ids, strings.ToUpper(f[9]))
		}
	}
	return ids
}

// verifiedFor is whether GitHub will accept this address on a commit signed by that key.
func verifiedFor(k ghGPGKey, email string) bool {
	for _, e := range k.Emails {
		if e.Verified && strings.EqualFold(e.Email, email) {
			return true
		}
	}
	return false
}

// verifiedAddresses is what it does accept, which is the useful half of saying no.
func verifiedAddresses(k ghGPGKey) []string {
	var out []string
	for _, e := range k.Emails {
		if e.Verified {
			out = append(out, e.Email)
		}
	}
	return out
}

// expiredAt reads GitHub's null-or-timestamp. Unparseable is not expired: a format this cannot
// read is not evidence about a key, and calling a working key expired sends someone to fix
// something that is not broken.
func expiredAt(ts *string) bool {
	if ts == nil || *ts == "" {
		return false
	}
	at, err := time.Parse(time.RFC3339, *ts)
	return err == nil && at.Before(time.Now())
}

// checkThisCheckout is the Mac side of one repo: it has to be a checkout with the remote signing
// pushes to, or `up` has nowhere to put what it signed. Not being in a repo at all is fine and
// says so: `sand init` is a per-machine command and someone will run it from home.
func checkThisCheckout(g *gaps) {
	c := gitCmd{out: io.Discard}
	if _, err := c.capture("rev-parse", "--absolute-git-dir"); err != nil {
		g.ok("not run inside a repository, so nothing repo-specific was checked (run it again " +
			"in one to check that repo's box checkout)")
		return
	}
	repo, err := c.capture("remote", "get-url", "origin")
	if err != nil {
		g.gap("this checkout has no `origin`, and that is where signing pushes",
			"git remote add origin <url>")
		return
	}
	g.ok("origin is %s", repo)
}

// checkBox is everything on the other machine, in the order each thing stops working: ssh, a
// checkout git can talk to, and the skill its agent reads. The skill is installed rather than
// reported, because the Mac is what carries the text and writing it there is this tool's job in
// the first place.
func checkBox(g *gaps, cfg Config) {
	if cfg.Host == "" {
		g.gap("no host, so nothing about the box could be checked", "sand config set host <alias>")
		return
	}
	// Host first and no options, like every other ssh in this program: the shim the tests use
	// takes the host as its first argument, and an ssh that wants a passphrase is fine here in
	// a way it would not be anywhere else, since this command is one somebody is sitting in
	// front of by definition.
	if out, err := exec.Command(sshBin(), cfg.Host, "true").CombinedOutput(); err != nil {
		// The tailnet refusing this Mac's local username is the one that reads as a broken
		// box rather than as a login, and it is fixed in ssh's config or in the host itself.
		g.gap(fmt.Sprintf("ssh %s failed: %s", cfg.Host, firstLine(string(out), 200)),
			fmt.Sprintf("a login name belongs in ~/.ssh/config or in the host: "+
				"`sand config set host ubuntu@%s`", cfg.Host))
		return
	}
	g.ok("ssh %s answers", cfg.Host)

	// Two more round trips to the same machine, and neither needs the other's answer.
	g.absorb(concurrently(
		func(g *gaps) { checkBoxCheckout(g) },
		func(g *gaps) { installBoxSkill(g, cfg) },
	)...)
}

// checkBoxCheckout asks the box's checkout of this repo the one question that proves git can
// talk to it at all, which is what the import at the top of every signing run needs.
func checkBoxCheckout(g *gaps) {
	boxURL, _, boxDir := thisRepoOnBox()
	if boxURL == "" {
		return // not in a repo: checkThisCheckout says so, and once is enough
	}
	branch, err := boxCurrentBranch(gitCmd{out: io.Discard}, boxURL)
	if err != nil {
		g.gap(fmt.Sprintf("no git checkout at %s that git can read: %v", boxURL, firstLine(err.Error(), 200)),
			fmt.Sprintf("on the box: git clone <url> %s", boxDir))
		return
	}
	g.ok("box has a checkout at %s, on %s", boxURL, cmp.Or(branch, "a detached HEAD"))
}

// installBoxSkill is the one thing init writes to the other machine, because the Mac is what
// carries the text and a box with no skill is a box whose agent does not know the loop.
func installBoxSkill(g *gaps, cfg Config) {
	skill, err := InstallSkillRemote(cfg.Host)
	if err != nil {
		g.gap("could not install the skill on the box: "+firstLine(err.Error(), 200),
			"sand skill install --remote (it is the only thing the box needs from here)")
		return
	}
	g.ok("skill at %s:%s%s", cfg.Host, skill.Path, installedInto(skill.Linked, skill.Current))

	// A harness the box does not have is the one gap here that is not sand's to fix and not
	// obvious from anything else: `comments pull` would start a command that is not there.
	harness := cmp.Or(cfg.Harness, defaultHarness)
	for _, absent := range skill.Absent {
		if absent == harness {
			g.gap(fmt.Sprintf("the box has no %s, which is the harness `comments pull` would start", harness),
				fmt.Sprintf("install it there, or `sand config set harness <%s>`",
					strings.Join(harnessNames(), "|")))
		}
	}
}

// installedInto names the harnesses now reading the skill, so the line says whether an agent
// over there can actually find it rather than only that a file was written.
func installedInto(linked, current []string) string {
	all := append(append([]string{}, linked...), current...)
	if len(all) == 0 {
		return ", linked to no harness (the box has none installed)"
	}
	return ", read by " + strings.Join(all, ", ")
}

// sshKeyBody is the base64 middle field of an ssh public key, which is the part GitHub stores
// and the part that identifies the key: the comment differs per machine and the algorithm is
// shared by every key of its kind. user.signingkey may name the public key or the private one,
// and the private one has the public beside it.
func sshKeyBody(path string) (string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) && !strings.HasSuffix(path, ".pub") {
		b, err = os.ReadFile(path + ".pub")
	}
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "ssh-") && !strings.HasPrefix(fields[0], "sk-") &&
		!strings.HasPrefix(fields[0], "ecdsa-") {
		return "", fmt.Errorf("not an ssh public key")
	}
	return fields[1], nil
}
