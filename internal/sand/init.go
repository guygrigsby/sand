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
)

// InitOpts is one `sand init` run. In and Out carry the questions, so a test can answer them
// and an unattended run (no tty, EOF) keeps every value the file already had.
type InitOpts struct {
	Host string // --host, which wins over the file and skips that question
	In   io.Reader
	Out  io.Writer
}

// gaps is what this Mac still needs, collected rather than returned one at a time: a setup run
// that stops at the first thing missing makes the operator run it once per problem, and the
// problems are independent. Each is a line saying what is wrong and the command that fixes it.
type gaps struct {
	out   io.Writer
	items []string
}

// ok reports a check that passed, one line, so the run reads as a list of answers rather than
// silence punctuated by complaints.
func (g *gaps) ok(format string, a ...any) {
	fmt.Fprintf(g.out, "  ok    %s\n", fmt.Sprintf(format, a...))
}

// gap reports one that did not, with the fix it needs. Printed as it is found, and kept for the
// summary: the fixes are what the operator does next, and scrolling back for them is the thing
// that makes people skip one.
func (g *gaps) gap(what, fix string) {
	fmt.Fprintf(g.out, "  TODO  %s\n        %s\n", what, fix)
	g.items = append(g.items, what+"\n    "+fix)
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

	g := &gaps{out: o.Out}
	checkGitHub(g)
	checkSigning(g)
	checkThisCheckout(g)
	checkBox(g, cfg)

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
// signing works locally, GitHub calls every commit unverified, and the run stops with the
// replies unposted. Both halves are checkable here — a key configured to sign with, and the
// same key on the account as a signing key — so both are checked.
//
// Only the ssh case is matched against the account. An openpgp key is reported as configured
// and left there: matching a subkey fingerprint from here is a different job than this command
// is doing, and a wrong "not on GitHub" would send someone re-adding a key they already have.
func checkSigning(g *gaps) {
	c := gitCmd{out: io.Discard}
	key, _ := c.capture("config", "user.signingkey")
	if key == "" {
		g.gap("no git signing key, so nothing this Mac signs will verify on GitHub",
			"git config --global user.signingkey <path-to-key.pub> (and gpg.format ssh)")
		return
	}
	format, _ := c.capture("config", "gpg.format")
	if format != "ssh" {
		g.ok("signing with a %s key (%s); its presence on your GitHub account is not checked here",
			cmp.Or(format, "openpgp"), key)
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
		g.gap("the signing key is not on your GitHub account as a signing key, so GitHub will "+
			"call every commit unverified and `sand up` will stop before posting",
			"gh ssh-key add "+key+" --type signing")
		return
	}
	g.ok("signing key %s is on your GitHub account", key)
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

	if boxURL, _, boxDir := thisRepoOnBox(); boxURL != "" {
		if branch, err := boxCurrentBranch(gitCmd{out: io.Discard}, boxURL); err != nil {
			g.gap(fmt.Sprintf("no git checkout at %s that git can read: %v", boxURL, firstLine(err.Error(), 200)),
				fmt.Sprintf("on the box: git clone <url> %s", boxDir))
		} else {
			g.ok("box has a checkout at %s, on %s", boxURL, cmp.Or(branch, "a detached HEAD"))
		}
	}

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
