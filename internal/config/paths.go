package config

import (
	"errors"
	"path/filepath"
)

// Paths are the XDG locations Sebab uses.
type Paths struct {
	ConfigDir  string // $XDG_CONFIG_HOME/sebab
	ConfigFile string // ConfigDir/config.toml, or $SEBAB_CONFIG
	StateDir   string // $XDG_STATE_HOME/sebab (history, state)
	RuntimeDir string // $XDG_RUNTIME_DIR/sebab, empty when XDG_RUNTIME_DIR is unset
}

// ErrNoHome is returned when neither XDG variables nor HOME are set.
var ErrNoHome = errors.New("cannot determine config directory: HOME and XDG_CONFIG_HOME are unset")

// ResolvePaths computes paths from an environment lookup function.
func ResolvePaths(getenv func(string) string) (Paths, error) {
	home := getenv("HOME")
	cfgBase := getenv("XDG_CONFIG_HOME")
	if cfgBase == "" {
		if home == "" {
			return Paths{}, ErrNoHome
		}
		cfgBase = filepath.Join(home, ".config")
	}
	stateBase := getenv("XDG_STATE_HOME")
	if stateBase == "" && home != "" {
		stateBase = filepath.Join(home, ".local", "state")
	}
	p := Paths{ConfigDir: filepath.Join(cfgBase, "sebab")}
	p.ConfigFile = filepath.Join(p.ConfigDir, "config.toml")
	if override := getenv("SEBAB_CONFIG"); override != "" {
		p.ConfigFile = override
	}
	if stateBase != "" {
		p.StateDir = filepath.Join(stateBase, "sebab")
	}
	if rt := getenv("XDG_RUNTIME_DIR"); rt != "" {
		p.RuntimeDir = filepath.Join(rt, "sebab")
	}
	return p, nil
}

// SecretsFile is the 0600 fallback secret store next to the config.
func (p Paths) SecretsFile() string { return filepath.Join(p.ConfigDir, "secrets.toml") }

// SocketPath is the engine control socket, empty without a runtime dir.
func (p Paths) SocketPath() string {
	if p.RuntimeDir == "" {
		return ""
	}
	return filepath.Join(p.RuntimeDir, "sebab.sock")
}
