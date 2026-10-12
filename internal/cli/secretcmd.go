package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/yoockh/sebab/internal/secret"
)

func runSecret(_ context.Context, env Env, args []string) int {
	if len(args) == 0 {
		return usageError(env, "secret: subcommand required: set, rm, list, handoff")
	}
	switch args[0] {
	case "set":
		return secretSet(env, args[1:])
	case "rm", "remove", "delete":
		return secretRemove(env, args[1:])
	case "list", "ls":
		return secretList(env, args[1:])
	case "handoff":
		return secretHandoff(env, args[1:])
	}
	return usageError(env, "secret: unknown subcommand %q", args[0])
}

// secretSet stores a secret read from stdin (or a handoff file). The value is
// never accepted as an argument, so it cannot leak through /proc/*/cmdline.
func secretSet(env Env, args []string) int {
	fs := newFlagSet("secret set", env)
	backend := fs.String("backend", "", "keyring or file (default: keyring when available)")
	fromFile := fs.String("from-file", "", "read the value from a 'sebab secret handoff' file (deleted after reading)")
	jsonOut := fs.Bool("json", false, "print JSON")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: sebab secret set NAME < file   (or pipe the value in; it is never an argument)")
		fs.PrintDefaults()
	}
	if code, ok := parseFlags(fs, reorder(args)); !ok {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(env, "secret set: exactly one NAME is required; the value is read from stdin")
	}
	name := fs.Arg(0)
	if *backend != "" && *backend != secret.BackendKeyring && *backend != secret.BackendFile {
		return usageError(env, "secret set: --backend must be keyring or file")
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	var value string
	if *fromFile != "" {
		value, err = secret.ReadHandoff(*fromFile, handoffDirs(rt, *fromFile), false)
	} else {
		if f, ok := env.Stdin.(*os.File); ok && isTerminal(f) {
			fmt.Fprintf(env.Stderr, "Paste the secret for %q and press Enter (input is not hidden; prefer a pipe):\n", name)
		}
		value, err = secret.ReadSecret(env.Stdin)
	}
	if err != nil {
		return fail(env, *jsonOut, rt.redactor, ExitUsage, "secret set: "+err.Error())
	}
	rt.redactor.Add(value)
	used, err := rt.store.Set(name, value, *backend)
	if err != nil {
		return fail(env, *jsonOut, rt.redactor, ExitInternal, "secret set: "+err.Error())
	}
	warning := ""
	if used == secret.BackendFile {
		warning = fileFallbackWarning(rt)
	}
	if *jsonOut {
		return printJSON(env, rt.redactor, result{OK: true, ID: name, Backend: used, Warning: warning})
	}
	if warning != "" {
		fmt.Fprintln(env.Stderr, "sebab: warning: "+warning)
	}
	fmt.Fprintf(env.Stdout, "Stored secret '%s' in the %s.\n", name, used)
	return ExitOK
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func secretRemove(env Env, args []string) int {
	fs := newFlagSet("secret rm", env)
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		return usageError(env, "secret rm: NAME is required")
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	if err := rt.store.Delete(fs.Arg(0)); err != nil {
		if errors.Is(err, secret.ErrNotFound) {
			return fail(env, false, rt.redactor, ExitUsage, err.Error())
		}
		return fail(env, false, rt.redactor, ExitInternal, err.Error())
	}
	fmt.Fprintf(env.Stdout, "Removed secret '%s'.\n", fs.Arg(0))
	return ExitOK
}

// secretList shows the secret references used by monitors and whether each
// resolves. Values are never printed.
func secretList(env Env, args []string) int {
	fs := newFlagSet("secret list", env)
	jsonOut := fs.Bool("json", false, "print JSON")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	cfg, err := rt.loadConfig()
	if err != nil {
		return fail(env, *jsonOut, rt.redactor, ExitUsage, validationProblems(err)...)
	}
	type row struct {
		Name     string   `json:"name"`
		Backend  string   `json:"backend,omitempty"`
		Status   string   `json:"status"`
		Monitors []string `json:"monitors"`
	}
	refs := map[string][]string{}
	for _, m := range cfg.Monitors {
		if m.Auth != nil {
			refs[m.Auth.Secret] = append(refs[m.Auth.Secret], m.ID)
		}
	}
	names, _ := rt.store.FileNames()
	for _, n := range names {
		if _, ok := refs[n]; !ok {
			refs[n] = []string{}
		}
	}
	keys := make([]string, 0, len(refs))
	for k := range refs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := []row{}
	for _, k := range keys {
		r := row{Name: k, Monitors: refs[k], Status: "ok"}
		v, backend, err := rt.store.Resolve(k)
		if err != nil {
			r.Status = "missing"
		} else {
			rt.redactor.Add(v)
			r.Backend = backend
		}
		rows = append(rows, r)
	}
	if *jsonOut {
		return printJSON(env, rt.redactor, rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(env.Stdout, "No secrets referenced.")
	}
	for _, r := range rows {
		fmt.Fprintf(env.Stdout, "%-24s %-8s %-8s used by: %v\n", r.Name, r.Status, dash(r.Backend), r.Monitors)
	}
	return ExitOK
}

// secretHandoff prints the path of a fresh 0600 handoff file (Noctalia panel).
func secretHandoff(env Env, args []string) int {
	fs := newFlagSet("secret handoff", env)
	fallback := fs.String("fallback-dir", "", "directory to use when $XDG_RUNTIME_DIR is unset (the plugin data dir)")
	if code, ok := parseFlags(fs, args); !ok {
		return code
	}
	rt, err := newRuntime(env)
	if err != nil {
		return errorf(env, "%v", err)
	}
	if *fallback != "" && !filepath.IsAbs(*fallback) {
		return usageError(env, "secret handoff: --fallback-dir must be absolute")
	}
	dir := handoffDir(rt, *fallback)
	if dir == "" {
		return errorf(env, "secret handoff: $XDG_RUNTIME_DIR is unset and no --fallback-dir was given")
	}
	path, err := secret.CreateHandoff(dir, time.Now())
	if err != nil {
		return errorf(env, "secret handoff: %v", err)
	}
	fmt.Fprintln(env.Stdout, path)
	return ExitOK
}
