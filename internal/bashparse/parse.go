package bashparse

import (
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
)

// MaxShellDepth is the maximum `sh -c` recursion depth (plan B1).
const MaxShellDepth = 2

// Options tune parsing (zero value = plan defaults).
type Options struct {
	// MaxDepth overrides MaxShellDepth when > 0 (tests only).
	MaxDepth int
}

// Parse parses src as bash. A non-nil error means the command could not be
// parsed (shell matchers then return unknown).
func Parse(src string, opt Options) (*model.CommandList, error) {
	panic("unimplemented: bashparse.Parse")
}

// StripWrappers removes leading wrapper commands from argv and returns the
// remaining argv, the stripped wrapper names, and whether sudo was stripped.
func StripWrappers(argv []model.Word) (rest []model.Word, wrappers []string, sudo bool) {
	panic("unimplemented: bashparse.StripWrappers")
}

// ParseGit parses git global options of a node whose command is git.
// argv[0] must be the git word. Returns nil when argv[0] is not git.
func ParseGit(argv []model.Word) *model.GitInvocation {
	panic("unimplemented: bashparse.ParseGit")
}

// ParseSleep parses the arguments of `sleep` (argv[1:]).
func ParseSleep(args []model.Word) model.SleepInfo {
	panic("unimplemented: bashparse.ParseSleep")
}

// WriteTargets derives write targets from a node's argv (tee/cp/mv/sed -i/...);
// redirect targets are added by Parse.
func WriteTargets(name string, argv []model.Word) []model.WriteTarget {
	panic("unimplemented: bashparse.WriteTargets")
}

// Parser implements ports.BashParser.
type Parser struct{ Opt Options }

var _ ports.BashParser = Parser{}

// ParseBash implements ports.BashParser.
func (p Parser) ParseBash(src string) (*model.CommandList, error) { return Parse(src, p.Opt) }
