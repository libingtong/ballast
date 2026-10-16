package tier

import (
	"encoding/json"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/sys"
)

// Fake host used by the table tests. Everything is fake: no real file is
// consulted and no process environment is touched.
const (
	tManaged = "/etc/claude-code"
	tMS      = "/etc/claude-code/managed-settings.d/ballast.json"
	tSelf    = "/usr/local/libexec/ballast/ballast"
	tHome    = "/home/u"
	tUser    = 1000
)

var baseExtra = []string{"~/.config/acme/ci-token"}

// settingsDoc renders the managed-settings fragment `org render` would write
// for a hook command and protected extras (via ManagedForms, like Detect).
func settingsDoc(command string, extra []string) map[string]any {
	return settingsDocAt(tManaged, command, extra)
}

func settingsDocAt(managed, command string, extra []string) map[string]any {
	lay := layout.Layout{GOOS: "linux", OrgRules: managed + "/ballast/rules.json"}
	dw, pd := ManagedForms(lay, command, extra)
	hook := map[string]any{"type": "command", "command": command, "args": []any{"hook", "--client", "claude-code", "--event", "PreToolUse"}}
	return map[string]any{
		"allowManagedHooksOnly": true,
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{hook}}},
			"Stop":       []any{map[string]any{"hooks": []any{hook}}},
		},
		"sandbox": map[string]any{
			"enabled": true, "allowUnsandboxedCommands": false, "failIfUnavailable": true,
			"excludedCommands": []any{},
			"filesystem":       map[string]any{"denyWrite": toAny(dw)},
		},
		"permissions":                     map[string]any{"deny": toAny(pd)},
		"allowManagedPermissionRulesOnly": true,
	}
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func mustJSON(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func baseOrgJSON() string {
	return `{"v":1,"tier":"T1","runner_sandbox":"require","runner_network":"deny","weakened_project":"deny","protected_extra":["~/.config/acme/ci-token"]}`
}

// baseline returns a fake host on which every T1 condition holds.
func baseline(t testing.TB) (*memFS, Options) {
	t.Helper()
	m := newMemFS()
	for _, d := range []string{"/etc", tManaged, tManaged + "/ballast", tManaged + "/managed-settings.d", "/usr", "/usr/local", "/usr/local/libexec", "/usr/local/libexec/ballast", "/usr/local/bin"} {
		m.mkdirAll(d, 0, 0o755)
	}
	m.writeFile(tSelf, "#!binary", 0, 0o755)
	m.writeFile(tManaged+"/ballast/org.json", baseOrgJSON(), 0, 0o644)
	m.writeFile(tMS, mustJSON(t, settingsDoc(tSelf, baseExtra)), 0, 0o644)
	m.mkdirAll(tHome, tUser, 0o755)
	m.mkdirAll(tHome+"/.claude", tUser, 0o755)
	m.writeFile(tHome+"/.claude/settings.json", `{"model":"opus","permissions":{"allow":["Bash(ls)"]}}`, tUser, 0o644)
	opt := Options{
		FS:     m,
		Env:    sys.MapEnv{OS: "linux", Vars: map[string]string{"HOME": tHome}},
		Layout: layout.Layout{GOOS: "linux", InstallRoot: tHome, ClaudeDir: tHome + "/.claude", OrgRules: tManaged + "/ballast/rules.json"},
		Self:   tSelf,
		Owner:  memOwner,
	}
	return m, opt
}

func envWith(vars map[string]string) sys.MapEnv {
	v := map[string]string{"HOME": tHome}
	for k, x := range vars {
		v[k] = x
	}
	return sys.MapEnv{OS: "linux", Vars: v}
}

// --- table operations (testdata/detect.json) ---------------------------

type tableOp struct {
	Op      string          `json:"op"`
	Path    string          `json:"path"`
	Content string          `json:"content"`
	Target  string          `json:"target"`
	Mode    string          `json:"mode"`
	UID     *uint32         `json:"uid"`
	Key     []string        `json:"key"`
	Value   json.RawMessage `json:"value"`
	Delete  bool            `json:"delete"`
	Command string          `json:"command"`
	Extra   []string        `json:"extra"`
}

var subst = strings.NewReplacer("$MS", tMS, "$M", tManaged, "$SELF", tSelf, "$HOME", tHome)

func parseMode(t testing.TB, s string, def fs.FileMode) fs.FileMode {
	if s == "" {
		return def
	}
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		t.Fatalf("bad mode %q", s)
	}
	return fs.FileMode(v)
}

func deepSubst(v any) any {
	switch x := v.(type) {
	case string:
		return subst.Replace(x)
	case []any:
		for i := range x {
			x[i] = deepSubst(x[i])
		}
	case map[string]any:
		for k := range x {
			x[k] = deepSubst(x[k])
		}
	}
	return v
}

func applyOps(t testing.TB, m *memFS, ops []tableOp) {
	t.Helper()
	for _, o := range ops {
		p := subst.Replace(o.Path)
		uid := uint32(0)
		if strings.HasPrefix(p, tHome) {
			uid = tUser
		}
		if o.UID != nil {
			uid = *o.UID
		}
		switch o.Op {
		case "write":
			content := subst.Replace(o.Content)
			switch content {
			case "@ORG@":
				content = baseOrgJSON()
			case "@MS@":
				content = mustJSON(t, settingsDoc(tSelf, baseExtra))
			}
			m.writeFile(p, content, uid, parseMode(t, o.Mode, 0o644))
		case "mkdir":
			m.mkdirAll(p, uid, parseMode(t, o.Mode, 0o755))
		case "chmod":
			m.chmod(p, parseMode(t, o.Mode, 0o644))
		case "chown":
			m.chown(p, uid)
		case "remove":
			m.remove(p)
		case "symlink":
			m.symlink(p, subst.Replace(o.Target), uid)
		case "render":
			extra := o.Extra
			if extra == nil {
				extra = baseExtra
			}
			m.writeFile(p, mustJSON(t, settingsDoc(subst.Replace(o.Command), extra)), 0, 0o644)
		case "jsonset":
			n, ok := m.nodes[clean(p)]
			if !ok {
				t.Fatalf("jsonset: %s missing", p)
			}
			var doc map[string]any
			if err := json.Unmarshal(n.data, &doc); err != nil {
				t.Fatal(err)
			}
			cur := doc
			for _, k := range o.Key[:len(o.Key)-1] {
				next, ok := cur[k].(map[string]any)
				if !ok {
					next = map[string]any{}
					cur[k] = next
				}
				cur = next
			}
			last := o.Key[len(o.Key)-1]
			if o.Delete {
				delete(cur, last)
			} else {
				var v any
				if err := json.Unmarshal(o.Value, &v); err != nil {
					t.Fatal(err)
				}
				cur[last] = deepSubst(v)
			}
			n.data = []byte(mustJSON(t, doc))
		default:
			t.Fatalf("unknown op %q", o.Op)
		}
	}
}

var _ = path.Join
