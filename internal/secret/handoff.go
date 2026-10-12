package secret

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Handoff files let the Noctalia panel pass a secret to the engine without
// putting it in process arguments (Noctalia's runAsync has no stdin). The engine
// creates an empty 0600 file in a 0700 directory, the panel overwrites it (a
// truncating write keeps the mode), and the engine reads and deletes it.

// MaxSecretBytes bounds what a handoff file or stdin may contain.
const MaxSecretBytes = 16 << 10

// HandoffTTL is how long an unused handoff file may live before cleanup.
const HandoffTTL = 10 * time.Minute

const handoffPrefix = "handoff-"

// CreateHandoff makes dir (0700) if needed, removes stale handoff files, and
// returns the path of a new empty 0600 file.
func CreateHandoff(dir string, now time.Time) (string, error) {
	if err := ensurePrivateDir(dir); err != nil {
		return "", err
	}
	CleanStaleHandoffs(dir, now)
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random name: %w", err)
	}
	path := filepath.Join(dir, handoffPrefix+hex.EncodeToString(b[:]))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create handoff file: %w", err)
	}
	f.Close()
	return path, nil
}

func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create handoff dir: %w", err)
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("stat handoff dir: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("handoff dir %s is not a directory", dir)
	}
	if err := checkOwner(st); err != nil {
		return err
	}
	if st.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("chmod handoff dir: %w", err)
		}
	}
	return nil
}

// CleanStaleHandoffs removes handoff files older than HandoffTTL.
func CleanStaleHandoffs(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), handoffPrefix) {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > HandoffTTL {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// ReadHandoff reads and deletes a handoff file after checking that it is a
// regular file inside one of allowedDirs, owned by the current user, and not
// accessible by group or others. keep leaves the file in place (live test
// before save); the panel deletes it on cancel and the TTL cleanup catches leaks.
func ReadHandoff(path string, allowedDirs []string, keep bool) (string, error) {
	clean, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("handoff path: %w", err)
	}
	if !inAllowedDir(clean, allowedDirs) || !strings.HasPrefix(filepath.Base(clean), handoffPrefix) {
		return "", errors.New("secret file is not a sebab handoff file (create one with 'sebab secret handoff')")
	}
	st, err := os.Lstat(clean)
	if err != nil {
		return "", fmt.Errorf("secret file: %w", err)
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("secret file must be a regular file")
	}
	if err := checkOwner(st); err != nil {
		return "", err
	}
	if st.Mode().Perm()&0o077 != 0 {
		_ = os.Remove(clean)
		return "", fmt.Errorf("secret file had mode %o; it was deleted unread", st.Mode().Perm())
	}
	f, err := os.Open(clean)
	if err != nil {
		return "", fmt.Errorf("open secret file: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxSecretBytes+1))
	f.Close()
	if !keep {
		_ = os.Remove(clean)
	}
	if err != nil {
		return "", fmt.Errorf("read secret file: %w", err)
	}
	return normalize(data)
}

// ReadSecret reads a secret from r (stdin): one value, trailing newline removed.
func ReadSecret(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxSecretBytes+1))
	if err != nil {
		return "", fmt.Errorf("read secret: %w", err)
	}
	return normalize(data)
}

func normalize(data []byte) (string, error) {
	if len(data) > MaxSecretBytes {
		return "", fmt.Errorf("secret is larger than %d bytes", MaxSecretBytes)
	}
	v := strings.TrimRight(string(data), "\r\n")
	if v == "" {
		return "", errors.New("secret is empty")
	}
	if strings.ContainsAny(v, "\r\n") {
		return "", errors.New("secret must be a single line")
	}
	return v, nil
}

func inAllowedDir(path string, dirs []string) bool {
	for _, d := range dirs {
		if d == "" {
			continue
		}
		abs, err := filepath.Abs(d)
		if err == nil && filepath.Dir(path) == abs {
			return true
		}
	}
	return false
}

func checkOwner(st os.FileInfo) error {
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by the current user", st.Name())
	}
	return nil
}
