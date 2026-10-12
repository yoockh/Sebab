package cli

import (
	"flag"
	"os"
	"path/filepath"

	"github.com/yoockh/sebab/internal/version"
)

type flagSet = *flag.FlagSet

// visited returns the names of flags given explicitly on the command line.
func visited(fs flagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

func versionString() string { return version.Version }

// fallbackHandoffDir is the subdirectory used when $XDG_RUNTIME_DIR is unset:
// the plugin passes its data dir and the engine creates this inside it.
const fallbackHandoffDir = "sebab-handoff"

// handoffDir picks where new handoff files go.
func handoffDir(rt *runtime, fallback string) string {
	if rt.paths.RuntimeDir != "" {
		return filepath.Join(rt.paths.RuntimeDir, "handoff")
	}
	if fallback != "" {
		return filepath.Join(fallback, fallbackHandoffDir)
	}
	return ""
}

// handoffDirs lists the directories a --secret-file may live in.
func handoffDirs(rt *runtime, secretFile string) []string {
	dirs := []string{}
	if rt.paths.RuntimeDir != "" {
		dirs = append(dirs, filepath.Join(rt.paths.RuntimeDir, "handoff"))
	}
	if secretFile != "" {
		parent := filepath.Dir(secretFile)
		if filepath.Base(parent) == fallbackHandoffDir {
			if st, err := os.Lstat(parent); err == nil && st.IsDir() && st.Mode().Perm() == 0o700 {
				dirs = append(dirs, parent)
			}
		}
	}
	return dirs
}
