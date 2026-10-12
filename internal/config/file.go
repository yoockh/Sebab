package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
)

// ErrNotFound is returned by Load when the config file does not exist.
var ErrNotFound = errors.New("config file not found")

// header is written at the top of every file Sebab writes.
const header = `# Sebab configuration. Edited by "sebab monitor" and the Noctalia panel.
# Comments are not preserved when Sebab rewrites this file; the previous
# version is kept next to it as config.toml.bak.
# Secrets never live here: auth.secret is a reference to the keyring,
# the 0600 secrets file, or an environment variable (env:NAME).

`

// Parse decodes TOML bytes and validates the result.
func Parse(data []byte) (*Config, error) {
	var c Config
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return nil, &ValidationError{Problems: []string{"unknown field: " + strict.String()}}
		}
		var de *toml.DecodeError
		if errors.As(err, &de) {
			row, col := de.Position()
			return nil, &ValidationError{Problems: []string{fmt.Sprintf("line %d column %d: %s", row, col, de.Error())}}
		}
		return nil, &ValidationError{Problems: []string{err.Error()}}
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

// LoadOrEmpty is like Load but returns an empty config when the file is missing.
func LoadOrEmpty(path string) (*Config, error) {
	c, err := Load(path)
	if errors.Is(err, ErrNotFound) {
		return &Config{}, nil
	}
	return c, err
}

// Marshal encodes the config with the standard header.
func (c *Config) Marshal() ([]byte, error) {
	body, err := toml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return append([]byte(header), body...), nil
}

// Save validates and atomically writes the config, keeping a .bak of the
// previous file. The file is written with mode 0600 because URLs can carry
// sensitive query parameters.
func (c *Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := c.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if old, err := os.ReadFile(path); err == nil {
		if err := WriteFileAtomic(path+".bak", old, 0o600); err != nil {
			return err
		}
	}
	return WriteFileAtomic(path, data, 0o600)
}

// WriteFileAtomic writes data to a temp file in the same directory, syncs it and
// renames it over path, so readers never observe a partial file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("replace %s: %w", path, err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// Find returns the index of a monitor by id, or -1.
func (c *Config) Find(id string) int {
	for i, m := range c.Monitors {
		if m.ID == id {
			return i
		}
	}
	return -1
}

// Add appends a monitor after validating the result.
func (c *Config) Add(m Monitor) error {
	if c.Find(m.ID) >= 0 {
		return fmt.Errorf("monitor %q already exists", m.ID)
	}
	c.Monitors = append(c.Monitors, m)
	if err := c.Validate(); err != nil {
		c.Monitors = c.Monitors[:len(c.Monitors)-1]
		return err
	}
	return nil
}

// Replace swaps the monitor with the given id for m (m.ID may differ).
func (c *Config) Replace(id string, m Monitor) error {
	i := c.Find(id)
	if i < 0 {
		return fmt.Errorf("monitor %q not found", id)
	}
	old := c.Monitors[i]
	c.Monitors[i] = m
	if err := c.Validate(); err != nil {
		c.Monitors[i] = old
		return err
	}
	return nil
}

// Remove deletes a monitor by id.
func (c *Config) Remove(id string) error {
	i := c.Find(id)
	if i < 0 {
		return fmt.Errorf("monitor %q not found", id)
	}
	c.Monitors = append(c.Monitors[:i], c.Monitors[i+1:]...)
	return nil
}

// UniqueID derives an unused id from a name.
func (c *Config) UniqueID(name string) string {
	base := Slugify(name)
	id := base
	for n := 2; c.Find(id) >= 0; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}
