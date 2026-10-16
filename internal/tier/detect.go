package tier

import (
	"encoding/json"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// missingOrder is the order in which Missing ids are reported.
var missingOrder = []string{
	model.TierMissingOS,
	model.TierMissingEnvOverride,
	model.TierMissingBinaryOwner,
	model.TierMissingOrgConfig,
	model.TierMissingManagedFile,
	model.TierMissingManagedHooks,
	model.TierMissingManagedOnly,
	model.TierMissingSandbox,
	model.TierMissingDenyWrite,
	model.TierMissingPermDeny,
	model.TierMissingManagedPerms,
	model.TierMissingLowerSandbox,
	model.TierMissingUnreadable,
}

type detector struct {
	opt     Options
	fsys    sys.FS
	env     sys.Env
	owner   ownerFunc
	goos    string
	managed string // compiled-in (or test-overridden) managed dir, unresolved
	home    string
	missing map[string]bool
	evals   map[string]evalResult // EvalSymlinks memo: Self and the hook command are usually the same path
}

type evalResult struct {
	path string
	err  error
}

func (d *detector) miss(id string) { d.missing[id] = true }

// eval is EvalSymlinks memoized for the lifetime of one detection (one
// Lstat per path component each time it is not).
func (d *detector) eval(p string) (string, error) {
	if r, ok := d.evals[p]; ok {
		return r.path, r.err
	}
	r, err := d.fsys.EvalSymlinks(p)
	if d.evals == nil {
		d.evals = make(map[string]evalResult, 2)
	}
	d.evals[p] = evalResult{r, err}
	return r, err
}

func detect(opt Options) model.TierInfo {
	d := &detector{opt: opt, fsys: opt.FS, env: opt.Env, owner: ownerOrDefault(opt.Owner), missing: map[string]bool{}}
	if d.fsys == nil {
		d.fsys = sys.OSFS{}
	}
	if d.env == nil {
		d.env = sys.MapEnv{}
	}
	d.goos = opt.GOOS
	if d.goos == "" {
		d.goos = opt.Layout.GOOS
	}
	info := model.TierInfo{
		Tier:            model.TierT0,
		SandboxPolicy:   model.SandboxOff,
		Network:         model.NetworkDeny,
		WeakenedProject: effectiveWeakened(OrgConfig{}),
	}
	// Condition 1.
	if d.goos != "darwin" && d.goos != "linux" {
		info.Missing = []string{model.TierMissingOS}
		return info
	}
	d.managed = opt.ManagedDir
	if d.managed == "" {
		d.managed = layout.ManagedDirFor(d.goos)
	}
	d.home = opt.Layout.InstallRoot
	if d.home == "" {
		if h, err := d.env.HomeDir(); err == nil {
			d.home = h
		}
	}

	// Condition 4 first: it decides whether the host is locked, which
	// decides condition 2.
	org := readOrg(d.fsys, d.owner, d.managed)
	if org.unreadable {
		d.miss(model.TierMissingUnreadable)
	}
	locked := lockedFrom(org)
	info.Locked = locked
	if !(org.fileTrusted && org.chainOK && org.org.Tier == model.TierT1) {
		d.miss(model.TierMissingOrgConfig)
	}
	if org.fileTrusted {
		o := org.org
		// Declared is what a root-owned org.json says; doctor and the hook
		// compare it with the verdict (tier_mismatch).
		info.Declared = o.Tier
	}
	// The policy values are adopted only when the whole parent chain is
	// trusted as well (condition 4): a group-writable managed directory lets
	// a group member replace org.json, so its contents are not believed.
	if org.fileTrusted && org.chainOK {
		o := org.org
		info.SandboxPolicy = o.RunnerSandbox
		if info.SandboxPolicy == "" {
			if o.Tier == model.TierT1 {
				info.SandboxPolicy = model.SandboxRequire
			} else {
				info.SandboxPolicy = model.SandboxOff
			}
		}
		if o.RunnerNetwork != "" {
			info.Network = o.RunnerNetwork
		}
		info.WeakenedProject = effectiveWeakened(o)
		for _, e := range o.ProtectedExtra {
			info.ProtectedExtra = append(info.ProtectedExtra, expandHome(e, d.home))
		}
		for _, e := range o.RunnerAllowWrite {
			info.RunnerAllowWrite = append(info.RunnerAllowWrite, expandHome(e, d.home))
		}
	}

	// Condition 2.
	if !locked {
		for _, k := range []string{layout.EnvHome, layout.EnvManagedDir, layout.EnvInstallRoot} {
			if v, ok := d.env.Getenv(k); ok && v != "" {
				d.miss(model.TierMissingEnvOverride)
			}
		}
	}

	// Condition 3.
	self, selfOK := d.checkSelf()
	if !selfOK {
		d.miss(model.TierMissingBinaryOwner)
	}

	// Condition 5.
	d.checkManagedSettings(org, self)

	// Condition 6.
	d.checkLowerLayers()

	for _, id := range missingOrder {
		if d.missing[id] {
			info.Missing = append(info.Missing, id)
		}
	}
	if len(info.Missing) == 0 {
		info.Tier = model.TierT1
	}
	return info
}

// checkSelf implements condition 3 and returns EvalSymlinks(Self).
func (d *detector) checkSelf() (resolved string, ok bool) {
	self := d.opt.Self
	if self == "" || !path.IsAbs(self) {
		return "", false
	}
	r, err := d.eval(self)
	if err != nil {
		if !isNotExist(err) {
			d.miss(model.TierMissingUnreadable)
		}
		return "", false
	}
	_, fok, err := trustedFile(d.fsys, d.owner, r)
	if err != nil {
		d.miss(model.TierMissingUnreadable)
		return r, false
	}
	if !fok {
		return r, false
	}
	cok, err := trustedChain(d.fsys, d.owner, path.Dir(r))
	if err != nil {
		d.miss(model.TierMissingUnreadable)
	}
	return r, cok
}

// managedDoc is one parsed Claude managed settings file.
type managedDoc map[string]any

func (d *detector) loadManaged(resolvedManaged string) (docs []managedDoc, present, bad bool) {
	load := func(p string) {
		present = true
		_, ok, err := trustedFile(d.fsys, d.owner, p)
		if err != nil {
			d.miss(model.TierMissingUnreadable)
			bad = true
			return
		}
		if !ok {
			bad = true
		}
		b, err := d.fsys.ReadFile(p)
		if err != nil {
			d.miss(model.TierMissingUnreadable)
			bad = true
			return
		}
		var m managedDoc
		if err := json.Unmarshal(b, &m); err != nil || m == nil {
			d.miss(model.TierMissingUnreadable)
			bad = true
			return
		}
		docs = append(docs, m)
	}
	base := path.Join(resolvedManaged, "managed-settings.json")
	if _, err := d.fsys.Lstat(base); err == nil {
		load(base)
	} else if !isNotExist(err) {
		present, bad = true, true
		d.miss(model.TierMissingUnreadable)
	}
	dir := path.Join(resolvedManaged, "managed-settings.d")
	fi, err := d.fsys.Lstat(dir)
	switch {
	case err == nil:
		if ok, _ := trustedDir(d.fsys, d.owner, dir); !ok || !fi.IsDir() {
			// Reported as managed_settings, but the files are still read so
			// the remaining ids describe their content.
			bad = true
			present = true
		}
		ents, err := d.fsys.ReadDir(dir)
		if err != nil {
			present, bad = true, true
			d.miss(model.TierMissingUnreadable)
			break
		}
		for _, e := range ents {
			n := e.Name()
			if !strings.HasSuffix(n, ".json") || strings.HasPrefix(n, ".") {
				continue
			}
			load(path.Join(dir, n))
		}
	case !isNotExist(err):
		present, bad = true, true
		d.miss(model.TierMissingUnreadable)
	}
	return docs, present, bad
}

func (doc managedDoc) get(keys ...string) (any, bool) {
	var cur any = map[string]any(doc)
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[k]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// explicit reports whether at least one doc sets keys to want and none sets
// it to anything else.
func explicit(docs []managedDoc, want bool, keys ...string) bool {
	seen := false
	for _, doc := range docs {
		v, ok := doc.get(keys...)
		if !ok {
			continue
		}
		b, isb := v.(bool)
		if !isb || b != want {
			return false
		}
		seen = true
	}
	return seen
}

// absentOrFalse: no doc sets keys to anything but false/null.
func absentOrFalse(docs []managedDoc, keys ...string) bool {
	for _, doc := range docs {
		v, ok := doc.get(keys...)
		if !ok || v == nil {
			continue
		}
		if b, isb := v.(bool); !isb || b {
			return false
		}
	}
	return true
}

// absentOrEmpty: no doc sets keys to anything but an empty array/null.
func absentOrEmpty(docs []managedDoc, keys ...string) bool {
	for _, doc := range docs {
		v, ok := doc.get(keys...)
		if !ok || v == nil {
			continue
		}
		if a, isa := v.([]any); !isa || len(a) != 0 {
			return false
		}
	}
	return true
}

// stringUnion is the union of the string elements of the arrays at keys.
func stringUnion(docs []managedDoc, keys ...string) map[string]bool {
	out := map[string]bool{}
	for _, doc := range docs {
		v, ok := doc.get(keys...)
		if !ok {
			continue
		}
		if a, isa := v.([]any); isa {
			for _, e := range a {
				if s, iss := e.(string); iss {
					out[s] = true
				}
			}
		}
	}
	return out
}

// findHook returns the command (exactly as written) of the first managed
// Ballast hook entry: absolute command whose symlink-free form equals self,
// args starting "hook","--client".
func (d *detector) findHook(docs []managedDoc, self string) (string, bool) {
	if self == "" {
		return "", false
	}
	for _, doc := range docs {
		hooks, _ := doc["hooks"].(map[string]any)
		events := make([]string, 0, len(hooks))
		for e := range hooks {
			events = append(events, e)
		}
		sort.Strings(events)
		for _, ev := range events {
			groups, _ := hooks[ev].([]any)
			for _, g := range groups {
				gm, _ := g.(map[string]any)
				entries, _ := gm["hooks"].([]any)
				for _, en := range entries {
					em, _ := en.(map[string]any)
					if t, _ := em["type"].(string); t != "command" {
						continue
					}
					cmd, _ := em["command"].(string)
					if cmd == "" || !path.IsAbs(cmd) {
						continue
					}
					args, _ := em["args"].([]any)
					if len(args) < 2 || args[0] != "hook" || args[1] != "--client" {
						continue
					}
					r, err := d.eval(cmd)
					if err != nil || r != self {
						continue
					}
					return cmd, true
				}
			}
		}
	}
	return "", false
}

func (d *detector) checkManagedSettings(org orgState, self string) {
	resolved := org.resolvedManaged
	if resolved == "" {
		if r, err := d.fsys.EvalSymlinks(d.managed); err == nil {
			resolved = r
		} else {
			resolved = d.managed
		}
	}
	docs, present, bad := d.loadManaged(resolved)
	if !present || len(docs) == 0 {
		// Nothing (parsable) to evaluate: only managed_settings (and
		// unreadable, already recorded) is reported.
		d.miss(model.TierMissingManagedFile)
		d.checkClaudeConfigDir()
		return
	}
	if bad {
		d.miss(model.TierMissingManagedFile)
	}

	if !explicit(docs, true, "allowManagedHooksOnly") {
		d.miss(model.TierMissingManagedOnly)
	}
	cmd, found := d.findHook(docs, self)
	if !found {
		d.miss(model.TierMissingManagedHooks)
	}
	if !explicit(docs, true, "sandbox", "enabled") ||
		!explicit(docs, false, "sandbox", "allowUnsandboxedCommands") ||
		!explicit(docs, true, "sandbox", "failIfUnavailable") ||
		!absentOrEmpty(docs, "sandbox", "excludedCommands") ||
		!absentOrEmpty(docs, "sandbox", "network", "allowUnixSockets") ||
		!absentOrFalse(docs, "sandbox", "enableWeakerNestedSandbox") ||
		!absentOrFalse(docs, "sandbox", "network", "allowAllUnixSockets") {
		d.miss(model.TierMissingSandbox)
	}
	if !explicit(docs, true, "allowManagedPermissionRulesOnly") {
		d.miss(model.TierMissingManagedPerms)
	}
	if found {
		lay := layout.Layout{GOOS: d.goos, OrgRules: path.Join(d.managed, "ballast", "rules.json")}
		dw, pd := managedForms(lay, cmd, org.org.ProtectedExtra)
		have := stringUnion(docs, "sandbox", "filesystem", "denyWrite")
		for _, s := range dw {
			if !have[s] {
				d.miss(model.TierMissingDenyWrite)
				break
			}
		}
		have = stringUnion(docs, "permissions", "deny")
		for _, s := range pd {
			if !have[s] {
				d.miss(model.TierMissingPermDeny)
				break
			}
		}
	}
	d.checkClaudeConfigDir()
}

// checkClaudeConfigDir: CLAUDE_CONFIG_DIR redirected away from <home>/.claude
// puts the user-layer files outside the literal forms of ManagedForms.
func (d *detector) checkClaudeConfigDir() {
	v, ok := d.env.Getenv(layout.EnvClaudeDir)
	if !ok || v == "" {
		return
	}
	if d.home == "" || path.Clean(v) != path.Join(d.home, ".claude") {
		d.miss(model.TierMissingDenyWrite)
	}
}

// checkLowerLayers implements condition 6 for the user layer.
func (d *detector) checkLowerLayers() {
	if d.home == "" {
		d.miss(model.TierMissingUnreadable)
		d.miss(model.TierMissingLowerSandbox)
		return
	}
	dirs := []string{path.Join(d.home, ".claude")}
	if v, ok := d.env.Getenv(layout.EnvClaudeDir); ok && v != "" {
		if c := path.Clean(v); c != dirs[0] {
			dirs = append(dirs, c)
		}
	}
	for _, dir := range dirs {
		ents, err := d.fsys.ReadDir(dir)
		if err != nil {
			if !isNotExist(err) {
				d.miss(model.TierMissingLowerSandbox)
			}
			continue
		}
		for _, e := range ents {
			if !isSettingsName(e.Name()) {
				continue
			}
			if e.IsDir() {
				continue
			}
			if t := e.Type(); t&fs.ModeSymlink == 0 && !t.IsRegular() {
				d.miss(model.TierMissingLowerSandbox)
				continue
			}
			b, err := d.fsys.ReadFile(path.Join(dir, e.Name()))
			if err != nil {
				d.miss(model.TierMissingLowerSandbox)
				continue
			}
			keys, err := weakenedSandboxKeys(b, LayerUser)
			if err != nil || len(keys) > 0 {
				d.miss(model.TierMissingLowerSandbox)
			}
		}
	}
}
