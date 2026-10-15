package model

import (
	"bytes"
	"encoding/json"
	"errors"
)

// RuleFile is the on-disk rule file format (schema/rules.v1.json). The same
// format is used by all three layers, rules/default.json, rules/optional/*,
// examples/* and schema/examples/*.
type RuleFile struct {
	Schema   string    `json:"$schema,omitempty"`
	Version  int       `json:"version"` // must be 1
	Settings *Settings `json:"settings,omitempty"`
	Rules    []Rule    `json:"rules"`
}

// Settings are file-level knobs merged per key with layer precedence
// org > repo > user (the first layer that sets a key wins).
type Settings struct {
	Inject *InjectSettings `json:"inject,omitempty"`
}

// InjectSettings tune continuous injection (plan B5).
type InjectSettings struct {
	// AnchorEvery: emit the anchor on every Nth UserPromptSubmit (default 1;
	// 0 disables prompt anchors).
	AnchorEvery *int `json:"anchor_every,omitempty"`
	// BatchAfterTools: mid-turn re-anchor once tool_count_since_inject ≥ N (default 25; 0 disables).
	BatchAfterTools *int `json:"batch_after_tools,omitempty"`
}

// Defaults for InjectSettings.
const (
	DefaultAnchorEvery     = 1
	DefaultBatchAfterTools = 25
)

// Rule is one declarative rule (plan B1, docs/dsl-semantics.md §2).
type Rule struct {
	ID          string     `json:"id"`
	Description string     `json:"description,omitempty"`
	Enabled     *bool      `json:"enabled,omitempty"` // default true
	Action      Action     `json:"action,omitempty"`  // default log
	Target      Action     `json:"target,omitempty"`  // default: strictest action the event allows on claude-code
	Event       EventName  `json:"event"`
	Clients     []ClientID `json:"clients,omitempty"` // default: all clients
	Match       *Match     `json:"match,omitempty"`
	When        []Cond     `json:"when,omitempty"`    // AND
	Message     string     `json:"message,omitempty"` // ≤300 runes
	Inject      *Inject    `json:"inject,omitempty"`
	Examples    []Example  `json:"examples"`
}

// IsEnabled applies the enabled default.
func (r *Rule) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// EffectiveAction applies the action default (log).
func (r *Rule) EffectiveAction() Action {
	if r.Action == ActionNone {
		return ActionLog
	}
	return r.Action
}

// AppliesTo reports whether the rule is active for client c.
func (r *Rule) AppliesTo(c ClientID) bool {
	if len(r.Clients) == 0 {
		return true
	}
	for _, x := range r.Clients {
		if x == c {
			return true
		}
	}
	return false
}

// Inject is a rule's injection text. JSON accepts either a string (the text)
// or an object {"text": ..., "anchor": ...}.
type Inject struct {
	Text   string `json:"text,omitempty"`   // full text for SessionStart, ≤400 runes
	Anchor string `json:"anchor,omitempty"` // one-line anchor ≤80 runes; default: first line of Text truncated
}

// UnmarshalJSON accepts a string or an object.
func (in *Inject) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &in.Text)
	}
	type plain Inject
	var p plain
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return err
	}
	*in = Inject(p)
	return nil
}

// StringList is a JSON string-or-array of strings. Used by every list field
// of the DSL (globs, regexes, flags, names). Lists are OR unless documented.
type StringList []string

// UnmarshalJSON accepts "x" or ["x","y"].
func (s *StringList) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var one string
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		*s = StringList{one}
		return nil
	}
	if len(b) > 0 && b[0] == '[' {
		var many []string
		if err := json.Unmarshal(b, &many); err != nil {
			return err
		}
		*s = StringList(many)
		return nil
	}
	return errors.New("expected string or array of strings")
}

// Match is the matcher block. Present fields combine with AND; list values
// inside a field combine with OR (plan B1). `bash` and `ps` are the one
// exception: they are dispatched by the tool's shell kind (§5.4 of
// dsl-semantics) so a rule can carry both variants.
type Match struct {
	Tool            StringList            `json:"tool,omitempty"`       // glob on canonical OR raw tool name
	Path            StringList            `json:"path,omitempty"`       // glob list with list-level "!" negation
	AgentType       StringList            `json:"agent_type,omitempty"` // glob on Event.AgentType ("main" for no agent)
	InputRegex      map[string]StringList `json:"input_regex,omitempty"`
	RunInBackground *bool                 `json:"run_in_background,omitempty"`
	Bash            *ShellMatch           `json:"bash,omitempty"`
	PS              *ShellMatch           `json:"ps,omitempty"`
	Text            *TextMatch            `json:"text,omitempty"`
}

// ShellMatch matches CommandNodes. All present fields must hold on the SAME
// node ("there exists a node satisfying all of them", plan B1 scope rule).
type ShellMatch struct {
	Cmd         StringList `json:"cmd,omitempty"`        // glob on CommandNode.Name
	ArgsRegex   StringList `json:"args_regex,omitempty"` // regex on literal args (argv[1:]) joined by " "
	Flags       StringList `json:"flags,omitempty"`      // any listed flag present (combined short flags expand)
	GitSub      StringList `json:"git_sub,omitempty"`    // CommandNode.Git.Sub in list
	SleepS      *Cmp       `json:"sleep_s,omitempty"`
	Async       *bool      `json:"async,omitempty"`
	InLoop      *bool      `json:"in_loop,omitempty"`
	LoopBounded *bool      `json:"loop_bounded,omitempty"`
	InSubst     *bool      `json:"in_subst,omitempty"`
	Sudo        *bool      `json:"sudo,omitempty"`
	Dynamic     *bool      `json:"dynamic,omitempty"`   // argv contains a dynamic word
	Writes      StringList `json:"writes,omitempty"`    // glob list on write targets (same semantics as match.path)
	PipeFrom    StringList `json:"pipe_from,omitempty"` // node is right of a pipe whose left node Name matches
	PipeTo      StringList `json:"pipe_to,omitempty"`   // node is left of a pipe whose right node Name matches
}

// TextSource selects the text a TextMatch reads.
type TextSource string

const (
	TextLastAssistantMessage TextSource = "last_assistant_message" // Stop, SubagentStop
	TextPrompt               TextSource = "prompt"                 // UserPromptSubmit
	TextToolResponse         TextSource = "tool_response"          // PostToolUse
	TextError                TextSource = "error"                  // PostToolUseFailure
)

// Preprocess steps for TextMatch (applied in list order, before Window).
const (
	PreStripCodeBlocks = "strip_code_blocks" // remove ``` fenced blocks and `inline` spans
	PreStripQuotes     = "strip_quotes"      // remove "…", “…”, 「…」 and > quoted lines
)

// TextMatch matches free text (plan B1 `text`).
type TextMatch struct {
	Source     TextSource  `json:"source,omitempty"` // default derived from the rule event
	Preprocess []string    `json:"preprocess,omitempty"`
	Window     *Window     `json:"window,omitempty"`
	Regex      StringList  `json:"regex,omitempty"`     // any matches
	NotRegex   StringList  `json:"not_regex,omitempty"` // ordered exemptions: any match → false
	CJKRatio   *Cmp        `json:"cjk_ratio,omitempty"` // ratio of CJK runes among letters
	CountRegex *CountRegex `json:"count_regex,omitempty"`
}

// Window limits the text to its head and/or tail (runes).
type Window struct {
	TailChars int `json:"tail_chars,omitempty"`
	HeadChars int `json:"head_chars,omitempty"`
}

// CountRegex compares the number of non-overlapping matches of Regex.
type CountRegex struct {
	Regex string `json:"regex"`
	Cmp
}

// Cond is one `when` condition: exactly one key must be set (validation
// enforces it). Combinators: all / any / not. Everything else is a
// predicate from the closed vocabulary (plan B1, dsl-semantics §7).
type Cond struct {
	All []Cond `json:"all,omitempty"`
	Any []Cond `json:"any,omitempty"`
	Not *Cond  `json:"not,omitempty"`

	ReadThisSession       *PathArg        `json:"read_this_session,omitempty"`
	PathExists            *PathArg        `json:"path_exists,omitempty"`
	VerifyFresh           *VerifyFreshArg `json:"verify_fresh,omitempty"`
	VerifyFailedSinceRead *PathArg        `json:"verify_failed_since_read,omitempty"`
	ToolCountSinceInject  *Cmp            `json:"tool_count_since_inject,omitempty"`
	PrevPromptRegex       *RegexArg       `json:"prev_prompt_regex,omitempty"`
	PriorHit              *PriorHitArg    `json:"prior_hit,omitempty"`
	EnvEquals             *EnvEqualsArg   `json:"env_equals,omitempty"`
	BranchGlob            *GlobArg        `json:"branch_glob,omitempty"`
	IsSubagent            *Empty          `json:"is_subagent,omitempty"`
}

// Predicate names (closed vocabulary).
const (
	PredReadThisSession       = "read_this_session"
	PredPathExists            = "path_exists"
	PredVerifyFresh           = "verify_fresh"
	PredVerifyFailedSinceRead = "verify_failed_since_read"
	PredToolCountSinceInject  = "tool_count_since_inject"
	PredPrevPromptRegex       = "prev_prompt_regex"
	PredPriorHit              = "prior_hit"
	PredEnvEquals             = "env_equals"
	PredBranchGlob            = "branch_glob"
	PredIsSubagent            = "is_subagent"
)

// Empty is a parameterless predicate argument: `{"is_subagent": {}}`.
type Empty struct{}

// PathArg selects the path a path predicate inspects. Path "" or "$target"
// binds to the current target (each tool target in turn, OR-combined;
// dsl-semantics §7.1). Otherwise a literal path (relative to the project root).
type PathArg struct {
	Path string `json:"path,omitempty"`
}

// TargetBinding is the PathArg value that binds to the current target.
const TargetBinding = "$target"

// VerifyFreshArg names the verify entries to check; empty = all `required`
// entries of .ballast/project.json.
type VerifyFreshArg struct {
	Names []string `json:"names,omitempty"`
}

// RegexArg is a single-regex argument.
type RegexArg struct {
	Regex string `json:"regex"`
}

// PriorHitArg compares the agent-scoped hit count of another (or the same) rule.
type PriorHitArg struct {
	Rule string `json:"rule"`
	Cmp
}

// EnvEqualsArg compares an environment variable of the hook process.
type EnvEqualsArg struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// GlobArg is a glob list argument (OR, list-level "!" negation).
type GlobArg struct {
	Patterns StringList `json:"patterns"`
}

// ExpectKind is an example's expected outcome.
type ExpectKind string

const (
	ExpectHit     ExpectKind = "hit"     // rule outcome TriTrue
	ExpectMiss    ExpectKind = "miss"    // rule outcome TriFalse
	ExpectUnknown ExpectKind = "unknown" // rule outcome TriUnknown (fail-open)
)

// Example is a self-test case run by `ballast test-rules` and by lint.
// Every rule needs ≥1 "hit" and ≥1 non-hit ("miss" or "unknown").
type Example struct {
	Name   string     `json:"name,omitempty"`
	Expect ExpectKind `json:"expect"`
	Client ClientID   `json:"client,omitempty"` // default claude-code
	// Native event name for Input; default: the rule event's native name on Client.
	Native string `json:"native,omitempty"`
	// Input is a hook stdin payload (subset) in the CLIENT's native format; it
	// goes through the real adapter ParseEvent/NormalizeTool. Common fields
	// (session_id, cwd, hook_event_name) are filled in when absent.
	Input   json.RawMessage `json:"input"`
	Fixture *Fixture        `json:"fixture,omitempty"`
}

// Fixture fakes the predicate environment of an Example.
// Relative paths are resolved against Cwd.
type Fixture struct {
	Cwd                  string            `json:"cwd,omitempty"`          // default "/repo" (also the project root)
	Home                 string            `json:"home,omitempty"`         // default "/home/user"
	GOOS                 string            `json:"goos,omitempty"`         // default "linux" (case-sensitive paths)
	Read                 []string          `json:"read,omitempty"`         // read_this_session = true for these
	Exists               []string          `json:"exists,omitempty"`       // path_exists = true for these, false otherwise
	VerifyFresh          string            `json:"verify_fresh,omitempty"` // "true"|"false"|"unknown" (default "unknown")
	FailedSinceRead      []string          `json:"failed_since_read,omitempty"`
	Env                  map[string]string `json:"env,omitempty"`
	Branch               *string           `json:"branch,omitempty"` // nil → branch unknown
	ToolCountSinceInject *int              `json:"tool_count_since_inject,omitempty"`
	PrevPrompt           string            `json:"prev_prompt,omitempty"`
	PriorHits            map[string]int    `json:"prior_hits,omitempty"`
	NotARepo             bool              `json:"not_a_repo,omitempty"`
}

// Layer is a configuration layer.
type Layer string

const (
	LayerOrg  Layer = "org"
	LayerRepo Layer = "repo"
	LayerUser Layer = "user"
	// LayerFile is used for rule files evaluated outside the 3-layer stack
	// (`lint <file>`, `test-rules <file>`, `replay --rules <file>`).
	LayerFile Layer = "file"
)

// MaxEnabledRules is the global cap on enabled rules (plan B4).
const MaxEnabledRules = 12

// Text budgets (runes) — plan B1/B5; enforced by dsl lint and inject.
const (
	MaxMessageRunes      = 300
	MaxInjectTextRunes   = 400
	MaxAnchorRunes       = 80
	BudgetSessionStart   = 8000
	BudgetAnchor         = 1200
	BudgetGenSummary     = 1000
	BudgetReason         = 2000 // concatenated messages of one decision
	MaxMatchedRunes      = 200  // audit "matched"
	MaxEventSummaryRunes = 120
)

// LoadedRule is a parsed rule together with its provenance.
type LoadedRule struct {
	Rule   Rule   `json:"rule"`
	Layer  Layer  `json:"layer"`
	Source string `json:"source"` // file path
	Index  int    `json:"index"`  // position within the file's rules array
}

// RuleProgram is the dsl-owned compiled form of one rule (regexes, glob
// matchers, predicate tree). Opaque to every unit except U3.
type RuleProgram interface {
	RuleID() string
}

// CompiledRule is an enabled, merged, compiled rule.
type CompiledRule struct {
	LoadedRule
	Action  Action      `json:"action"` // defaults applied
	Target  Action      `json:"target"` // defaults applied
	Program RuleProgram `json:"-"`
}

// CompiledRuleSet is the immutable evaluation-ready rule set for one hook
// invocation. Produced by config (U4) using dsl (U3); consumed by the hook,
// inject, test-rules and replay.
type CompiledRuleSet struct {
	// Generation = rules_hash: hex(sha256(canonical JSON of the merged
	// effective rules + settings))[:16]. Stable across processes.
	Generation string         `json:"generation"`
	Rules      []CompiledRule `json:"rules"` // load order, enabled only, ≤ MaxEnabledRules
	Settings   Settings       `json:"settings"`
	Sources    []LayerSource  `json:"sources"`
	Notices    []ConfigNotice `json:"notices,omitempty"`
}

// Rule returns the compiled rule with the given id, or nil.
func (rs *CompiledRuleSet) Rule(id string) *CompiledRule {
	for i := range rs.Rules {
		if rs.Rules[i].Rule.ID == id {
			return &rs.Rules[i]
		}
	}
	return nil
}

// LayerStatus describes how a layer was obtained.
type LayerStatus string

const (
	LayerOK      LayerStatus = "ok"
	LayerMissing LayerStatus = "missing"     // file absent (normal)
	LayerLKG     LayerStatus = "lkg"         // file invalid; last-known-good used
	LayerInvalid LayerStatus = "invalid"     // file invalid and no LKG → layer empty (loud)
	LayerLocked  LayerStatus = "locked_path" // T1 lock_paths disabled an override
)

// StatKey identifies a file version cheaply (plan B4 hot reload).
type StatKey struct {
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Size    int64  `json:"size"`
	MtimeNS int64  `json:"mtime_ns"`
	Inode   uint64 `json:"inode"` // 0 on Windows (use file index if available)
}

// LayerSource records one layer's input file.
type LayerSource struct {
	Layer  Layer       `json:"layer"`
	Path   string      `json:"path"`
	Stat   StatKey     `json:"stat"`
	Hash   string      `json:"hash,omitempty"` // sha256 hex of the bytes actually used (LKG bytes when status=lkg)
	Status LayerStatus `json:"status"`
	Error  string      `json:"error,omitempty"`
}

// NoticeKind enumerates configuration notices (also audit record types).
type NoticeKind string

const (
	NoticeRuleDropped   NoticeKind = "rule_dropped"   // over the 12-rule cap
	NoticeRuleShadowed  NoticeKind = "rule_shadowed"  // lower layer redefined an org id, or user id overridden by repo
	NoticeConfigInvalid NoticeKind = "config_invalid" // layer invalid (LKG or empty)
)

// ConfigNotice is a load-time notice surfaced in stderr, audit and the next injection.
type ConfigNotice struct {
	Kind    NoticeKind `json:"kind"`
	Layer   Layer      `json:"layer"`
	RuleID  string     `json:"rule_id,omitempty"`
	Path    string     `json:"path,omitempty"`
	Message string     `json:"message"`
}

// Severity of a lint diagnostic.
type Severity string

const (
	SevError   Severity = "error"
	SevWarning Severity = "warning"
)

// Diagnostic is one lint / validation finding.
type Diagnostic struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"` // stable kebab-case code, see dsl-semantics §11
	File     string   `json:"file,omitempty"`
	Pointer  string   `json:"pointer,omitempty"` // JSON pointer, e.g. /rules/2/match/input_regex/command
	RuleID   string   `json:"rule_id,omitempty"`
	Client   ClientID `json:"client,omitempty"`
	Message  string   `json:"message"`
}
