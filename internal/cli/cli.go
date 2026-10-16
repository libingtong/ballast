package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/libingtong/ballast/internal/hook"
	"github.com/libingtong/ballast/internal/sys"
)

// Main dispatches argv (without the program name) and returns the exit code.
//
// The hook branch is dispatched first and builds nothing but the hook's own
// dependencies; every other command goes through newApp, which is only
// reached after help handling (so --help never touches the file system).
func Main(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(argv) > 0 && argv[0] == "hook" {
		return runHook(argv[1:], stdin, stdout, stderr)
	}
	if len(argv) == 0 {
		fmt.Fprint(stderr, topUsage)
		return ExitUsage
	}
	name, args := argv[0], argv[1:]
	switch name {
	case "-h", "--help", "-help", "help":
		fmt.Fprint(stdout, topUsage)
		return ExitOK
	case "-v", "--version":
		name = "version"
	}
	cmd, ok := lookupCommand(name)
	if !ok {
		fmt.Fprintf(stderr, "ballast: unknown command %q\n\n%s", name, topUsage)
		return ExitUsage
	}
	if wantsHelp(args) {
		fmt.Fprint(stdout, cmd.usage)
		return ExitOK
	}
	a, err := newAppWith(stdin, stdout, stderr, wireSeams{Diagnostic: name == "doctor"})
	if err != nil {
		fmt.Fprintf(stderr, "ballast: %s\n", stripBallastPrefix(err.Error()))
		return ExitInfra
	}
	return cmd.run(a, cmd, args)
}

// runHook is the hot path: parse nothing, build the hook dependencies and
// hand over. Any failure before hook.Main takes over follows the hook
// protocol (empty stdout, "ballast:" on stderr, exit 1).
func runHook(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runHookWith(args, stdin, stdout, stderr, WireHook)
}

// runHookWith is runHook with the wiring function injected (tests).
func runHookWith(args []string, stdin io.Reader, stdout, stderr io.Writer, wire func(sys.Env) (*hook.Deps, error)) int {
	if wantsHelp(args) {
		fmt.Fprint(stdout, hookUsage)
		return ExitOK
	}
	deps, err := wire(sys.OSEnv{})
	if err != nil {
		fmt.Fprintf(stderr, "ballast: hook: %s\n", oneLine(stripBallastPrefix(err.Error())))
		return ExitHookError
	}
	return hook.Main(args, stdin, stdout, stderr, deps)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// wantsHelp reports whether args (before a "--" terminator) ask for help.
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		switch a {
		case "-h", "--help", "-help":
			return true
		}
	}
	return false
}

// app is the runtime context of every non-hook command.
type app struct {
	*core
	in   io.Reader
	out  io.Writer
	errw io.Writer
	cwd  string
}

func newApp(in io.Reader, out, errw io.Writer) (*app, error) {
	return newAppWith(in, out, errw, wireSeams{})
}

func newAppWith(in io.Reader, out, errw io.Writer, sm wireSeams) (*app, error) {
	c, err := newCoreWith(sys.OSEnv{}, sm)
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("cannot determine the working directory: %w", err)
	}
	return &app{core: c, in: in, out: out, errw: errw, cwd: cwd}, nil
}

// errorf prints "ballast: ..." to stderr and returns code.
//
// Wrapped sentinel errors (model.Err*) already carry their own "ballast: "
// prefix; it is dropped from the message so stderr shows exactly one.
func (a *app) errorf(code int, format string, args ...any) int {
	fmt.Fprintf(a.errw, "ballast: %s\n", stripBallastPrefix(fmt.Sprintf(format, args...)))
	return code
}

// stripBallastPrefix removes every "ballast: " marker from msg.
func stripBallastPrefix(msg string) string {
	return strings.ReplaceAll(msg, "ballast: ", "")
}

// usageErr reports a usage problem for command usage text and returns 2.
func (a *app) usageErr(c *command, format string, args ...any) int {
	fmt.Fprintf(a.errw, "ballast %s: "+format+"\n", append([]any{c.name}, args...)...)
	fmt.Fprint(a.errw, c.usage)
	return ExitUsage
}

type command struct {
	name  string
	usage string
	run   func(a *app, c *command, args []string) int
}

// lookupCommand resolves a command name. It is a function (not a package
// level table) so nothing is allocated on the hook path.
func lookupCommand(name string) (*command, bool) {
	var c *command
	switch name {
	case "lint":
		c = &command{name, lintUsage, runLint}
	case "test-rules":
		c = &command{name, testRulesUsage, runTestRules}
	case "config":
		c = &command{name, configUsage, runConfig}
	case "verify":
		c = &command{name, verifyUsage, runVerify}
	case "init":
		c = &command{name, initUsage, runInit}
	case "stats":
		c = &command{name, statsUsage, runStats}
	case "rules":
		c = &command{name, rulesUsage, runRules}
	case "replay":
		c = &command{name, replayUsage, runReplay}
	case "audit":
		c = &command{name, auditUsage, runAudit}
	case "install":
		c = &command{name, installUsage, runInstall}
	case "uninstall":
		c = &command{name, uninstallUsage, runUninstall}
	case "doctor":
		c = &command{name, doctorUsage, runDoctor}
	case "org":
		c = &command{name, orgUsage, runOrg}
	case "report":
		c = &command{name, reportUsage, runReport}
	case "gc":
		c = &command{name, gcUsage, runGC}
	case "version":
		c = &command{name, versionUsage, runVersion}
	default:
		return nil, false
	}
	return c, true
}

// newFlagSet returns a quiet FlagSet (errors are reported by the caller).
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseArgs parses flags interspersed with positional arguments (the stdlib
// flag package stops at the first positional). Everything after a "--"
// terminator is positional.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			tail = args[i+1:]
			args = args[:i]
			break
		}
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
	return append(pos, tail...), nil
}

// multiFlag collects repeated or comma-separated values.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*m = append(*m, p)
		}
	}
	return nil
}

// sortedKeys returns the keys of m in order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// errIs is errors.Is spelled shorter for the exit-code mapping tables.
func errIs(err, target error) bool { return errors.Is(err, target) }
