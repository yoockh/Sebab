package secret

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

const canary = "canary-Secret+/=\"value-42"

type memKeyring struct {
	data map[string]string
	err  error
}

func (m *memKeyring) Get(s, u string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	v, ok := m.data[s+"/"+u]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (m *memKeyring) Set(s, u, p string) error {
	if m.err != nil {
		return m.err
	}
	m.data[s+"/"+u] = p
	return nil
}

func (m *memKeyring) Delete(s, u string) error {
	if _, ok := m.data[s+"/"+u]; !ok {
		return keyring.ErrNotFound
	}
	delete(m.data, s+"/"+u)
	return nil
}

func newStore(t *testing.T, kr Keyring) *Store {
	return &Store{Keyring: kr, FilePath: filepath.Join(t.TempDir(), "secrets.toml"),
		Getenv: func(k string) string { return map[string]string{"API_TOKEN": "from-env"}[k] }}
}

func TestKeyringPreferred(t *testing.T) {
	kr := &memKeyring{data: map[string]string{}}
	s := newStore(t, kr)
	backend, err := s.Set("api", canary, "")
	if err != nil || backend != BackendKeyring {
		t.Fatalf("backend %q err %v", backend, err)
	}
	if _, err := os.Stat(s.FilePath); !errors.Is(err, os.ErrNotExist) {
		t.Error("file must not be written when the keyring works")
	}
	v, from, err := s.Resolve("api")
	if err != nil || v != canary || from != BackendKeyring {
		t.Fatalf("resolve %q %q %v", v, from, err)
	}
	if err := s.Delete("api"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Resolve("api"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestFileFallbackWhenNoKeyring(t *testing.T) {
	s := newStore(t, &memKeyring{err: errors.New("org.freedesktop.secrets was not provided")})
	if s.KeyringAvailable() == nil {
		t.Fatal("keyring should be unavailable")
	}
	backend, err := s.Set("api", canary, "")
	if err != nil || backend != BackendFile {
		t.Fatalf("backend %q err %v", backend, err)
	}
	st, err := os.Stat(s.FilePath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file mode %v err %v", st.Mode(), err)
	}
	v, from, err := s.Resolve("api")
	if err != nil || v != canary || from != BackendFile {
		t.Fatalf("resolve %q %q %v", v, from, err)
	}
	names, _ := s.FileNames()
	if len(names) != 1 || names[0] != "api" {
		t.Errorf("names %v", names)
	}
	if _, err := s.Set("x", "y-value", BackendKeyring); err == nil {
		t.Error("forcing the keyring backend should fail when unavailable")
	}
	_ = os.Chmod(s.FilePath, 0o644)
	if _, _, err := s.Resolve("api"); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("world-readable secrets file must be refused, got %v", err)
	}
}

func TestEnvReference(t *testing.T) {
	s := newStore(t, &memKeyring{data: map[string]string{}})
	v, from, err := s.Resolve("env:API_TOKEN")
	if err != nil || v != "from-env" || from != BackendEnv {
		t.Fatalf("got %q %q %v", v, from, err)
	}
	if _, _, err := s.Resolve("env:MISSING"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing env should be ErrNotFound: %v", err)
	}
	if _, err := s.Set("env:X", "v", ""); err == nil {
		t.Error("cannot store env references")
	}
}

func TestHandoffLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "handoff")
	now := time.Now()
	path, err := CreateHandoff(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	dst, _ := os.Stat(dir)
	if st.Mode().Perm() != 0o600 || dst.Mode().Perm() != 0o700 {
		t.Fatalf("modes file %o dir %o", st.Mode().Perm(), dst.Mode().Perm())
	}
	// The panel overwrites with a truncating write, like noctalia.writeFile.
	if err := os.WriteFile(path, []byte(canary+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("truncating write changed the mode to %o", st.Mode().Perm())
	}
	v, err := ReadHandoff(path, []string{dir}, true)
	if err != nil || v != canary {
		t.Fatalf("keep read %q %v", v, err)
	}
	v, err = ReadHandoff(path, []string{dir}, false)
	if err != nil || v != canary {
		t.Fatalf("consume read %q %v", v, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("handoff file must be deleted after reading")
	}
}

func TestHandoffRejections(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "handoff")
	path, _ := CreateHandoff(dir, time.Now())
	if _, err := ReadHandoff(path, []string{t.TempDir()}, false); err == nil {
		t.Error("file outside allowed dirs must be rejected")
	}
	other := filepath.Join(dir, "notes.txt")
	_ = os.WriteFile(other, []byte("x"), 0o600)
	if _, err := ReadHandoff(other, []string{dir}, false); err == nil {
		t.Error("non-handoff names must be rejected")
	}
	_ = os.WriteFile(path, []byte(canary), 0o600)
	_ = os.Chmod(path, 0o644)
	if _, err := ReadHandoff(path, []string{dir}, false); err == nil {
		t.Error("group/world readable handoff must be rejected")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("an exposed handoff file must be deleted")
	}
	link := filepath.Join(dir, "handoff-link")
	target, _ := CreateHandoff(dir, time.Now())
	_ = os.Symlink(target, link)
	if _, err := ReadHandoff(link, []string{dir}, false); err == nil {
		t.Error("symlinks must be rejected")
	}
	empty, _ := CreateHandoff(dir, time.Now())
	if _, err := ReadHandoff(empty, []string{dir}, false); err == nil {
		t.Error("empty secret must be rejected")
	}
}

func TestStaleHandoffsCleaned(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "handoff")
	old, _ := CreateHandoff(dir, time.Now())
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(old, past, past)
	if _, err := CreateHandoff(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Error("stale handoff file should be removed")
	}
}

func TestReadSecretStdin(t *testing.T) {
	if v, err := ReadSecret(strings.NewReader(canary + "\r\n")); err != nil || v != canary {
		t.Fatalf("%q %v", v, err)
	}
	for _, bad := range []string{"", "\n", "a\nb"} {
		if _, err := ReadSecret(strings.NewReader(bad)); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if _, err := ReadSecret(strings.NewReader(strings.Repeat("x", MaxSecretBytes+1))); err == nil {
		t.Error("oversized secret should be rejected")
	}
}

func TestRedactorForms(t *testing.T) {
	r := NewRedactor()
	r.Add(canary)
	r.AddBasic("lab", canary)
	r.Add("ab") // too short to redact safely
	for _, in := range []string{
		"token " + canary,
		`{"message":"` + strings.ReplaceAll(canary, `"`, `\"`) + `"}`,
		"https://x.example/?t=" + url.QueryEscape(canary),
		"Basic bGFiOmNhbmFyeS1TZWNyZXQrLz0idmFsdWUtNDI=",
	} {
		out := r.Scrub(in)
		if strings.Contains(out, "canary-Secret") || strings.Contains(out, "bGFiOmNhbmFyeS1TZWNyZXQr") {
			t.Errorf("leak: %q -> %q", in, out)
		}
		if !strings.Contains(out, Mask) {
			t.Errorf("no mask in %q", out)
		}
	}
	if got := r.Scrub("tab"); got != "tab" {
		t.Errorf("short values must not be redacted: %q", got)
	}
	if got := string(r.ScrubBytes([]byte("x" + canary))); got != "x"+Mask {
		t.Errorf("bytes %q", got)
	}
	var nilR *Redactor
	if nilR.Scrub("a") != "a" {
		t.Error("nil redactor is a no-op")
	}
}

func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com/health":                   "https://api.example.com/health",
		"https://api.example.com/x?api_key=abc&page=2":     "https://api.example.com/x?api_key=[REDACTED]&page=2",
		"https://api.example.com/x?access_token=abc":       "https://api.example.com/x?access_token=[REDACTED]",
		"https://u:pw@api.example.com/":                    "https://u:[REDACTED]@api.example.com/",
		"tcp://db.example:5432":                            "tcp://db.example:5432",
		"https://api.example.com/x?Signature=s&X-Amz-Date": "https://api.example.com/x?Signature=[REDACTED]&X-Amz-Date=",
	}
	for in, want := range cases {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
	}
}
