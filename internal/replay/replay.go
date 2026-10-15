package replay

import (
	"errors"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// Class of a replayed judgement.
type Class string

const (
	KnownHit    Class = "known-hit"
	KnownMiss   Class = "known-miss"
	Unknown     Class = "unknown"
	Approximate Class = "approximate"
)

// ItemKind classifies transcript items.
type ItemKind string

const (
	ItemUserPrompt    ItemKind = "user_prompt"
	ItemToolUse       ItemKind = "tool_use"
	ItemToolResult    ItemKind = "tool_result"
	ItemAssistantText ItemKind = "assistant_text"
	ItemTurnEnd       ItemKind = "turn_end"
	ItemCompact       ItemKind = "compact"
)

// Item is one normalized transcript item.
type Item struct {
	UUID      string
	TS        time.Time
	Kind      ItemKind
	SessionID string
	AgentID   string
	Cwd       string
	ToolName  string // native tool name
	ToolInput []byte // raw JSON
	ToolUseID string
	IsError   bool   // tool_result error flag
	Text      string // prompt / assistant text / tool result text
	GitBranch string // branch recorded by the client at this item ("" if none)
}

// TranscriptReader streams items.
type TranscriptReader interface {
	// Next returns io.EOF at the end.
	Next() (*Item, error)
	// Skipped reports unparsable lines so far.
	Skipped() int
}

// Options for Run.
type Options struct {
	Transcript   string
	Client       model.ClientID // default claude-code
	Subagents    bool
	EvidenceMode string // "auto" | "file"
	EvidenceFile string
	// InitialState is a JSON file seeding the in-memory session state
	// (format frozen in the package doc: a model.Fixture restricted to its
	// state keys). "" ⇒ empty state.
	InitialState string
	RulesPath    string // "" ⇒ current 3-layer config
	Format       string // "json" | "md"
}

// Deps are injected collaborators.
type Deps struct {
	FS       sys.FS
	Clock    sys.Clock
	Adapters ports.AdapterRegistry
	Compiler ports.RuleCompiler
	Engine   ports.RuleEvaluator
	Rules    ports.RulesLoader
	Bash     ports.BashParser
	PS       ports.PowerShellParser
	Paths    ports.PathService
	// EvidenceDir is layout.EvidenceDir for auto mode.
	EvidenceDir string
	LabelsDir   string
	// Home is the home directory used to render transcript paths "~/"-relative
	// ("" ⇒ the process home, via sys.OSEnv).
	Home string
	// Project (optional; contract v1.3) is U8's strict project.json loader,
	// injected by the CLI so replay classifies project.json exactly like the
	// hook's evidence provider: (nil, nil) ⇒ no_project, error ⇒
	// project_invalid. nil ⇒ a lenient json.Unmarshal fallback.
	Project ports.ProjectLoader
}

// ErrUsage marks invalid options (CLI exit 2): unknown format or evidence
// mode, unsupported client, malformed --initial-state or rules file.
var ErrUsage = errors.New("replay: usage error")

// Sample is one judgement in the report.
type Sample struct {
	ID           string          `json:"id"`
	RuleID       string          `json:"rule_id"`
	Class        Class           `json:"class"`
	Event        model.EventName `json:"event"`
	TS           time.Time       `json:"ts"`
	UUID         string          `json:"uuid"`
	Matched      string          `json:"matched,omitempty"`
	UnknownCause string          `json:"unknown_cause,omitempty"`
	Label        string          `json:"label,omitempty"` // tp | fp
}

// RuleSummary aggregates per rule.
type RuleSummary struct {
	RuleID    string        `json:"rule_id"`
	Counts    map[Class]int `json:"counts"`
	Labeled   int           `json:"labeled"`
	Precision *float64      `json:"precision,omitempty"`
}

// Report is the replay output (JSON shape frozen; md is a rendering of it).
type Report struct {
	V            int            `json:"v"`
	Transcript   string         `json:"transcript"` // "~/"-relative
	Client       model.ClientID `json:"client"`
	RulesHash    string         `json:"rules_hash"`
	Items        int            `json:"items"`
	SkippedLines int            `json:"skipped_lines"`
	Rules        []RuleSummary  `json:"rules"`
	Samples      []Sample       `json:"samples"`
}
