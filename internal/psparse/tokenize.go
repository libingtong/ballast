package psparse

import (
	"fmt"
	"strings"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
)

// Aliases maps lower-cased PowerShell aliases to canonical lower-cased
// cmdlet names. Frozen seed list; U2 may ADD entries (never remove).
var Aliases = map[string]string{
	"rm": "remove-item", "del": "remove-item", "erase": "remove-item", "ri": "remove-item",
	"rd": "remove-item", "rmdir": "remove-item",
	"sleep": "start-sleep",
	"sc":    "set-content", "ac": "add-content",
	"cp": "copy-item", "copy": "copy-item", "cpi": "copy-item",
	"mv": "move-item", "move": "move-item", "mi": "move-item",
	"ni": "new-item", "md": "new-item", "mkdir": "new-item",
	"cat": "get-content", "gc": "get-content", "type": "get-content",
	"echo": "write-output", "write": "write-output",
	"iex":  "invoke-expression",
	"saps": "start-process", "start": "start-process",
	"sajb":    "start-job",
	"foreach": "foreach-object", "%": "foreach-object",
	"cd": "set-location", "sl": "set-location", "chdir": "set-location",
	"tee": "tee-object", "clc": "clear-content", "sls": "select-string",
	"?": "where-object", "where": "where-object",
}

// Parse tokenizes src. A non-nil error means "cannot tokenize" ⇒ unknown.
func Parse(src string) (cl *model.CommandList, err error) {
	defer func() {
		if r := recover(); r != nil {
			cl, err = nil, fmt.Errorf("psparse: internal error: %v", r)
		}
	}()
	cl = &model.CommandList{Shell: model.ShellPowerShell, Nodes: make([]model.CommandNode, 0, 4)}
	p := &parser{src: src, cl: cl}
	if err := p.statements(ctx{}, 0); err != nil {
		return nil, err
	}
	return cl, nil
}

// CanonicalName resolves aliases and strips ".exe" (lower-cased result).
func CanonicalName(word string) string {
	w := strings.ToLower(word)
	bare := !strings.ContainsAny(w, `\/`)
	if i := strings.LastIndexAny(w, `\/`); i >= 0 {
		w = w[i+1:]
	}
	if strings.HasSuffix(w, ".exe") && len(w) > len(".exe") {
		return strings.TrimSuffix(w, ".exe")
	}
	if bare {
		if a, ok := Aliases[w]; ok {
			return a
		}
	}
	return w
}

// canonicalHead is CanonicalName for the command word of a pipeline element.
func canonicalHead(word string) string { return CanonicalName(word) }

// Parser implements ports.PowerShellParser.
type Parser struct{}

var _ ports.PowerShellParser = Parser{}

// ParsePowerShell implements ports.PowerShellParser.
func (Parser) ParsePowerShell(src string) (*model.CommandList, error) { return Parse(src) }
