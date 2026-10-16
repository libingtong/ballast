package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/hook"
	"github.com/libingtong/ballast/internal/install"
	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/report"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/tier"
)

// wireEnv is a sys.Env over a map (GOOS linux unless set).
type wireEnv struct {
	vars map[string]string
	home string
	goos string
}

func (e wireEnv) Getenv(k string) (string, bool) { v, ok := e.vars[k]; return v, ok }
func (e wireEnv) Environ() []string {
	var out []string
	for k, v := range e.vars {
		out = append(out, k+"="+v)
	}
	return out
}
func (e wireEnv) HomeDir() (string, error) {
	if e.home == "" {
		return "", errors.New("no home")
	}
	return e.home, nil
}
func (e wireEnv) GOOS() string {
	if e.goos == "" {
		return "linux"
	}
	return e.goos
}

// lockedFS is a fake host with a root-owned, locked-down managed directory
// holding org.json = orgJSON ("" ⇒ no org.json at all).
func lockedFS(orgJSON string) *wireFS {
	m := newWireFS()
	m.mkdirAll("/etc/claude-code/ballast", 0, 0o755)
	if orgJSON != "" {
		m.writeFile("/etc/claude-code/ballast/org.json", orgJSON, 0, 0o644)
	}
	return m
}

func agentEnv(vars map[string]string) wireEnv {
	return wireEnv{vars: vars, home: "/home/agent"}
}

func lookupCounter(home string, calls *atomic.Int64) func(uid int) (string, error) {
	return func(uid int) (string, error) {
		calls.Add(1)
		return home, nil
	}
}

// TestWireHookLocked: a locked host ignores the redirect variables, takes the
// home from passwd and wires every P2 dependency of hook.Deps.
func TestWireHookLocked(t *testing.T) {
	for _, tc := range []struct {
		name string
		org  string
		want model.WeakenedProjectPolicy
	}{
		{"default weakened_project", `{"v":1,"tier":"T1"}`, tier.DefaultWeakenedProject},
		{"explicit audit", `{"v":1,"tier":"T1","weakened_project":"audit"}`, model.WeakenedProjectAudit},
		{"explicit deny", `{"v":1,"tier":"T1","weakened_project":"deny"}`, model.WeakenedProjectDeny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			env := agentEnv(map[string]string{
				"BALLAST_HOME": "/evil/home", "BALLAST_INSTALL_ROOT": "/evil/root", "BALLAST_MANAGED_DIR": "",
				"CLAUDE_CONFIG_DIR": "/home/real/.claude",
			})
			deps, err := wireHook(env, wireSeams{
				FS: lockedFS(tc.org), Owner: wireOwner, Euid: func() int { return 1000 },
				Lookup: lookupCounter("/home/real", &calls),
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Errorf("passwd lookup called %d times, want 1", calls.Load())
			}
			if want := filepath.Join("/home/real", ".ballast"); deps.Layout.Home != want {
				t.Errorf("Layout.Home = %q, want %q (BALLAST_HOME must be ignored)", deps.Layout.Home, want)
			}
			if want := filepath.Join("/home/real"); deps.Layout.InstallRoot != want {
				t.Errorf("Layout.InstallRoot = %q, want %q", deps.Layout.InstallRoot, want)
			}
			// Only the variables that are set (non-empty), in the frozen order.
			if want := []string{"BALLAST_HOME", "BALLAST_INSTALL_ROOT"}; !reflect.DeepEqual(deps.RedirectsIgnored, want) {
				t.Errorf("RedirectsIgnored = %v, want %v", deps.RedirectsIgnored, want)
			}
			if deps.ProjectSandbox == nil {
				t.Error("ProjectSandbox must be wired on a locked host")
			}
			if deps.WeakenedProject != tc.want {
				t.Errorf("WeakenedProject = %q, want %q", deps.WeakenedProject, tc.want)
			}
			if deps.IDs == nil || deps.Tier == nil {
				t.Errorf("IDs=%v Tier=%v must always be wired", deps.IDs, deps.Tier)
			}
		})
	}
}

// TestWireHookLockedNoRedirectVars: locked but nothing set ⇒ RedirectsIgnored
// stays nil (the hook audits only when it is non-nil).
func TestWireHookLockedNoRedirectVars(t *testing.T) {
	var calls atomic.Int64
	deps, err := wireHook(agentEnv(map[string]string{}), wireSeams{
		FS: lockedFS(`{"v":1,"tier":"T1"}`), Owner: wireOwner, Euid: func() int { return 1000 },
		Lookup: lookupCounter("/home/real", &calls),
	})
	if err != nil {
		t.Fatal(err)
	}
	if deps.RedirectsIgnored != nil {
		t.Errorf("RedirectsIgnored = %#v, want nil", deps.RedirectsIgnored)
	}
	if deps.ProjectSandbox == nil || deps.WeakenedProject == "" {
		t.Error("a locked host still wires ProjectSandbox and WeakenedProject")
	}
}

// TestWireHookUnlocked: no (trusted) org.json ⇒ today's behaviour: redirects
// honoured, PasswdHome never called, RedirectsIgnored and ProjectSandbox nil,
// IDs always non-nil.
func TestWireHookUnlocked(t *testing.T) {
	for name, fsys := range map[string]*wireFS{
		"no org.json":        lockedFS(""),
		"org.json not root":  withOrgOwner(lockedFS(`{"v":1,"tier":"T1"}`), 1000),
		"org.json tier T0":   lockedFS(`{"v":1,"tier":"T0"}`),
		"org.json invalid":   lockedFS(`{"v":2,"tier":"T1"}`),
		"group-writable dir": groupWritable(lockedFS(`{"v":1,"tier":"T1"}`)),
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64
			env := agentEnv(map[string]string{"BALLAST_HOME": "/redirected/home", "BALLAST_INSTALL_ROOT": "/redirected/root"})
			deps, err := wireHook(env, wireSeams{
				FS: fsys, Owner: wireOwner, Euid: func() int { return 1000 },
				Lookup: lookupCounter("/home/real", &calls),
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 0 {
				t.Errorf("PasswdHome was called %d times on an unlocked host", calls.Load())
			}
			if deps.Layout.Home != "/redirected/home" {
				t.Errorf("Layout.Home = %q, want the redirected home", deps.Layout.Home)
			}
			if deps.RedirectsIgnored != nil || deps.ProjectSandbox != nil || deps.WeakenedProject != "" {
				t.Errorf("unlocked host wired lock-only fields: %v %v %q", deps.RedirectsIgnored, deps.ProjectSandbox, deps.WeakenedProject)
			}
			if deps.IDs == nil || deps.Tier == nil {
				t.Error("IDs and Tier are wired on every host")
			}
		})
	}
}

func withOrgOwner(m *wireFS, uid uint32) *wireFS {
	m.chown("/etc/claude-code/ballast/org.json", uid)
	return m
}

func groupWritable(m *wireFS) *wireFS {
	m.chmod("/etc/claude-code", 0o775)
	return m
}

// TestWireHookLockedHomeFailures: a locked host whose home cannot be
// determined is an error, never a fallback to $HOME.
func TestWireHookLockedHomeFailures(t *testing.T) {
	env := agentEnv(map[string]string{"BALLAST_HOME": "/evil"})
	locked := func() *wireFS { return lockedFS(`{"v":1,"tier":"T1"}`) }
	t.Run("lookup error", func(t *testing.T) {
		_, err := wireHook(env, wireSeams{FS: locked(), Owner: wireOwner, Euid: func() int { return 1 },
			Lookup: func(int) (string, error) { return "", errors.New("nss exploded") }})
		if err == nil {
			t.Fatal("want an error")
		}
	})
	t.Run("no passwd entry and no home_root", func(t *testing.T) {
		_, err := wireHook(env, wireSeams{FS: locked(), Owner: wireOwner, Euid: func() int { return 1 },
			Lookup: func(int) (string, error) { return "", sys.ErrNoPasswdEntry }})
		if err == nil {
			t.Fatal("want an error")
		}
	})
}

// TestDoctorDiagnosticFallback: doctor must still run on a locked host whose
// home cannot be determined (so it can report tier.locked_home as fail): with
// the Diagnostic seam the layout falls back to $HOME, redirects still ignored.
func TestDoctorDiagnosticFallback(t *testing.T) {
	env := agentEnv(map[string]string{"BALLAST_HOME": "/evil"})
	seams := wireSeams{FS: lockedFS(`{"v":1,"tier":"T1"}`), Owner: wireOwner, Euid: func() int { return 1 },
		Lookup: func(int) (string, error) { return "", errors.New("nss exploded") }}
	if _, err := newCoreWith(env, seams); err == nil {
		t.Fatal("a non-diagnostic core must fail")
	}
	seams.Diagnostic = true
	c, err := newCoreWith(env, seams)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/agent", ".ballast"); c.lay.Home != want {
		t.Errorf("fallback Layout.Home = %q, want %q (redirects stay ignored, $HOME is the fallback)", c.lay.Home, want)
	}
	if !c.locked {
		t.Error("the host is still locked")
	}
}

// TestWireHookPasswdBlocks: a passwd lookup that never returns makes WireHook
// fail within the lookup time limit, and the hook then exits 1 with an empty
// stdout (B7), without touching anything.
func TestWireHookPasswdBlocks(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	seams := wireSeams{
		FS: lockedFS(`{"v":1,"tier":"T1"}`), Owner: wireOwner, Euid: func() int { return 1000 },
		Lookup:  func(int) (string, error) { <-block; return "/home/real", nil },
		Timeout: 25 * time.Millisecond,
	}
	env := agentEnv(map[string]string{})

	start := time.Now()
	_, err := wireHook(env, seams)
	if !errors.Is(err, tier.ErrLockedHomeTimeout) {
		t.Fatalf("err = %v, want tier.ErrLockedHomeTimeout", err)
	}
	if d := time.Since(start); d > 25*time.Millisecond+250*time.Millisecond {
		t.Errorf("WireHook took %v, want about the 25ms limit", d)
	}

	var stdout, stderr bytes.Buffer
	start = time.Now()
	code := runHookWith([]string{"--client", "claude-code", "--event", "PreToolUse"},
		strings.NewReader(`{"session_id":"s","hook_event_name":"PreToolUse"}`), &stdout, &stderr,
		func(sys.Env) (*hook.Deps, error) { return wireHook(env, seams) })
	if code != ExitHookError || stdout.Len() != 0 {
		t.Errorf("exit %d stdout %q, want exit 1 and empty stdout", code, stdout.String())
	}
	if !strings.HasPrefix(stderr.String(), "ballast: hook:") || !strings.Contains(stderr.String(), "timed out") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if d := time.Since(start); d > 25*time.Millisecond+250*time.Millisecond {
		t.Errorf("hook took %v", d)
	}
}

// TestLockedHomeRootRule: the home_root rule is wired from org.json.
func TestLockedHomeRootRule(t *testing.T) {
	m := lockedFS(`{"v":1,"tier":"T1","home_root":["/home"]}`)
	m.mkdirAll("/home", 0, 0o755)
	m.mkdirAll("/home/ghost", 4242, 0o755)
	env := wireEnv{vars: map[string]string{}, home: "/home/ghost"}
	deps, err := wireHook(env, wireSeams{FS: m, Owner: wireOwner, Euid: func() int { return 4242 },
		Lookup: func(int) (string, error) { return "", sys.ErrNoPasswdEntry }})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/ghost", ".ballast"); deps.Layout.Home != want {
		t.Errorf("Layout.Home = %q, want %q", deps.Layout.Home, want)
	}
}

// TestDefaultWeakenedProjectMatchesM0b: tier.DefaultWeakenedProject follows
// the conditional contract (docs/p2-design.md §3.2): "audit" only when the
// credentialed M0b case managed-denywrite-anydepth passed, otherwise "deny",
// and `org render` prints exactly that value in its org.json section.
func TestDefaultWeakenedProjectMatchesM0b(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "facts", "m0b-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		CaseID string `json:"case_id"`
		Result string `json:"result"`
		Mode   string `json:"mode"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	var result, mode string
	for _, c := range cases {
		if c.CaseID == "managed-denywrite-anydepth" {
			result, mode = c.Result, c.Mode
		}
	}
	if result == "" {
		t.Fatal("m0b-cases.json has no managed-denywrite-anydepth row")
	}
	want := model.WeakenedProjectDeny
	if result == "pass" && mode == "credentials" {
		want = model.WeakenedProjectAudit
	}
	if tier.DefaultWeakenedProject != want {
		t.Errorf("tier.DefaultWeakenedProject = %q but managed-denywrite-anydepth is %q (mode %q): want %q",
			tier.DefaultWeakenedProject, result, mode, want)
	}
	if got := tier.EffectiveWeakenedProject(tier.OrgConfig{}); got != want {
		t.Errorf("EffectiveWeakenedProject(zero) = %q, want %q", got, want)
	}

	rendered, err := install.OrgRenderFull(layout.Layout{GOOS: "linux"}, sys.OSFS{}, install.OrgRenderOptions{
		GOOS: "linux", Binary: "/usr/local/libexec/ballast/ballast",
	})
	if err != nil {
		t.Fatal(err)
	}
	var orgDoc struct {
		WeakenedProject string `json:"weakened_project"`
	}
	if err := json.Unmarshal(rendered.OrgJSON, &orgDoc); err != nil {
		t.Fatal(err)
	}
	if orgDoc.WeakenedProject != string(want) {
		t.Errorf("org render weakened_project = %q, want %q", orgDoc.WeakenedProject, want)
	}
}

// TestShippedRuleIDsMatchPacks keeps the report's verbatim rule-id list
// identical to rules/default.json and rules/optional/*.json.
func TestShippedRuleIDsMatchPacks(t *testing.T) {
	root := filepath.Join("..", "..", "rules")
	files, err := filepath.Glob(filepath.Join(root, "optional", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, filepath.Join(root, "default.json"))
	got := map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var pack struct {
			Rules []struct {
				ID string `json:"id"`
			} `json:"rules"`
		}
		if err := json.Unmarshal(data, &pack); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, r := range pack.Rules {
			got[r.ID] = true
		}
	}
	have := map[string]bool{}
	for _, id := range shippedRuleIDs {
		if have[id] {
			t.Errorf("shippedRuleIDs lists %q twice", id)
		}
		have[id] = true
	}
	for id := range got {
		if !have[id] {
			t.Errorf("rule %q ships in rules/ but is missing from shippedRuleIDs", id)
		}
	}
	for id := range have {
		if !got[id] {
			t.Errorf("shippedRuleIDs lists %q which no shipped pack defines", id)
		}
	}
}

// TestReportWriteExit: the exit codes of `ballast report` that the end-to-end
// test cannot reach (the privacy self-check failing is a bug, never an input).
func TestReportWriteExit(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"privacy":         {&report.PrivacyError{Violations: []string{"x"}}, ExitFindings},
		"wrapped privacy": {fmt.Errorf("w: %w", &report.PrivacyError{}), ExitFindings},
		"exists":          {fmt.Errorf("%w: f", report.ErrExists), ExitRefused},
		"io":              {errors.New("disk full"), ExitInfra},
	} {
		var errw bytes.Buffer
		a := &app{errw: &errw}
		if got := a.reportWriteExit(tc.err); got != tc.want {
			t.Errorf("%s: exit %d, want %d", name, got, tc.want)
		}
		if !strings.HasPrefix(errw.String(), "ballast: report:") {
			t.Errorf("%s: stderr %q", name, errw.String())
		}
	}
}

var _ fs.FileInfo = wireInfo{}
