package tier

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

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
