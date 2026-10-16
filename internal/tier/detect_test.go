package tier

import (
	"encoding/json"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
)

type detectRow struct {
	Name     string            `json:"name"`
	Goos     string            `json:"goos"`
	Env      map[string]string `json:"env"`
	Ops      []tableOp         `json:"ops"`
	Tier     model.Tier        `json:"tier"`
	Missing  []string          `json:"missing"`
	Locked   *bool             `json:"locked"`
	Declared *model.Tier       `json:"declared"`
}

func loadRows[T any](t *testing.T, name string) []T {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Rows []T `json:"rows"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(f.Rows) == 0 {
		t.Fatalf("%s has no rows", name)
	}
	return f.Rows
}

func TestDetectTable(t *testing.T) {
	for _, r := range loadRows[detectRow](t, "detect.json") {
		t.Run(r.Name, func(t *testing.T) {
			m, opt := baseline(t)
			applyOps(t, m, r.Ops)
			vars := map[string]string{}
			for k, v := range r.Env {
				vars[k] = subst.Replace(v)
			}
			opt.Env = envWith(vars)
			if r.Goos != "" {
				opt.GOOS = r.Goos
			}
			got := Detect(opt)
			if got.Tier != r.Tier {
				t.Errorf("tier = %s (missing %v), want %s", got.Tier, got.Missing, r.Tier)
			}
			want := r.Missing
			if len(want) == 0 {
				want = nil
			}
			if !reflect.DeepEqual(got.Missing, want) {
				t.Errorf("missing = %v, want %v", got.Missing, want)
			}
			if (got.Tier == model.TierT1) != (len(got.Missing) == 0) {
				t.Errorf("tier %s inconsistent with missing %v", got.Tier, got.Missing)
			}
			if r.Locked != nil && got.Locked != *r.Locked {
				t.Errorf("locked = %v, want %v", got.Locked, *r.Locked)
			}
			if r.Declared != nil && got.Declared != *r.Declared {
				t.Errorf("declared = %q, want %q", got.Declared, *r.Declared)
			}
			// Detect is pure: locked must agree with the standalone lock.
			if r.Goos == "" {
				if l := Locked(m, "linux", memOwner); l != got.Locked {
					t.Errorf("Locked() = %v but Detect().Locked = %v", l, got.Locked)
				}
			}
		})
	}
}

func TestDetectEveryMissingIDIsKnown(t *testing.T) {
	known := map[string]bool{}
	for _, id := range missingOrder {
		known[id] = true
	}
	for _, r := range loadRows[detectRow](t, "detect.json") {
		for _, id := range r.Missing {
			if !known[id] {
				t.Errorf("row %s: unknown Missing id %q", r.Name, id)
			}
		}
	}
}

func TestDetectCoversEveryConditionID(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range loadRows[detectRow](t, "detect.json") {
		for _, id := range r.Missing {
			seen[id] = true
		}
	}
	for _, id := range []string{
		model.TierMissingOS, model.TierMissingEnvOverride, model.TierMissingBinaryOwner, model.TierMissingOrgConfig,
		model.TierMissingManagedFile, model.TierMissingManagedHooks, model.TierMissingManagedOnly, model.TierMissingSandbox,
		model.TierMissingDenyWrite, model.TierMissingPermDeny, model.TierMissingManagedPerms, model.TierMissingLowerSandbox,
		model.TierMissingUnreadable,
	} {
		if !seen[id] {
			t.Errorf("detect.json has no row producing %q", id)
		}
	}
}

func TestDetectPoliciesFromTrustedOrg(t *testing.T) {
	m, opt := baseline(t)
	m.writeFile(tManaged+"/ballast/org.json", `{"v":1,"tier":"T1","runner_sandbox":"prefer","runner_network":"loopback","weakened_project":"audit","runner_allow_write":["~/.cache/x","/srv/cache"],"protected_extra":["~/.config/acme/ci-token"]}`, 0, 0o644)
	got := Detect(opt)
	if got.Tier != model.TierT1 {
		t.Fatalf("tier %s %v", got.Tier, got.Missing)
	}
	if got.Declared != model.TierT1 || !got.Locked {
		t.Errorf("declared %q locked %v", got.Declared, got.Locked)
	}
	if got.SandboxPolicy != model.SandboxPrefer || got.Network != model.NetworkLoopback || got.WeakenedProject != model.WeakenedProjectAudit {
		t.Errorf("policies = %q %q %q", got.SandboxPolicy, got.Network, got.WeakenedProject)
	}
	if !reflect.DeepEqual(got.RunnerAllowWrite, []string{"/home/u/.cache/x", "/srv/cache"}) {
		t.Errorf("RunnerAllowWrite = %v", got.RunnerAllowWrite)
	}
	if !reflect.DeepEqual(got.ProtectedExtra, []string{"/home/u/.config/acme/ci-token"}) {
		t.Errorf("ProtectedExtra = %v", got.ProtectedExtra)
	}
}

func TestDetectPolicyDefaults(t *testing.T) {
	m, opt := baseline(t)
	m.writeFile(tManaged+"/ballast/org.json", `{"v":1,"tier":"T1"}`, 0, 0o644)
	got := Detect(opt)
	if got.SandboxPolicy != model.SandboxRequire || got.Network != model.NetworkDeny || got.WeakenedProject != DefaultWeakenedProject {
		t.Errorf("T1 defaults = %q %q %q", got.SandboxPolicy, got.Network, got.WeakenedProject)
	}
	// An untrusted org.json (not root-owned) must not influence the policies.
	m.writeFile(tManaged+"/ballast/org.json", `{"v":1,"tier":"T1","runner_sandbox":"prefer","runner_network":"allow","weakened_project":"audit"}`, tUser, 0o644)
	got = Detect(opt)
	if got.Tier != model.TierT0 || got.SandboxPolicy != model.SandboxOff || got.Network != model.NetworkDeny || got.Declared != "" {
		t.Errorf("untrusted org.json leaked: %+v", got)
	}
	// T0 declaration without a policy: off.
	m.writeFile(tManaged+"/ballast/org.json", `{"v":1,"tier":"T0"}`, 0, 0o644)
	if got = Detect(opt); got.SandboxPolicy != model.SandboxOff {
		t.Errorf("T0 default sandbox policy = %q", got.SandboxPolicy)
	}
}

// A group-writable managed directory (condition 4 fails on the chain) must
// not let its org.json steer the policy values, even though the file itself
// looks trusted.
func TestDetectPolicyNotAdoptedWhenChainUntrusted(t *testing.T) {
	m, opt := baseline(t)
	m.writeFile(tManaged+"/ballast/org.json", `{"v":1,"tier":"T1","runner_sandbox":"prefer","runner_network":"allow","weakened_project":"audit","protected_extra":["/x/p"],"runner_allow_write":["/x/w"]}`, 0, 0o644)
	m.chmod(tManaged, 0o775)
	got := Detect(opt)
	if got.Tier != model.TierT0 || !contains(got.Missing, model.TierMissingOrgConfig) {
		t.Fatalf("tier %s missing %v", got.Tier, got.Missing)
	}
	if got.Locked {
		t.Errorf("locked on an untrusted chain")
	}
	if got.SandboxPolicy != model.SandboxOff {
		t.Errorf("SandboxPolicy = %q leaked from the untrusted org.json", got.SandboxPolicy)
	}
	if got.Network != model.NetworkDeny || got.WeakenedProject != DefaultWeakenedProject ||
		len(got.ProtectedExtra) != 0 || len(got.RunnerAllowWrite) != 0 {
		t.Errorf("policy leaked: %+v", got)
	}
	// Declared stays visible for doctor.
	if got.Declared != model.TierT1 {
		t.Errorf("Declared = %q", got.Declared)
	}
}

func TestDetectUnreadableInputFailsClosed(t *testing.T) {
	m, opt := baseline(t)
	m.hook = func(op, name string) {}
	// A read error on the managed settings file is T0 + unreadable.
	failing := &failReadFS{memFS: m, fail: tMS}
	opt.FS = failing
	got := Detect(opt)
	if got.Tier != model.TierT0 {
		t.Fatalf("tier = %s", got.Tier)
	}
	if !contains(got.Missing, model.TierMissingUnreadable) {
		t.Errorf("missing %v lacks unreadable", got.Missing)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

type failReadFS struct {
	*memFS
	fail string
}

func (f *failReadFS) ReadFile(name string) ([]byte, error) {
	if strings.Contains(name, f.fail) {
		return nil, os.ErrPermission
	}
	return f.memFS.ReadFile(name)
}

func TestDetectNilAndWindowsDefaults(t *testing.T) {
	got := Detect(Options{GOOS: "windows"})
	if got.Tier != model.TierT0 || !reflect.DeepEqual(got.Missing, []string{model.TierMissingOS}) {
		t.Errorf("windows: %+v", got)
	}
	if got.SandboxPolicy != model.SandboxOff || got.Network != model.NetworkDeny {
		t.Errorf("windows policies: %+v", got)
	}
}

func TestProbeMemoizes(t *testing.T) {
	m, opt := baseline(t)
	p := New(opt)
	first := p.Probe()
	if first.Tier != model.TierT1 {
		t.Fatalf("tier %s %v", first.Tier, first.Missing)
	}
	before := m.calls.Load()
	if before == 0 {
		t.Fatal("first Probe performed no I/O")
	}
	second := p.Probe()
	if after := m.calls.Load(); after != before {
		t.Fatalf("second Probe performed %d I/O calls, want 0", after-before)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("memoized result differs: %+v vs %+v", first, second)
	}
	// Mutating a returned slice must not corrupt the memo.
	second.Missing = append(second.Missing, "x")
	if got := p.Probe(); len(got.Missing) != 0 {
		t.Errorf("memo corrupted: %v", got.Missing)
	}
}

func TestProbeConcurrent(t *testing.T) {
	_, opt := baseline(t)
	p := New(opt)
	done := make(chan model.TierInfo, 8)
	for i := 0; i < 8; i++ {
		go func() { done <- p.Probe() }()
	}
	for i := 0; i < 8; i++ {
		if got := <-done; got.Tier != model.TierT1 {
			t.Errorf("tier %s", got.Tier)
		}
	}
}

func TestDetectIsDeterministic(t *testing.T) {
	_, opt := baseline(t)
	a, b := Detect(opt), Detect(opt)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("not deterministic: %+v vs %+v", a, b)
	}
}

// Condition 5 judges permissions.deny by exact string inclusion of the M0b
// forms: Edit(...) entries with "//" for POSIX absolute paths. The pre-M0b
// single-slash Edit forms do not count as coverage (Claude does not read
// them as filesystem-absolute), and Write(...) / NotebookEdit(...) entries
// are neither required nor a substitute.
func TestDetectCondition5PermissionFormsM0b(t *testing.T) {
	_, pd := ManagedForms(layout.Layout{GOOS: "linux", OrgRules: tManaged + "/ballast/rules.json"}, tSelf, baseExtra)
	for _, s := range pd {
		if !strings.HasPrefix(s, "Edit(") {
			t.Fatalf("ManagedForms emits a non-Edit permission entry %q", s)
		}
	}
	hasAbs := false
	for _, s := range pd {
		if strings.HasPrefix(s, "Edit(//") {
			hasAbs = true
		}
		if strings.HasPrefix(s, "Edit(/") && !strings.HasPrefix(s, "Edit(//") {
			t.Errorf("single-slash absolute form %q", s)
		}
	}
	if !hasAbs {
		t.Fatal("no Edit(//...) entry")
	}

	cases := []struct {
		name string
		deny func(pd []string) []string
		want []string
	}{
		{"exact M0b forms", func(pd []string) []string { return pd }, nil},
		{"extra Write and NotebookEdit entries are harmless", func(pd []string) []string {
			return append(append([]string{}, pd...), "Write(//etc/claude-code/ballast/**)", "NotebookEdit(~/.ballast/**)")
		}, nil},
		{"single-slash Edit forms do not cover", func(pd []string) []string {
			out := make([]string, len(pd))
			for i, s := range pd {
				out[i] = strings.Replace(s, "Edit(//", "Edit(/", 1)
			}
			return out
		}, []string{model.TierMissingPermDeny}},
		{"Write forms cannot replace Edit forms", func(pd []string) []string {
			out := make([]string, len(pd))
			for i, s := range pd {
				out[i] = strings.Replace(s, "Edit(", "Write(", 1)
			}
			return out
		}, []string{model.TierMissingPermDeny}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, opt := baseline(t)
			doc := settingsDoc(tSelf, baseExtra)
			doc["permissions"] = map[string]any{"deny": toAny(c.deny(pd))}
			m.writeFile(tMS, mustJSON(t, doc), 0, 0o644)
			got := Detect(opt)
			if !reflect.DeepEqual(got.Missing, c.want) {
				t.Errorf("missing = %v, want %v", got.Missing, c.want)
			}
		})
	}
}

// A user-layer settings file that is a symlink to a non-regular file (FIFO,
// device) must not be read: Detect answers lower_sandbox_weakened instead of
// hanging on it. The same goes for an oversized file.
func TestReadSettingsRefusesNonRegularAndOversized(t *testing.T) {
	m := newMemFS()
	m.mkdirAll("/d", 0, 0o755)
	m.writeFile("/d/ok.json", `{}`, 0, 0o644)
	m.writeFile("/d/big.json", strings.Repeat("x", maxSettingsBytes+1), 0, 0o644)
	m.writeFile("/d/fifo.json", "", 0, 0o644)
	m.nodes["/d/fifo.json"].mode |= fs.ModeNamedPipe
	m.symlink("/d/link-fifo.json", "/d/fifo.json", 0)
	if b, err := readSettings(m, "/d/ok.json"); err != nil || string(b) != "{}" {
		t.Fatalf("ok: %q %v", b, err)
	}
	for _, name := range []string{"big.json", "fifo.json", "link-fifo.json"} {
		if _, err := readSettings(m, "/d/"+name); err == nil {
			t.Errorf("%s was read", name)
		}
	}
}
