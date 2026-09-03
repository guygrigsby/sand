package sand

import (
	"bufio"
	"cmp"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is ~/.config/sand/config.yaml. Flags and env override it; see Resolve.
type Config struct {
	Host         string `yaml:"host"`          // ssh alias or user@host for the sandbox
	RemoteDir    string `yaml:"remote_dir"`    // base dir on the sandbox, ~ allowed
	Harness      string `yaml:"harness"`       // agent CLI pull starts on the box: see harnesses
	Model        string `yaml:"model"`         // model to pass it; empty means the harness's own default
	BranchPrefix string `yaml:"branch_prefix"` // what `sand new` puts before <issue>-<title>
}

const (
	defaultRemoteDir = "~/.sand"
	defaultHarness   = "claude"
)

// configDefaults is what a key falls back to when neither the file nor the environment says.
// Keyed by config key so a new field defaults, documents and overrides itself through the
// same three tables rather than another if-empty line in Load.
//
// An empty default is a real answer, for two different reasons. No model means the harness
// picks, which is the only answer that does not go stale every time a model ships. No host
// means there is nothing sensible to guess: it names one specific machine on one specific
// tailnet, and a compiled-in alias is either someone else's box or an alias that resolves
// nowhere. `sand config init` asks for it instead, and every command that needs it says so.
//
// A function rather than a table because one default is not a constant: the branch prefix is
// whoever is running this, which is only knowable at run time. It was `guy/`, compiled into
// `sand new`, so every other person's first branch was named after someone else and `up`
// could not find the issue number in a branch they had named themselves.
func configDefaults() map[string]string {
	return map[string]string{
		"host":          "",
		"remote_dir":    defaultRemoteDir,
		"harness":       defaultHarness,
		"model":         "",
		"branch_prefix": cmp.Or(os.Getenv("USER"), os.Getenv("LOGNAME")),
	}
}

// ConfigPath is `~/.config/sand/config.yaml`, `XDG_CONFIG_HOME` first. The box has none: it
// never runs this tool.
//
// Deliberately not os.UserConfigDir, which answers `~/Library/Application Support` on darwin.
// The driving machine is usually a Mac and sometimes a Linux laptop, and every mention of the
// file in the docs and in `sand config`'s own output says `~/.config`. A path that is right on
// one of them and silently different on the other is a config nobody can find.
func ConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir() // empty without HOME, and then the read error names the path
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "sand", "config.yaml")
}

// loadFile reads the config file as written, with no defaults filled in. Set needs that:
// writing a defaulted value back would freeze today's default into the file.
func loadFile() (Config, error) {
	var c Config
	b, err := os.ReadFile(ConfigPath())
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", ConfigPath(), err)
	}
	return c, nil
}

// Load reads the config file, with the defaults filled in for whatever it does not say. A
// missing file is not an error — the defaults, env or flags may cover it.
func Load() (Config, error) {
	c, err := loadFile()
	fill := func() Config {
		defaults := configDefaults()
		for _, f := range configFields(&c) {
			if *f.ptr == "" {
				*f.ptr = defaults[f.Key]
			}
		}
		return c
	}
	if err != nil {
		c = Config{}
		return fill(), err
	}
	return fill(), nil
}

// effective is the file with the environment over it and the defaults under it, checked
// for nothing. Get needs that separately from Resolve: asking for `harness` must not fail
// because `host` is unset, which is the state of every machine before `config init` runs.
func effective() (Config, error) {
	c, err := Load()
	if err != nil {
		return c, err
	}
	for _, f := range configFields(&c) {
		if v := os.Getenv("SAND_" + strings.ToUpper(f.Key)); v != "" {
			*f.ptr = v
		}
	}
	return c, nil
}

// Resolve settles every value for a command that is about to talk to the box: flag, then
// SAND_<KEY> in the environment, then the file, then the default. Only host and remote dir
// have flags, because they are the two the other commands take. A missing host is an error
// here and only here: this is the point where one is actually needed.
func Resolve(hostFlag, remoteDirFlag string) (Config, error) {
	c, err := effective()
	if err != nil {
		return c, err
	}
	if hostFlag != "" {
		c.Host = hostFlag
	}
	if remoteDirFlag != "" {
		c.RemoteDir = remoteDirFlag
	}
	if c.Host == "" {
		return c, fmt.Errorf("no sandbox host: run `sand config init`, pass --host, or set SAND_HOST")
	}
	return c, nil
}

// configDoc is the comment written above each key. The file is rendered from the struct
// and this, rather than round-tripped, so `config set` cannot lose the documentation and a
// new field needs nothing here but its own line.
var configDoc = map[string]string{
	"host": "ssh alias or user@host for the sandbox; ~/.ssh/config resolves key, user, port.\n" +
		"# Required, and asked for by `sand config init`: there is no sensible default.",
	"remote_dir": "base dir on the sandbox. Per-PR files land in\n" +
		"# <remote_dir>/<owner>/<repo>/pr-<number>/.",
	// The harness names come off the harnesses table rather than being retyped, so adding
	// one cannot leave this comment naming a set that is no longer the set.
	"harness": "agent CLI `comments pull` starts on the box to work the threads: " +
		strings.Join(harnessNames(), " or ") + ".\n" +
		"# `pull --no-agent` starts nothing; `pull --agent '<cmd>'` runs something else once.",
	"model": "model to run it with, in that harness's own spelling. Empty means the\n" +
		"# harness's default.",
	"branch_prefix": "what `sand new` puts before <issue>-<title>, and what `sand up` reads\n" +
		"# the issue number back out of. Unset means $USER.",
}

// configField is one settable key: its name in the file and the field behind it.
type configField struct {
	Key string
	ptr *string
}

// configFields reads the keys off the struct tags, in declaration order, so adding a
// field to Config makes it settable, printable and documented without touching the
// command. Non-string fields are skipped rather than mishandled; there are none yet.
func configFields(c *Config) []configField {
	v := reflect.ValueOf(c).Elem()
	t := v.Type()
	out := make([]configField, 0, t.NumField())
	for i := range t.NumField() {
		key, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if key == "" || key == "-" || v.Field(i).Kind() != reflect.String {
			continue
		}
		out = append(out, configField{Key: key, ptr: v.Field(i).Addr().Interface().(*string)})
	}
	return out
}

// ConfigKeys is what `config set` accepts, for help text and error messages.
func ConfigKeys() []string {
	var c Config
	var keys []string
	for _, f := range configFields(&c) {
		keys = append(keys, f.Key)
	}
	return keys
}

// Get returns the effective value of one key: the file, with env and the defaults applied,
// which is what another program (the Makefile) needs to reach the same box this tool would.
// An unset host comes back empty rather than as an error, because empty is the true answer
// and the Makefile already refuses a blank box with a better message than this one could.
func Get(key string) (string, error) {
	c, err := effective()
	if err != nil {
		return "", err
	}
	for _, f := range configFields(&c) {
		if f.Key == key {
			return *f.ptr, nil
		}
	}
	return "", fmt.Errorf("unknown config key %q; known keys: %s", key, strings.Join(ConfigKeys(), ", "))
}

// Set writes key/value pairs to the config file, creating it if it does not exist and
// leaving every other key as it was. An unknown key is an error rather than a line in the
// file nothing reads.
func Set(pairs [][2]string) (string, error) {
	p := ConfigPath()
	c, err := loadFile()
	if err != nil {
		return p, err
	}
	fields := configFields(&c)
	for _, kv := range pairs {
		found := false
		for _, f := range fields {
			if f.Key == kv[0] {
				*f.ptr, found = kv[1], true
				break
			}
		}
		if !found {
			return p, fmt.Errorf("unknown config key %q; known keys: %s", kv[0], strings.Join(ConfigKeys(), ", "))
		}
	}
	return p, writeConfig(c)
}

// InitConfig creates the config file, or brings an existing one up to date: every key this
// version of Config has, with this version's comments, and every value the file already
// held. Running it again writes the same bytes, so a setup script, a fresh Mac and a Mac
// that has been through three versions of the tool all end up at the same file, and none of
// them has to know which case it is in. That is worth more than the old behaviour of
// refusing to touch a file that exists: refusing means there is no command that adds a key
// added since the file was written.
//
// It writes no defaulted value, for the same reason `Set` does not: a `remote_dir:` line
// holding today's default is indistinguishable from one the operator chose, so tomorrow's
// default would never reach this machine. The keys are present and empty, with the default
// named in the comment above them.
//
// The host is the exception, being the one key with no default. It is asked for when
// neither the flag nor the file already answers.
func InitConfig(host string, in io.Reader, out io.Writer) (string, error) {
	p := ConfigPath()
	c, err := loadFile()
	if err != nil {
		return p, err
	}
	if host != "" {
		c.Host = host
	}
	if c.Host == "" {
		var answers *bufio.Reader
		if in != nil {
			answers = bufio.NewReader(in)
		}
		c.Host = ask(answers, out, "sandbox ssh alias or user@host", "")
	}
	return p, writeConfig(c)
}

// ask reads one line, showing current as what an empty answer keeps. An empty answer, EOF or
// no reader at all comes back as current, so an unattended run (a setup script, a test) writes
// the file it would have written rather than blocking on a prompt nobody is there to answer or
// inventing a value.
//
// The reader is the caller's, made once per run and shared between questions, because a
// bufio.Reader per question reads ahead and eats the next one's answer. See confirm in sign.go,
// where that silently turned a "yes, push" into a branch left unpushed.
func ask(in *bufio.Reader, out io.Writer, question, current string) string {
	if in == nil {
		return current
	}
	if current != "" {
		fmt.Fprintf(out, "%s [%s]: ", question, current)
	} else {
		fmt.Fprintf(out, "%s: ", question)
	}
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(out)
		return current
	}
	if answer := strings.TrimSpace(line); answer != "" {
		return answer
	}
	return current
}

// writeConfig renders the whole file: one commented key per Config field, values as they
// are. Every key is always present, so the file doubles as the list of what can be set.
func writeConfig(c Config) error {
	var b strings.Builder
	b.WriteString("# sand config\n")
	defaults := configDefaults()
	for _, f := range configFields(&c) {
		if doc := configDoc[f.Key]; doc != "" {
			fmt.Fprintf(&b, "# %s: %s\n", f.Key, doc)
		}
		// The default is named here rather than written as the value, so an empty key
		// still tells the reader what it will do and a later change to the default
		// still reaches this machine.
		if d := defaults[f.Key]; d != "" {
			fmt.Fprintf(&b, "# Unset means %s.\n", d)
		}
		v := *f.ptr
		if v == "" {
			// An empty value would read back as "unset"; say so rather than write `host: ""`.
			fmt.Fprintf(&b, "%s:\n", f.Key)
			continue
		}
		// yaml quotes whatever needs it, so a value with a colon or a leading ~ survives
		// the round trip that hand-rolled printing would break.
		enc, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s: %s", f.Key, enc)
	}
	p := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(b.String()), 0o600)
}
