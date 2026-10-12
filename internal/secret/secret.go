// Package secret stores and resolves credentials. Secrets live in the Secret
// Service keyring when one is running; the fallbacks are environment variable
// references (env:NAME) and a 0600 secrets file with a visible warning.
// config.toml only ever holds the reference.
package secret

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/zalando/go-keyring"
)

// Service is the keyring service name; the secret name is the keyring user.
const Service = "sebab"

// ErrNotFound means no backend has the secret.
var ErrNotFound = errors.New("secret not found")

// Keyring is the subset of go-keyring Sebab uses (swappable in tests).
type Keyring interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

// SystemKeyring talks to the Secret Service over D-Bus via go-keyring.
type SystemKeyring struct{}

// Get implements Keyring.
func (SystemKeyring) Get(s, u string) (string, error) { return keyring.Get(s, u) }

// Set implements Keyring.
func (SystemKeyring) Set(s, u, p string) error { return keyring.Set(s, u, p) }

// Delete implements Keyring.
func (SystemKeyring) Delete(s, u string) error { return keyring.Delete(s, u) }

// Backend names reported to the user.
const (
	BackendKeyring = "keyring"
	BackendFile    = "file"
	BackendEnv     = "env"
)

// Store resolves references and writes secrets to the best available backend.
type Store struct {
	Keyring   Keyring
	FilePath  string // 0600 fallback file
	Getenv    func(string) string
	probeOnce sync.Once
	probeErr  error
}

// KeyringAvailable reports whether a Secret Service provider answers. The result
// is cached for the life of the Store.
func (s *Store) KeyringAvailable() error {
	s.probeOnce.Do(func() {
		if s.Keyring == nil {
			s.probeErr = errors.New("no keyring configured")
			return
		}
		_, err := s.Keyring.Get(Service, "__sebab_probe__")
		if err != nil && !errors.Is(err, keyring.ErrNotFound) {
			s.probeErr = fmt.Errorf("no Secret Service provider: %w", err)
		}
	})
	return s.probeErr
}

// IsEnvRef reports whether ref points at an environment variable.
func IsEnvRef(ref string) bool { return strings.HasPrefix(ref, "env:") }

// Resolve returns the secret value for a reference and the backend that had it.
func (s *Store) Resolve(ref string) (string, string, error) {
	if IsEnvRef(ref) {
		name := strings.TrimPrefix(ref, "env:")
		if v := s.Getenv(name); v != "" {
			return v, BackendEnv, nil
		}
		return "", "", fmt.Errorf("%w: environment variable %s is empty or unset", ErrNotFound, name)
	}
	if s.KeyringAvailable() == nil {
		v, err := s.Keyring.Get(Service, ref)
		if err == nil {
			return v, BackendKeyring, nil
		}
		if !errors.Is(err, keyring.ErrNotFound) {
			return "", "", fmt.Errorf("keyring lookup for %q failed: %w", ref, err)
		}
	}
	values, err := s.readFile()
	if err != nil {
		return "", "", err
	}
	if v, ok := values[ref]; ok {
		return v, BackendFile, nil
	}
	return "", "", fmt.Errorf("%w: %q (store it with 'sebab secret set %s')", ErrNotFound, ref, ref)
}

// Set stores a secret. backend "" picks the keyring when available, else the
// file. It returns the backend used.
func (s *Store) Set(name, value, backend string) (string, error) {
	if IsEnvRef(name) {
		return "", errors.New("env: references are read from the environment and cannot be stored")
	}
	if value == "" {
		return "", errors.New("refusing to store an empty secret")
	}
	if backend == "" || backend == BackendKeyring {
		if err := s.KeyringAvailable(); err == nil {
			if err := s.Keyring.Set(Service, name, value); err != nil {
				return "", fmt.Errorf("store in keyring: %w", err)
			}
			_ = s.deleteFromFile(name) // keep a single copy
			return BackendKeyring, nil
		} else if backend == BackendKeyring {
			return "", err
		}
	}
	values, err := s.readFile()
	if err != nil {
		return "", err
	}
	values[name] = value
	return BackendFile, s.writeFile(values)
}

// Delete removes a secret from every backend that has it.
func (s *Store) Delete(name string) error {
	found := false
	if s.KeyringAvailable() == nil {
		if err := s.Keyring.Delete(Service, name); err == nil {
			found = true
		} else if !errors.Is(err, keyring.ErrNotFound) {
			return fmt.Errorf("delete from keyring: %w", err)
		}
	}
	if err := s.deleteFromFile(name); err == nil {
		found = true
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	if !found {
		return fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	return nil
}

// FileNames lists the names stored in the fallback file (never values).
func (s *Store) FileNames() ([]string, error) {
	values, err := s.readFile()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(values))
	for k := range values {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}

func (s *Store) deleteFromFile(name string) error {
	values, err := s.readFile()
	if err != nil {
		return err
	}
	if _, ok := values[name]; !ok {
		return ErrNotFound
	}
	delete(values, name)
	return s.writeFile(values)
}

func (s *Store) readFile() (map[string]string, error) {
	values := map[string]string{}
	if s.FilePath == "" {
		return values, nil
	}
	data, err := os.ReadFile(s.FilePath)
	if errors.Is(err, os.ErrNotExist) {
		return values, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read secrets file: %w", err)
	}
	if st, err := os.Stat(s.FilePath); err == nil && st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("secrets file %s is readable by others (mode %o); run: chmod 600 %s",
			s.FilePath, st.Mode().Perm(), s.FilePath)
	}
	var doc struct {
		Secrets map[string]string `toml:"secrets"`
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse secrets file: %w", err)
	}
	for k, v := range doc.Secrets {
		values[k] = v
	}
	return values, nil
}

const fileHeader = `# Sebab fallback secret store. Used only when no Secret Service keyring
# (gnome-keyring, KeePassXC, KWallet) is running. Keep this file mode 0600.
# Prefer a keyring: see 'sebab doctor'.

`

func (s *Store) writeFile(values map[string]string) error {
	if s.FilePath == "" {
		return errors.New("no secrets file configured")
	}
	body, err := toml.Marshal(struct {
		Secrets map[string]string `toml:"secrets"`
	}{values})
	if err != nil {
		return fmt.Errorf("encode secrets file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.FilePath), 0o700); err != nil {
		return fmt.Errorf("create secrets dir: %w", err)
	}
	return writePrivate(s.FilePath, append([]byte(fileHeader), body...))
}

// writePrivate writes atomically with mode 0600 from creation on.
func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".secrets-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	name := tmp.Name() // CreateTemp uses mode 0600
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("write secrets file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("sync secrets file: %w", err)
	}
	tmp.Close()
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("replace secrets file: %w", err)
	}
	return nil
}
