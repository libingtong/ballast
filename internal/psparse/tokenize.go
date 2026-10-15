package psparse

import (
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
}

// Parse tokenizes src. A non-nil error means "cannot tokenize" ⇒ unknown.
func Parse(src string) (*model.CommandList, error) {
	panic("unimplemented: psparse.Parse")
}

// CanonicalName resolves aliases and strips ".exe" (lower-cased result).
func CanonicalName(word string) string {
	panic("unimplemented: psparse.CanonicalName")
}

// Parser implements ports.PowerShellParser.
type Parser struct{}

var _ ports.PowerShellParser = Parser{}

// ParsePowerShell implements ports.PowerShellParser.
func (Parser) ParsePowerShell(src string) (*model.CommandList, error) { return Parse(src) }
