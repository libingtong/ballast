package ports

import (
	"encoding/json"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

// Adapter is the per-client runtime protocol (U6). The v0.4 plan's Adapter
// also lists Detect / InstallTargets / Transcript / Doctor; those are split
// out by ownership into install.Client (U10) and replay.TranscriptReader (U9).
type Adapter interface {
	ID() model.ClientID
	// Capabilities MUST return model.Capabilities(ID()).
	Capabilities() model.CapabilitySet
	// NativeEvents MUST return model.NativeEventTable[ID()].
	NativeEvents() []model.NativeEvent
	// ParseEvent decodes hook stdin for the given native event. Optional
	// fields are tolerant (missing/mistyped ⇒ zero). Returns
	// model.ErrUnknownEvent for natives not in the table and model.ErrBadInput
	// when stdin is not a JSON object. Tool events are normalized with
	// NormalizeTool. ReceivedAt is left zero (the hook stamps it).
	ParseEvent(native string, stdin []byte) (*model.Event, error)
	// NormalizeTool maps a native tool name + input onto a canonical ToolCall
	// (apply_patch → Edit with multiple targets; exec_command/Shell/terminal →
	// Bash; Task → Agent; PowerShell stays PowerShell). Never fails on unknown
	// tools (passthrough with Name = raw); errors only on malformed input JSON
	// that it needed to parse, in which case the ToolCall is still usable.
	NormalizeTool(rawName string, input json.RawMessage) (model.ToolCall, error)
	// Render turns the decision into stdout bytes. d.Empty() ⇒ nil (empty
	// stdout). Never renders "allow". The caller guarantees d.Action is
	// supported for (client, event) — Render returns an error otherwise.
	Render(ev *model.Event, d *model.Decision) ([]byte, error)
}

// AdapterRegistry resolves adapters by client id.
type AdapterRegistry interface {
	Get(id model.ClientID) (Adapter, bool)
	All() []Adapter
}

// RulesLoader (U4 config) returns the rule set in effect. It never fails
// because of a bad rule file (LKG / empty layer + notices instead); it
// fails only on unexpected I/O errors (the hook then fails open).
type RulesLoader interface {
	Load(req model.LoadRequest) (*model.CompiledRuleSet, error)
}

// RuleCompiler (U3 dsl) parses, validates and compiles rules; used by the
// loader (U4), lint, test-rules and replay.
type RuleCompiler interface {
	// ParseFile decodes and VALIDATES one rule file (schema-level checks,
	// regex/glob compile, field applicability, predicate arity). Errors in
	// individual rules are diagnostics; err != nil only for undecodable input.
	ParseFile(layer model.Layer, path string, data []byte) (*model.RuleFile, []model.Diagnostic, error)
	// Compile builds programs for already merged rules. Generation is
	// computed by the caller (config) and passed through.
	Compile(rules []model.LoadedRule, settings model.Settings, generation string) (*model.CompiledRuleSet, error)
}

// RuleEvaluator (U3 dsl) evaluates every rule of rs whose Event equals
// in.Event.Name and which applies to in.Client, then arbitrates (plan B1).
// It must not panic on any input; internal errors yield unknown outcomes.
type RuleEvaluator interface {
	Evaluate(rs *model.CompiledRuleSet, in *model.EvalInput) (*model.Verdict, error)
}

// BashParser (U2 bashparse). err != nil ⇒ parse failure ⇒ matcher unknown.
type BashParser interface {
	ParseBash(src string) (*model.CommandList, error)
}

// PowerShellParser (U2 psparse). err != nil ⇒ cannot tokenize ⇒ unknown.
type PowerShellParser interface {
	ParsePowerShell(src string) (*model.CommandList, error)
}

// GlobMatcher is a compiled glob list (list-level "!" negation, doublestar
// dialect, case folding per PathContext.GOOS).
type GlobMatcher interface {
	// Match reports whether SOME candidate matches ≥1 positive pattern AND NO
	// candidate matches any negative ("!") pattern (dsl-semantics §5.2).
	Match(candidates ...string) bool
	Patterns() []string
}

// PathService (U1 paths) normalizes paths and compiles globs.
type PathService interface {
	// Normalize returns the internal form of p (absolute; forward slashes;
	// lower-case drive letter; UNC → //server/share; Git-Bash /c/… → c:/…;
	// "~" expanded; cleaned). Relative p is resolved against base.
	Normalize(p, base string, ctx model.PathContext) string
	// Candidates returns the strings a path glob is matched against: the
	// normalized absolute path, its ProjectRoot-relative form (when inside),
	// and the same two forms of the EvalSymlinks-resolved path (parent
	// resolution for non-existent files), deduplicated.
	Candidates(raw string, ctx model.PathContext) []string
	// CompileGlobs validates and compiles patterns for a path context:
	// "~/" prefixes expand with ctx.Home; case folding follows ctx.GOOS
	// (windows, darwin). Callers cache the result per (patterns, Home, GOOS).
	CompileGlobs(patterns []string, ctx model.PathContext) (GlobMatcher, error)
}

// StateStore (U5 state). Scope directories are created on demand.
type StateStore interface {
	// Load reads WITHOUT locking. Missing ⇒ empty state, info.Exists=false,
	// info.Readable=true, nil error. Corrupt ⇒ empty state,
	// info.Corrupt=true, Readable=true, nil error. Any other I/O error ⇒
	// empty state, Readable=false and the error (contract v1.3: Readable is
	// false exactly when err != nil). Never returns a nil state.
	Load(key model.ScopeKey) (model.SessionState, model.StateLoadInfo, error)
	// Commit locks (≤20ms), re-reads, merges d, atomically replaces the
	// file. A lock timeout returns info.LockTimeout=true and a nil error.
	Commit(key model.ScopeKey, d *model.StateDelta) (model.StateCommitInfo, error)
	// DropAgent removes one agent state file (SubagentStop); DropSession
	// removes every agent state file of the session (SessionEnd). Missing ⇒
	// nil. Both remove a file only while holding its lock. DropAgent leaves
	// "<file>.lock" alone; DropSession (v1.4) then unlinks the orphaned lock
	// files, each only while holding it (lockers re-verify the inode after
	// locking, so a lock is never split in two), and removes the emptied
	// session directory. Best effort: whatever survives is left to GC, and a
	// lock timeout leaves the file for GC and returns nil (contract v1.3).
	DropAgent(key model.ScopeKey) error
	DropSession(key model.ScopeKey) error
}

// AuditSink (U5 audit). Append writes all records with ONE write call
// (each line ≤ 8KB after truncation), checks short writes, and returns an
// error on any failure (the hook treats it as an exception, plan B7).
type AuditSink interface {
	Append(recs []model.AuditRecord) error
}

// AuditSource (U5 audit Reader) streams audit records for stats/replay
// (U9) in chronological order; corrupt lines are skipped and counted.
// Zero since/until ⇒ unbounded.
type AuditSource interface {
	Records(since, until time.Time, fn func(rec *model.AuditRecord) error) (corrupt int, err error)
}

// RepoLocator (U8 snapshot) finds the git work tree containing cwd without
// running git. Returns model.ErrNotRepo outside a repo.
type RepoLocator interface {
	Locate(cwd string) (*model.RepoInfo, error)
	// Branch reads HEAD: ("main", TriTrue); detached ⇒ ("", TriFalse);
	// unreadable ⇒ ("", TriUnknown). Backs WorldView.Branch in the hook.
	Branch(repo *model.RepoInfo) (string, model.Tri)
}

// EvidenceProvider (U8 evidence) opens an EvidenceView bound to the event's
// repo (nil outside git ⇒ every evidence predicate unknown, cause
// not_a_repo). Open does NO I/O: project.json is loaded lazily, through the
// provider's own ProjectLoader, the first time a view method needs it, and
// for `git -C <other repo> commit` the TARGET repo's project.json (required
// names, exempt) is loaded instead (internal/evidence/doc.go).
type EvidenceProvider interface {
	Open(repo *model.RepoInfo, deadline time.Time) (model.EvidenceView, error)
}

// ProjectLoader (U8 project) reads <root>/.ballast/project.json.
// Missing ⇒ (nil, nil). Invalid ⇒ (nil, error). Used by the evidence
// provider (lazily) and by the CLI; the hook never calls it directly.
type ProjectLoader interface {
	LoadProject(root string) (*model.ProjectConfig, error)
}

// Injector (U4 inject) builds injection text within budgets (plan B5).
type Injector interface {
	Build(req *model.InjectRequest) (*model.Injection, error)
}

// Spawner (U8 runner) starts a detached child process (GC, verify runner)
// that survives the hook's exit and never blocks it. env is the child's
// complete environment (nil ⇒ inherit the caller's); logPath "" ⇒ the
// child's stdout/stderr go to the null device, otherwise they are appended
// to logPath (parent directories created). stdin is always the null device
// (contract v1.3).
type Spawner interface {
	SpawnDetached(argv []string, env []string, logPath string) (pid int, err error)
}
