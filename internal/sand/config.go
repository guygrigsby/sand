package sand

import (
	"fmt"
	"os"
	"path/filepath"

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

// Load reads the config file. A missing file is not an error — flags or env may cover it.
func Load() (Config, error) {
	c := Config{Host: defaultHost, RemoteDir: defaultRemoteDir}
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

// WriteDefault creates the config file with a commented template, refusing to clobber
// an existing one.
func WriteDefault(host string) (string, error) {
	if host == "" {
		host = defaultHost
	}
	p := ConfigPath()
	if _, err := os.Stat(p); err == nil {
		return p, fmt.Errorf("%s already exists", p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return p, err
	}
	body := fmt.Sprintf(`# sand config
# host: ssh alias or user@host for the sandbox; ~/.ssh/config resolves key, user, port.
host: %s
# remote_dir: base dir on the sandbox. Per-PR files land in
# <remote_dir>/<owner>/<repo>/pr-<number>/.
remote_dir: %s
`, host, defaultRemoteDir)
	return p, os.WriteFile(p, []byte(body), 0o600)
}
