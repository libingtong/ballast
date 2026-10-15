package model

import "sync"

// NewLazyCommands returns a ShellCommands that parses src with parse on the
// first Get and memoizes the result. parse must not be nil. Used by the hook
// (U7) and dsl test-rules (U3) so that rules without shell matchers never
// pay for parsing.
func NewLazyCommands(src string, parse func(string) (*CommandList, error)) ShellCommands {
	return &lazyCommands{src: src, parse: parse}
}

type lazyCommands struct {
	once  sync.Once
	src   string
	parse func(string) (*CommandList, error)
	cl    *CommandList
	err   error
}

func (l *lazyCommands) Get() (*CommandList, error) {
	l.once.Do(func() { l.cl, l.err = l.parse(l.src) })
	return l.cl, l.err
}
