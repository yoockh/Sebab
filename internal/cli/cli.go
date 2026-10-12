// Package cli implements the sebab command line. It is separate from cmd/sebab so
// that every command can be driven from tests with injected I/O.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/yoockh/sebab/internal/version"
)

// Exit codes shared by all commands. `sebab check` adds per-layer codes on top.
const (
	ExitOK       = 0
	ExitUsage    = 2
	ExitInternal = 3
)

// Env is the process environment a command runs in.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	hooks  *hooks // tests only
}

// OSEnv returns the real process environment.
func OSEnv() Env {
	return Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv}
}

type command struct {
	summary string
	run     func(ctx context.Context, env Env, args []string) int
}

func commands() map[string]command {
	return map[string]command{
		"check":        {"check one URL or host:port now and explain any failure", runCheck},
		"monitor":      {"add, list, show, edit, rm, pause or resume monitors", runMonitor},
		"secret":       {"set, rm, list secrets (values only via stdin) or create a handoff file", runSecret},
		"watch":        {"stream engine events as JSON lines (attaches to a running engine)", runWatch},
		"doctor":       {"explain what is missing (keyring, notifications, network, engine)", runDoctor},
		"refresh":      {"ask the running engine to check now (all monitors, or one id)", runRefresh},
		"report":       {"print a redacted Markdown diagnosis for a monitor", runReport},
		"kill-session": {"stop a 'sebab watch --session' process (used by frontends)", runKillSession},
		"version":      {"print the version", runVersion},
	}
}

// Run executes the command line and returns the process exit code.
func Run(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		usage(env.Stdout)
		return ExitOK
	}
	if args[0] == "--version" {
		return runVersion(ctx, env, nil)
	}
	cmd, ok := commands()[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "sebab: unknown command %q\n\n", args[0])
		usage(env.Stderr)
		return ExitUsage
	}
	return cmd.run(ctx, env, args[1:])
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Sebab: don't just know it's down. Know why.")
	fmt.Fprintln(w, "\nUsage: sebab <command> [flags]")
	fmt.Fprintln(w, "\nCommands:")
	cmds := commands()
	names := make([]string, 0, len(cmds))
	for name := range cmds {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "  %-13s %s\n", name, cmds[name].summary)
	}
	fmt.Fprintln(w, "\nRun 'sebab <command> -h' for command flags.")
}

func runVersion(_ context.Context, env Env, _ []string) int {
	fmt.Fprintf(env.Stdout, "sebab %s\n", version.Version)
	return ExitOK
}

// newFlagSet returns a flag set that reports errors instead of exiting.
func newFlagSet(name string, env Env) *flag.FlagSet {
	fs := flag.NewFlagSet("sebab "+name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	return fs
}

// parseFlags parses args and maps -h and parse errors to exit codes. The second
// return value is false when the caller should stop and return the code.
func parseFlags(fs *flag.FlagSet, args []string) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK, false
		}
		return ExitUsage, false
	}
	return ExitOK, true
}

// usageError prints a message and returns the usage exit code.
func usageError(env Env, format string, a ...any) int {
	fmt.Fprintf(env.Stderr, "sebab: "+strings.TrimRight(format, "\n")+"\n", a...)
	return ExitUsage
}
