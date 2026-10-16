package model

// CommandList is the parse result of one shell command string (bash via
// mvdan.cc/sh, or the conservative PowerShell tokenizer). See plan B1
// "Bash 解析契约" and docs/dsl-semantics.md §6.
type CommandList struct {
	Shell ShellKind     `json:"shell"`
	Nodes []CommandNode `json:"nodes"`
	// Pipes lists adjacent pipeline pairs by node index (From | To).
	Pipes []PipeEdge `json:"pipes,omitempty"`
	// Unknown is true when the command contains a construct whose effect
	// cannot be known statically (eval, source of a dynamic file, PowerShell
	// Invoke-Expression, ...). Shell matchers return TriUnknown when no node
	// matches positively and Unknown is set.
	Unknown       bool   `json:"unknown,omitempty"`
	UnknownReason string `json:"unknown_reason,omitempty"`
}

// PipeEdge connects Nodes[From] | Nodes[To].
type PipeEdge struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// Word is one shell word. Lit is the literal value after quote removal; when
// Dynamic is true the word contains parameter expansion, command
// substitution, arithmetic, globbing on unknown input, etc., and Lit holds
// the best-effort source text (never trusted for matching).
type Word struct {
	Lit     string `json:"lit"`
	Dynamic bool   `json:"dynamic,omitempty"`
}

// LoopKind enumerates loop constructs enclosing a node.
type LoopKind string

const (
	LoopForList LoopKind = "for_list" // for x in a b c (N = item count when literal)
	LoopForC    LoopKind = "for_c"    // for ((i=0; i<n; i++))
	LoopWhile   LoopKind = "while"
	LoopUntil   LoopKind = "until"
	LoopForEach LoopKind = "foreach" // PowerShell foreach / ForEach-Object
)

// LoopInfo describes the innermost loop enclosing a node.
type LoopInfo struct {
	Kind LoopKind `json:"kind"`
	// N is the literal item count of a for_list loop (-1 when dynamic or not applicable).
	N int `json:"n"`
	// Bounded: for_list with literal items → TriTrue; `while true`/`until false`
	// → TriFalse; everything else → TriUnknown.
	Bounded Tri `json:"bounded"`
	// Depth counts nested loops (1 = innermost loop is top-level).
	Depth int `json:"depth"`
}

// Redirect is one I/O redirection attached to a node.
type Redirect struct {
	Op     string `json:"op"`           // ">", ">>", "&>", "&>>", ">|", "<", "<<", "<<<", "2>", ...
	Fd     string `json:"fd,omitempty"` // explicit fd ("2") or ""
	Target Word   `json:"target"`       // heredoc bodies are never parsed; Target is the delimiter
}

// WriteTarget is a file path a command writes (CommandNode.Writes) or reads
// (CommandNode.Reads), with how it was derived. The type name is kept for
// compatibility; it is used for both directions.
type WriteTarget struct {
	Path    string `json:"path"` // raw (not normalized); "" when Dynamic
	Dynamic bool   `json:"dynamic,omitempty"`
	// Via for Writes: "redirect", "tee", "cp", "mv", "install", "rsync",
	// "sed -i", "perl -i", "ruby -i", "git checkout", "git restore", "rm",
	// "truncate", "dd", "touch", "ln", "out-file",
	// "set-content", "add-content", "remove-item", "copy-item", "move-item",
	// "new-item".
	// Via for Reads: "redirect" (input redirection `< FILE`), "cat", "tac",
	// "nl", "head", "tail", "less", "more", "wc", "file", "sed", "grep",
	// "egrep", "fgrep", "rg", "awk", "gawk", "mawk", "get-content",
	// "select-string". Extraction rules: internal/bashparse/doc.go and
	// internal/psparse/doc.go.
	Via string `json:"via"`
}

// GitInvocation is the parse of a `git` node's global options (plan B1).
type GitInvocation struct {
	Sub string `json:"sub"` // subcommand ("" when none or dynamic)
	// SubDynamic is true when the subcommand word is dynamic.
	SubDynamic bool `json:"sub_dynamic,omitempty"`
	// Cwd is the effective -C directory (multiple -C are joined as git does);
	// "" when absent. CwdDynamic marks a dynamic -C argument.
	Cwd        string   `json:"cwd,omitempty"`
	CwdDynamic bool     `json:"cwd_dynamic,omitempty"`
	GitDir     string   `json:"git_dir,omitempty"`
	WorkTree   string   `json:"work_tree,omitempty"`
	Config     []string `json:"config,omitempty"` // -c k=v values
	NoPager    bool     `json:"no_pager,omitempty"`
	// Args are the words after the subcommand (Dynamic preserved).
	Args []Word `json:"args,omitempty"`
}

// SleepInfo is the parsed duration of a sleep-like node
// (`sleep 300|5m|1h|1d`, sums of several args, PowerShell Start-Sleep -Seconds/-Milliseconds).
type SleepInfo struct {
	Seconds float64 `json:"seconds"`
	Known   bool    `json:"known"` // false when any duration word is dynamic/unparsable
}

// CommandNode is one simple command after wrapper stripping.
type CommandNode struct {
	// Argv after wrapper stripping. Argv[0] is the command word.
	Argv []Word `json:"argv"`
	// Name is the lower-cased basename of Argv[0] for PowerShell (canonical
	// cmdlet name after alias resolution, e.g. "start-sleep") and the exact
	// basename for bash; "" when Argv[0] is dynamic.
	Name string `json:"name"`
	// Wrappers stripped in order: env, nice, nohup, timeout, time, command,
	// exec, sudo, xargs is NOT a wrapper.
	Wrappers []string `json:"wrappers,omitempty"`
	Sudo     bool     `json:"sudo,omitempty"`

	Loop    *LoopInfo `json:"loop,omitempty"`
	Async   bool      `json:"async,omitempty"`    // shell `&` (or PowerShell Start-Job/&) — not tool.run_in_background
	InSubst bool      `json:"in_subst,omitempty"` // inside $(...), `...`, <(...)
	// Depth is the `sh -c` recursion depth (0 = top level, max 2).
	Depth int `json:"depth,omitempty"`

	Redirects []Redirect    `json:"redirects,omitempty"`
	Writes    []WriteTarget `json:"writes,omitempty"`
	// Reads are files the node reads with a recognized read-only command
	// (cat/head/tail/sed -n/grep/rg/awk/wc/nl/Get-Content/... and `< FILE`).
	// The hook adds the non-dynamic ones to the agent's read set on
	// PostToolUse of Bash/PowerShell (read_this_session), which is how
	// clients without a Read tool (Codex exec_command) satisfy
	// read-before-edit. Reads never feed `path`/`writes` matching.
	Reads []WriteTarget  `json:"reads,omitempty"`
	Git   *GitInvocation `json:"git,omitempty"`
	Sleep *SleepInfo     `json:"sleep,omitempty"`
	// Params is the PowerShell parameter map (lower-cased "-name" → values); nil for bash.
	Params map[string][]Word `json:"params,omitempty"`
}

// HasDynamicArgv reports whether any argv word is dynamic.
func (n *CommandNode) HasDynamicArgv() bool {
	for _, w := range n.Argv {
		if w.Dynamic {
			return true
		}
	}
	return false
}
