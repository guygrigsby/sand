package sand

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is ~/.config/sand/config.yaml. Flags and env override it; see Resolve.
type Config struct {
	Host      string `yaml:"host"`       // ssh alias or user@host for the sandbox
	RemoteDir string `yaml:"remote_dir"` // base dir on the sandbox, ~ allowed
}

const (
	// There is exactly one sandbox, and this is the alias the Mac reaches it by, so it
	// is a default rather than something to configure before the tool works at all.
	defaultHost      = "guy-llm-sandbox"
	defaultRemoteDir = "~/.sand"
)

func ConfigPath() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "sand", "config.yaml")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "sand", "config.yaml")
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

// Load reads the config file. A missing file is not an error — flags or env may cover it.
func Load() (Config, error) {
	c, err := loadFile()
	if err != nil {
		return Config{Host: defaultHost, RemoteDir: defaultRemoteDir}, err
	}
	if c.Host == "" {
		c.Host = defaultHost
	}
	if c.RemoteDir == "" {
		c.RemoteDir = defaultRemoteDir
	}
	return c, nil
}

// Resolve settles host and remote dir: flag, then env, then config file.
func Resolve(hostFlag, remoteDirFlag string) (Config, error) {
	c, err := Load()
	if err != nil {
		return c, err
	}
	if v := os.Getenv("SAND_HOST"); v != "" {
		c.Host = v
	}
	if v := os.Getenv("SAND_REMOTE_DIR"); v != "" {
		c.RemoteDir = v
	}
	if hostFlag != "" {
		c.Host = hostFlag
	}
	if remoteDirFlag != "" {
		c.RemoteDir = remoteDirFlag
	}
	if c.Host == "" {
		return c, fmt.Errorf("no sandbox host: pass --host, set SAND_HOST, or write %s", ConfigPath())
	}
	return c, nil
}

// configDoc is the comment written above each key. The file is rendered from the struct
// and this, rather than round-tripped, so `config set` cannot lose the documentation and a
// new field needs nothing here but its own line.
var configDoc = map[string]string{
	"host": "ssh alias or user@host for the sandbox; ~/.ssh/config resolves key, user, port.",
	"remote_dir": "base dir on the sandbox. Per-PR files land in\n" +
		"# <remote_dir>/<owner>/<repo>/pr-<number>/.",
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

// WriteDefault creates the config file with a commented template, refusing to clobber
// an existing one.
func WriteDefault(host string) (string, error) {
	p := ConfigPath()
	if _, err := os.Stat(p); err == nil {
		return p, fmt.Errorf("%s already exists", p)
	}
	c := Config{Host: host, RemoteDir: defaultRemoteDir}
	if c.Host == "" {
		c.Host = defaultHost
	}
	return p, writeConfig(c)
}

// writeConfig renders the whole file: one commented key per Config field, values as they
// are. Every key is always present, so the file doubles as the list of what can be set.
func writeConfig(c Config) error {
	var b strings.Builder
	b.WriteString("# sand config\n")
	for _, f := range configFields(&c) {
		if doc := configDoc[f.Key]; doc != "" {
			fmt.Fprintf(&b, "# %s: %s\n", f.Key, doc)
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
