package layout

import (
	"errors"
	"path/filepath"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// Env var names.
const (
	EnvHome        = "BALLAST_HOME"
	EnvInstallRoot = "BALLAST_INSTALL_ROOT"
	EnvManagedDir  = "BALLAST_MANAGED_DIR"
	EnvClaudeDir   = "CLAUDE_CONFIG_DIR"
	EnvCodexHome   = "CODEX_HOME"
	EnvHermesHome  = "HERMES_HOME"
)

// Layout holds absolute OS paths.
type Layout struct {
	Home          string // ~/.ballast
	UserRules     string // ~/.ballast/rules.json
	StateDir      string // ~/.ballast/state/<repo_inst>/<session>/<agent>.json(+.lock)
	EvidenceDir   string // ~/.ballast/evidence/<repo_inst>/<name>/<t_start_ns>-<job_id>.json
	JobsDir       string // ~/.ballast/jobs/<repo_inst>/<name>/<job_id>.{json,log}
	RequestsDir   string // ~/.ballast/requests/<repo_inst>/<request_id>.json (P2: hook-started verify)
	QuarantineDir string // ~/.ballast/quarantine/<job_id>/ (P2: protected paths a sandboxed test created, sandbox.Quarantine)
	AuditDir      string // ~/.ballast/audit/YYYY-MM[.N].jsonl
	CacheDir      string // ~/.ballast/cache/rules-<generation>-<key[:8]>.json, cache/stat.json
	LKGDir        string // ~/.ballast/cache/lkg/<layer>-<sha256(path)[:12]>.json
	BackupsDir    string // ~/.ballast/backups/<UTC yyyymmddThhmmssZ>-<rand6>/
	ReviewsDir    string // ~/.ballast/reviews/<rule_id>.json
	LabelsDir     string // ~/.ballast/labels/<rule_id>.jsonl (replay annotations)
	Manifest      string // ~/.ballast/install-manifest.json
	GCStamp       string // ~/.ballast/gc.stamp (daily GC marker)
	ReplaySet     string // ~/.ballast/replay-set.txt (local, never committed)

	InstallRoot string // parent of client config roots (normally the home dir)
	ManagedDir  string // OS managed-settings dir (Claude Code), see ManagedDirFor
	OrgRules    string // <ManagedDir>/ballast/rules.json
	OrgConfig   string // <ManagedDir>/ballast/org.json (P2: declared tier, runner sandbox policy)

	ClaudeDir string // CLAUDE_CONFIG_DIR or <InstallRoot>/.claude
	CodexDir  string // CODEX_HOME or <InstallRoot>/.codex
	CursorDir string // <InstallRoot>/.cursor
	HermesDir string // HERMES_HOME or <InstallRoot>/.hermes

	GOOS string
}

// Resolve computes the Layout from env. It performs no I/O.
func Resolve(env sys.Env) (Layout, error) {
	goos := env.GOOS()
	home, herr := env.HomeDir()
	get := func(k string) string { v, _ := env.Getenv(k); return v }

	var l Layout
	l.GOOS = goos
	l.Home = get(EnvHome)
	if l.Home == "" {
		if herr != nil || home == "" {
			return Layout{}, errors.New("layout: cannot determine home directory and BALLAST_HOME unset")
		}
		l.Home = filepath.Join(home, ".ballast")
	}
	l.UserRules = filepath.Join(l.Home, "rules.json")
	l.StateDir = filepath.Join(l.Home, "state")
	l.EvidenceDir = filepath.Join(l.Home, "evidence")
	l.JobsDir = filepath.Join(l.Home, "jobs")
	l.RequestsDir = filepath.Join(l.Home, "requests")
	l.QuarantineDir = filepath.Join(l.Home, "quarantine")
	l.AuditDir = filepath.Join(l.Home, "audit")
	l.CacheDir = filepath.Join(l.Home, "cache")
	l.LKGDir = filepath.Join(l.Home, "cache", "lkg")
	l.BackupsDir = filepath.Join(l.Home, "backups")
	l.ReviewsDir = filepath.Join(l.Home, "reviews")
	l.LabelsDir = filepath.Join(l.Home, "labels")
	l.Manifest = filepath.Join(l.Home, "install-manifest.json")
	l.GCStamp = filepath.Join(l.Home, "gc.stamp")
	l.ReplaySet = filepath.Join(l.Home, "replay-set.txt")

	l.InstallRoot = get(EnvInstallRoot)
	if l.InstallRoot == "" {
		l.InstallRoot = home
	}
	l.ManagedDir = get(EnvManagedDir)
	if l.ManagedDir == "" {
		l.ManagedDir = ManagedDirFor(goos)
	}
	l.OrgRules = filepath.Join(l.ManagedDir, "ballast", "rules.json")
	l.OrgConfig = filepath.Join(l.ManagedDir, "ballast", "org.json")

	l.ClaudeDir = get(EnvClaudeDir)
	if l.ClaudeDir == "" && l.InstallRoot != "" {
		l.ClaudeDir = filepath.Join(l.InstallRoot, ".claude")
	}
	l.CodexDir = get(EnvCodexHome)
	if l.CodexDir == "" && l.InstallRoot != "" {
		l.CodexDir = filepath.Join(l.InstallRoot, ".codex")
	}
	if l.InstallRoot != "" {
		l.CursorDir = filepath.Join(l.InstallRoot, ".cursor")
	}
	l.HermesDir = get(EnvHermesHome)
	if l.HermesDir == "" && l.InstallRoot != "" {
		l.HermesDir = filepath.Join(l.InstallRoot, ".hermes")
	}
	return l, nil
}

// ManagedDirFor returns Claude Code's managed-settings directory (plan A13).
func ManagedDirFor(goos string) string {
	switch goos {
	case "darwin":
		return "/Library/Application Support/ClaudeCode"
	case "windows":
		return `C:\Program Files\ClaudeCode`
	default: // linux, WSL
		return "/etc/claude-code"
	}
}

// RepoDir returns <root>/.ballast for a project root (git toplevel, or cwd outside git).
func RepoDir(root string) string { return filepath.Join(root, ".ballast") }

// RepoRules returns <root>/.ballast/rules.json.
func RepoRules(root string) string { return filepath.Join(root, ".ballast", "rules.json") }

// ProjectJSON returns <root>/.ballast/project.json.
func ProjectJSON(root string) string { return filepath.Join(root, ".ballast", "project.json") }

// StatePath returns the state file for a key whose components are ALREADY
// sanitized by the state store (see state.SanitizeComponent).
func (l Layout) StatePath(repoInst, session, agent string) string {
	return filepath.Join(l.StateDir, repoInst, session, agent+".json")
}

// ClientDir returns the config root of client c.
func (l Layout) ClientDir(c model.ClientID) string {
	switch c {
	case model.ClientClaude:
		return l.ClaudeDir
	case model.ClientCodex:
		return l.CodexDir
	case model.ClientCursor:
		return l.CursorDir
	case model.ClientHermes:
		return l.HermesDir
	}
	return ""
}

// ClaudeManagedSettings returns <ManagedDir>/managed-settings.json.
func (l Layout) ClaudeManagedSettings() string {
	return filepath.Join(l.ManagedDir, "managed-settings.json")
}

// ClaudeManagedSettingsD returns <ManagedDir>/managed-settings.d.
func (l Layout) ClaudeManagedSettingsD() string {
	return filepath.Join(l.ManagedDir, "managed-settings.d")
}
