package bashparse

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"mvdan.cc/sh/v3/syntax"

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

var parserPool = sync.Pool{New: func() any {
	return syntax.NewParser(syntax.Variant(syntax.LangBash))
}}

func parseFile(src string) (*syntax.File, error) {
	p := parserPool.Get().(*syntax.Parser)
	defer parserPool.Put(p)
	return p.Parse(strings.NewReader(src), "")
}

// nodeHint estimates the node count from the source size so large commands
// do not repeatedly regrow the node slice.
func nodeHint(n int) int {
	h := n/24 + 4
	if h > 1<<14 {
		h = 1 << 14
	}
	return h
}

// errInternal marks a recovered panic (a bug, never a syntax error).
var errInternal = errors.New("bashparse: internal error")

// Parse parses src as bash. A non-nil error means the command could not be
// parsed (shell matchers then return unknown).
func Parse(src string, opt Options) (cl *model.CommandList, err error) {
	defer func() {
		if r := recover(); r != nil {
			cl, err = nil, fmt.Errorf("%w: %v", errInternal, r)
		}
	}()
	f, err := parseFile(src)
	if err != nil {
		return nil, err
	}
	b := &builder{
		cl:  &model.CommandList{Shell: model.ShellBash, Nodes: make([]model.CommandNode, 0, nodeHint(len(src)))},
		max: MaxShellDepth,
	}
	if opt.MaxDepth > 0 {
		b.max = opt.MaxDepth
	}
	(&walker{b: b, src: src}).stmts(f.Stmts, ctx{})
	return b.cl, nil
}

// Parser implements ports.BashParser.
type Parser struct{ Opt Options }

var _ ports.BashParser = Parser{}

// ParseBash implements ports.BashParser.
func (p Parser) ParseBash(src string) (*model.CommandList, error) { return Parse(src, p.Opt) }
