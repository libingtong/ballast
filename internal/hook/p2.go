package hook

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/model"
)

// P2 additions (docs/p2-design.md §3.3, §5.1): hook-started verify under T1,
// the session-level project-layer sandbox checks, env_redirect_ignored and
// the ConfigChange audit. Everything here is a pure function of the event
// and the injected ports; nothing spawns except Deps.Spawner.

// Frozen user-facing texts.
const (
	verifyDenyAll = "全部 required"
	// verifyDenyFmt: request_id, names (or verifyDenyAll), request_id.
	verifyDenyFmt = "ballast(T1):已在沙箱外后台启动 verify,job=%s(%s)。不要重复执行;用 `ballast verify --status %s` 查看结果,完成后结果也会在下一次注入中回报。"
	// verifyFailFmt: error, `ballast verify <names>`.
	verifyFailFmt = "无法在后台启动 verify:%s;请让用户在沙箱外执行 `%s`"
	// weakenedDenyFmt: entries.
	weakenedDenyFmt = "ballast(T1):本项目的 .claude/settings*.json 含削弱托管沙箱的设置(%s),组织策略禁止在此项目中调用工具。请移除这些设置或联系管理员。"
)

// Bounds on the weakened-settings entries quoted in the deny reason and the
// lower_sandbox_weakened audit detail. The keys come from the project's
// .claude/settings*.json, which the repo (hence an agent or a third party)
// controls, so neither their number nor their length is trusted.
const (
	maxWeakenedEntries    = 8
	maxWeakenedEntryRunes = 80
	weakenedMoreFmt       = "…等 %d 项"
)

// maxSpawnErrRunes bounds the spawn error quoted in the deny reason and audit.
const maxSpawnErrRunes = 200

// jobIDFormat is the UTC job-id timestamp layout (runner/job.go).
const jobIDFormat = "20060102T150405"

// synthetic is a deny formed by the hook itself (not a rule hit).
type synthetic struct {
	reason string
}

// isClaude reports whether ev comes from Claude Code, the only client with a
// T1 and a project layer.
func isClaude(ev *model.Event) bool { return ev.Client == model.ClientClaude }

// projectDirs lists the directories whose .claude/settings*.json Claude loads
// as the session's project and local layers (docs/p2-design.md §3.3): only
// $CLAUDE_PROJECT_DIR when it is absolute, otherwise the event cwd and the
// repo top level when it differs.
func (inv *invocation) projectDirs(ev *model.Event, repo *model.RepoInfo) []string {
	if v, ok := inv.deps.Env.Getenv("CLAUDE_PROJECT_DIR"); ok && filepath.IsAbs(v) {
		return []string{v}
	}
	var dirs []string
	if ev.Cwd != "" {
		dirs = append(dirs, ev.Cwd)
	}
	if repo != nil && repo.Toplevel != "" && repo.Toplevel != ev.Cwd {
		dirs = append(dirs, repo.Toplevel)
	}
	return dirs
}

// weakenedEntries runs the ProjectSandbox check once per invocation and
// returns the sorted, de-duplicated "<basename>:<key>" entries.
func (inv *invocation) weakenedEntries(ev *model.Event, repo *model.RepoInfo) []string {
	if inv.weakDone {
		return inv.weakEntries
	}
	inv.weakDone = true
	chk := inv.deps.ProjectSandbox
	if chk == nil || !isClaude(ev) {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, dir := range inv.projectDirs(ev, repo) {
		for _, e := range chk.Weakened(dir) {
			if _, dup := seen[e]; dup {
				continue
			}
			seen[e] = struct{}{}
			out = append(out, e)
		}
	}
	sort.Strings(out)
	inv.weakEntries = out
	return out
}

// boundEntries caps the entry list shown to the agent and written to the
// audit: at most maxWeakenedEntries entries of at most maxWeakenedEntryRunes
// runes each (single line); total is the full count so callers can say how
// many were left out.
func boundEntries(entries []string) (shown []string, total int) {
	total = len(entries)
	n := total
	if n > maxWeakenedEntries {
		n = maxWeakenedEntries
	}
	shown = make([]string, 0, n)
	nl := strings.NewReplacer("\r", " ", "\n", " ")
	for _, e := range entries[:n] {
		shown = append(shown, clipRunes(nl.Replace(e), maxWeakenedEntryRunes))
	}
	return shown, total
}

// weakenedDenyActive reports whether the org policy denies every PreToolUse
// of a session whose project layer is weakened.
func (inv *invocation) weakenedDenyActive(ev *model.Event) bool {
	return inv.deps.WeakenedProject == model.WeakenedProjectDeny &&
		inv.deps.ProjectSandbox != nil && isClaude(ev) && ev.Name == model.EventPreToolUse
}

// firstEventDiags returns the diags written on the first event of a session
// (State.StartedAt() false for the scope): env_redirect_ignored and
// lower_sandbox_weakened.
func (inv *invocation) firstEventDiags(ev *model.Event, repo *model.RepoInfo) []diag {
	var out []diag
	d := inv.deps
	if len(d.RedirectsIgnored) > 0 {
		out = append(out, diag{name: model.DiagEnvRedirectIgnored, fields: map[string]any{
			"vars": append([]string(nil), d.RedirectsIgnored...),
		}})
	}
	if d.ProjectSandbox != nil && isClaude(ev) {
		if entries := inv.weakenedEntries(ev, repo); len(entries) > 0 {
			shown, total := boundEntries(entries)
			// The frozen detail shape is {entries:[…]}: a cut list is marked by a
			// trailing placeholder entry, not by an extra key.
			if total > len(shown) {
				shown = append(shown, fmt.Sprintf(weakenedMoreFmt, total-len(shown)))
			}
			out = append(out, diag{name: model.DiagLowerSandboxWeakened, fields: map[string]any{"entries": shown}})
		}
	}
	return out
}

// syntheticDecision forms the hook's own deny for this event, if any:
//
//  1. org policy weakened_project:"deny" and the project layer is weakened ⇒
//     deny (no verify is started for such a session);
//  2. a canonical `ballast verify` under T1 ⇒ start the verify detached and
//     deny with the frozen reason.
//
// Diags describing what happened are returned alongside.
func (inv *invocation) syntheticDecision(ev *model.Event, repo *model.RepoInfo, cmds model.ShellCommands, key model.ScopeKey) (*synthetic, []diag) {
	if ev.Name != model.EventPreToolUse || !isClaude(ev) {
		return nil, nil
	}
	if inv.weakenedDenyActive(ev) {
		if entries := inv.weakenedEntries(ev, repo); len(entries) > 0 {
			tool := ""
			if ev.Tool != nil {
				tool = ev.Tool.Name
			}
			shown, total := boundEntries(entries)
			list := strings.Join(shown, ", ")
			if total > len(shown) {
				list += ", " + fmt.Sprintf(weakenedMoreFmt, total-len(shown))
			}
			return &synthetic{reason: fmt.Sprintf(weakenedDenyFmt, list)},
				[]diag{{name: model.DiagLowerSandboxDeny, fields: map[string]any{"tool": tool}}}
		}
	}
	t := ev.Tool
	d := inv.deps
	// Tier nil ⇒ T0: nothing to do, so the command is not even parsed here
	// (the hook itself never parses unless it can act on the result).
	if d.Tier == nil || t == nil || cmds == nil || (t.Shell != model.ShellBash && t.Shell != model.ShellPowerShell) {
		return nil, nil
	}
	cl, err := cmds.Get()
	if err != nil {
		return nil, nil
	}
	vr, ok := model.ParseVerifyRequest(cl)
	if !ok {
		return nil, nil
	}
	info := d.Tier.Probe()
	inv.tier = info.Tier
	if info.Tier != model.TierT1 {
		if info.Declared == model.TierT1 {
			return nil, []diag{{name: model.DiagTierMismatch, fields: map[string]any{
				"declared": string(info.Declared), "missing": nonNilStrings(info.Missing),
			}}}
		}
		return nil, nil
	}
	return inv.startVerify(ev, key, vr)
}

// startVerify spawns `ballast verify --hook-start …` for a canonical verify
// and returns the synthetic deny plus audit diags.
func (inv *invocation) startVerify(ev *model.Event, key model.ScopeKey, vr model.VerifyRequest) (*synthetic, []diag) {
	d := inv.deps
	names := nonNilStrings(vr.Names)
	var reqID string
	var err error
	switch {
	case d.IDs == nil:
		err = fmt.Errorf("no id generator")
	case d.Spawner == nil || d.Self == "":
		err = fmt.Errorf("no spawner")
	case !filepath.IsAbs(ev.Cwd):
		err = fmt.Errorf("event cwd is not absolute")
	default:
		reqID = d.Clock.Now().UTC().Format(jobIDFormat) + "-" + d.IDs.NewID(3)
		projectDir := ""
		if v, ok := d.Env.Getenv("CLAUDE_PROJECT_DIR"); ok && filepath.IsAbs(v) {
			projectDir = v
		}
		argv := model.HookStartArgv(d.Self, model.HookStartRequest{
			RequestID: reqID, Cwd: ev.Cwd, Names: vr.Names, Client: ev.Client,
			SessionID: ev.SessionID, AgentID: ev.AgentID, ProjectDir: projectDir,
		})
		logPath := filepath.Join(d.Layout.RequestsDir, key.RepoInst, reqID+".spawn.log")
		_, err = d.Spawner.SpawnDetached(argv, d.Env.Environ(), logPath)
	}
	fields := map[string]any{"names": names, "spawn_ok": err == nil}
	if reqID != "" {
		fields["request_id"] = reqID
	}
	if err == nil {
		list := verifyDenyAll
		if len(vr.Names) > 0 {
			list = strings.Join(vr.Names, ", ")
		}
		return &synthetic{reason: fmt.Sprintf(verifyDenyFmt, reqID, list, reqID)},
			[]diag{{name: model.DiagVerifyHookStart, fields: fields}}
	}
	msg := clipRunes(oneLine(inv.scrubHome(err.Error())), maxSpawnErrRunes)
	cmd := "ballast verify"
	if len(vr.Names) > 0 {
		cmd += " " + strings.Join(vr.Names, " ")
	}
	failFields := map[string]any{"names": names, "error": msg}
	if reqID != "" {
		failFields["request_id"] = reqID
	}
	// A failed spawn is audited as verify_spawn_failed only (design §3.3 step
	// 4); verify_hook_start means a verify was actually started.
	return &synthetic{reason: fmt.Sprintf(verifyFailFmt, msg, cmd)},
		[]diag{{name: model.DiagVerifySpawnFailed, fields: failFields}}
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// mergeSynthetic folds a synthetic deny into the rule verdict: the action
// becomes deny (the strictest), the synthetic reason comes first and the
// rule reasons follow within model.BudgetReason runes. Hits are untouched
// (a synthetic deny is not a rule hit).
func mergeSynthetic(v *model.Verdict, s *synthetic) *model.Verdict {
	out := *v
	out.Action = model.Stricter(v.Action, model.ActionDeny)
	reason := s.reason
	if rest := strings.TrimSpace(v.Reason); rest != "" {
		if room := model.BudgetReason - utf8.RuneCountInString(reason) - 1; room > 1 {
			reason += "\n" + clipRunes(rest, room)
		}
	}
	out.Reason = reason
	return &out
}

// ---- ConfigChange audit (docs/p2-design.md §5.1) ---------------------------

// configChangeDetail is the detail of a config_change audit line. Empty
// source/file are omitted; protected is only judged when there is a file.
type configChangeDetail struct {
	Source    string `json:"source,omitempty"`
	File      string `json:"file,omitempty"`
	Protected *bool  `json:"protected,omitempty"`
}

// configChange writes the audit line of a ConfigChange event. It never
// blocks (stdout stays empty) and neither reads nor writes session state.
func (inv *invocation) configChange(ev *model.Event, repo *model.RepoInfo, pc model.PathContext, key model.ScopeKey, caps model.CapabilitySet) (*Result, func(), error) {
	d := inv.deps
	det := configChangeDetail{Source: ev.Source}
	if ev.ConfigFile != "" {
		det.File = inv.displayPath(ev.ConfigFile, repo)
		p := inv.protectedPath(ev, pc, repo)
		det.Protected = &p
	}
	raw, err := json.Marshal(det)
	if err != nil {
		return nil, nil, fmt.Errorf("config_change detail: %w", err)
	}
	for _, p := range []FaultPoint{FaultLoad, FaultStateRead, FaultEvaluate, FaultDecided, FaultStateCommit} {
		if err := inv.fault(p); err != nil {
			return nil, nil, err
		}
	}
	ab := &auditBuilder{deps: d, ev: ev, repo: repo, key: key, caps: caps, start: inv.start}
	rec := ab.base(model.AuditConfigChange)
	rec.Detail = raw
	if err := inv.fault(FaultAuditWrite); err != nil {
		return nil, nil, err
	}
	if err := d.Audit.Append([]model.AuditRecord{rec}); err != nil {
		return nil, nil, fmt.Errorf("append audit: %w", err)
	}
	if err := inv.fault(FaultRender); err != nil {
		return nil, nil, err
	}
	dec := &model.Decision{Event: ev.Name, Native: ev.Native, Action: model.ActionNone}
	return &Result{Decision: dec, Audit: []model.AuditRecord{rec}, Event: ev}, func() {}, nil
}

// scrubHome rewrites the Ballast home and the user's home directory inside an
// error text (an *os.PathError carries the absolute path) to "~/.ballast" and
// "~", so the audit line and the deny reason carry no user name.
func (inv *invocation) scrubHome(msg string) string {
	d := inv.deps
	var pairs []string
	add := func(dir, repl string) {
		if dir = strings.TrimRight(dir, `/\`); dir != "" && dir != "." {
			pairs = append(pairs, dir, repl)
		}
	}
	add(d.Layout.Home, "~/.ballast") // the more specific prefix first
	if home, err := d.Env.HomeDir(); err == nil {
		add(home, "~")
	}
	if len(pairs) == 0 {
		return msg
	}
	return strings.NewReplacer(pairs...).Replace(msg)
}

// displayPath rewrites an absolute config path into "<repo>/…" or "~/…"
// form; a path under neither keeps only its basename (no usernames, no
// directory layout in the audit trail). The path is cleaned first, so a
// "/repo/../../Users/x" or "~/../.." cannot smuggle the layout past the
// prefix test.
func (inv *invocation) displayPath(p string, repo *model.RepoInfo) string {
	d := inv.deps
	q := path.Clean(strings.ReplaceAll(p, `\`, "/"))
	fold := caseInsensitiveFS(d.Env.GOOS())
	under := func(root string) (string, bool) {
		root = strings.TrimRight(strings.ReplaceAll(root, `\`, "/"), "/")
		if root == "" {
			return "", false
		}
		prefix := root + "/"
		if len(q) <= len(prefix) {
			return "", false
		}
		head := q[:len(prefix)]
		if head == prefix || fold && strings.EqualFold(head, prefix) {
			rel := q[len(prefix):]
			if rel == ".." || strings.HasPrefix(rel, "../") {
				return "", false
			}
			return rel, true
		}
		return "", false
	}
	if repo != nil {
		if rel, ok := under(repo.Toplevel); ok {
			return "<repo>/" + rel
		}
	}
	if strings.HasPrefix(q, "~/") && len(q) > 2 {
		return q
	}
	if home, err := d.Env.HomeDir(); err == nil {
		if rel, ok := under(home); ok {
			return "~/" + rel
		}
	}
	base := path.Base(q)
	if base == "." || base == ".." || base == "/" {
		return ""
	}
	return base
}

// protectedPath reports whether the changed file is one Ballast protects:
// anything under ~/.ballast, the org rules directory, the managed settings
// directory or <repo>/.ballast, or a Claude settings*.json of the user, the
// repo, a project directory Claude loads (CLAUDE_PROJECT_DIR or the event
// cwd), or any other .claude/settings*.json (the managed any-depth form); the
// directory of the running binary also counts. protected_extra from org.json
// is not visible here. Decided on PathService-normalized paths.
func (inv *invocation) protectedPath(ev *model.Event, pc model.PathContext, repo *model.RepoInfo) bool {
	d := inv.deps
	norm := d.Paths.Normalize(ev.ConfigFile, ev.Cwd, pc)
	if norm == "" {
		return false
	}
	fold := caseInsensitiveFS(d.Env.GOOS())
	cmp := func(s string) string {
		if fold {
			return strings.ToLower(s)
		}
		return s
	}
	n := cmp(norm)
	n2 := func(p string) string {
		if p == "" {
			return ""
		}
		return cmp(d.Paths.Normalize(p, "", pc))
	}
	inDir := func(dir string) bool {
		dir = n2(dir)
		return dir != "" && dir != "/" && strings.HasPrefix(n, strings.TrimRight(dir, "/")+"/")
	}
	lay := d.Layout
	dirs := []string{lay.Home, lay.ManagedDir}
	if lay.OrgRules != "" {
		dirs = append(dirs, filepath.Dir(lay.OrgRules))
	}
	settingsDirs := []string{lay.ClaudeDir, pc.Home + "/.claude"}
	if repo != nil && repo.Toplevel != "" {
		// Joined as strings on the normalized top level, not with
		// filepath.Join: Normalize already yields '/' separators on every OS.
		if top := strings.TrimRight(d.Paths.Normalize(repo.Toplevel, "", pc), "/"); top != "" {
			dirs = append(dirs, top+"/.ballast")
			settingsDirs = append(settingsDirs, top+"/.claude")
		}
	}
	if pc.ProjectRoot != "" {
		settingsDirs = append(settingsDirs, pc.ProjectRoot+"/.claude")
	}
	// The directories Claude actually loads the project layers from
	// ($CLAUDE_PROJECT_DIR, or the event cwd: a monorepo sub-directory).
	for _, pd := range inv.projectDirs(ev, repo) {
		if np := strings.TrimRight(d.Paths.Normalize(pd, "", pc), "/"); np != "" {
			settingsDirs = append(settingsDirs, np+"/.claude")
		}
	}
	// The directory of the running binary (managed binary_dir entry).
	if d.Self != "" {
		if sd := path.Dir(d.Paths.Normalize(d.Self, "", pc)); sd != "." && sd != "/" {
			dirs = append(dirs, sd)
		}
	}
	for _, dir := range dirs {
		if inDir(dir) {
			return true
		}
	}
	base := path.Base(n)
	if ok, _ := path.Match("settings*.json", base); ok {
		parent := path.Dir(n)
		// The managed denyWrite forms also name **/.claude/settings*.json at
		// any depth (docs/p2-design.md §3.2): protected wherever it is.
		if path.Base(parent) == ".claude" {
			return true
		}
		for _, sd := range settingsDirs {
			if sd2 := n2(sd); sd2 != "" && sd2 == parent {
				return true
			}
		}
	}
	return false
}
