package tier

import (
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

func lockFS(t testing.TB, ops ...tableOp) *memFS {
	t.Helper()
	m, _ := baseline(t)
	applyOps(t, m, ops)
	return m
}

func TestLockedTable(t *testing.T) {
	op := func(o, p string, extra ...func(*tableOp)) tableOp {
		x := tableOp{Op: o, Path: p}
		for _, f := range extra {
			f(&x)
		}
		return x
	}
	mode := func(m string) func(*tableOp) { return func(o *tableOp) { o.Mode = m } }
	uid := func(u uint32) func(*tableOp) { return func(o *tableOp) { o.UID = &u } }
	content := func(c string) func(*tableOp) { return func(o *tableOp) { o.Content = c } }
	const org = tManaged + "/ballast/org.json"
	cases := []struct {
		name string
		ops  []tableOp
		goos string
		want bool
	}{
		{"valid-t1-org", nil, "linux", true},
		{"absent", []tableOp{op("remove", org)}, "linux", false},
		{"owned-by-user", []tableOp{op("chown", org, uid(tUser))}, "linux", false},
		{"group-writable", []tableOp{op("chmod", org, mode("0664"))}, "linux", false},
		{"other-writable", []tableOp{op("chmod", org, mode("0646"))}, "linux", false},
		{"tier-t0", []tableOp{op("write", org, content(`{"v":1,"tier":"T0"}`))}, "linux", false},
		{"tier-absent", []tableOp{op("write", org, content(`{"v":1}`))}, "linux", false},
		{"invalid-version", []tableOp{op("write", org, content(`{"v":9,"tier":"T1"}`))}, "linux", false},
		{"invalid-enum", []tableOp{op("write", org, content(`{"v":1,"tier":"T1","runner_network":"wide-open"}`))}, "linux", false},
		{"invalid-json", []tableOp{op("write", org, content(`{`))}, "linux", false},
		{"empty-file", []tableOp{op("write", org, content(``))}, "linux", false},
		{"group-writable-parent-managed-dir", []tableOp{op("chmod", tManaged, mode("0775"))}, "linux", false},
		{"group-writable-parent-ballast-dir", []tableOp{op("chmod", tManaged+"/ballast", mode("0775"))}, "linux", false},
		{"group-writable-grandparent", []tableOp{op("chmod", "/etc", mode("0775"))}, "linux", false},
		{"parent-owned-by-user", []tableOp{op("chown", tManaged+"/ballast", uid(tUser))}, "linux", false},
		{"is-a-directory", []tableOp{op("remove", org), op("mkdir", org)}, "linux", false},
		{"windows-never-locked", nil, "windows", false},
		{"unknown-goos", nil, "plan9", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := lockFS(t, tc.ops...)
			if got := Locked(m, tc.goos, memOwner); got != tc.want {
				t.Errorf("Locked = %v, want %v", got, tc.want)
			}
			// LockState agrees with Locked, and returns the org config only when locked.
			o, ok := LockState(m, tc.goos, memOwner)
			if ok != tc.want {
				t.Errorf("LockState ok = %v, want %v", ok, tc.want)
			}
			if ok && o.Tier != model.TierT1 {
				t.Errorf("LockState org tier = %q", o.Tier)
			}
			if !ok && !reflect.DeepEqual(o, OrgConfig{}) {
				t.Errorf("LockState returned a non-zero OrgConfig %+v when not locked", o)
			}
		})
	}
}

func TestLockStateReturnsOrg(t *testing.T) {
	m := lockFS(t, tableOp{Op: "write", Path: tManaged + "/ballast/org.json", Content: `{"v":1,"tier":"T1","home_root":["/home","/srv/h"],"weakened_project":"audit"}`})
	org, ok := LockState(m, "linux", memOwner)
	if !ok {
		t.Fatal("not locked")
	}
	if !reflect.DeepEqual(org.HomeRoot, []string{"/home", "/srv/h"}) || org.WeakenedProject != model.WeakenedProjectAudit {
		t.Errorf("org = %+v", org)
	}
}

func TestLockedAbsentCostsOneLstat(t *testing.T) {
	m := newMemFS()
	var ops []string
	m.hook = func(op, name string) { ops = append(ops, op) }
	if Locked(m, "linux", memOwner) {
		t.Fatal("locked on an empty FS")
	}
	if !reflect.DeepEqual(ops, []string{"Lstat"}) {
		t.Errorf("I/O when org.json is absent = %v, want exactly one Lstat", ops)
	}
}

func TestLockedIgnoresLayoutManagedDir(t *testing.T) {
	// A planted org.json at some other directory must not lock: Locked only
	// looks at the compiled-in managed dir.
	m := newMemFS()
	m.mkdirAll("/tmp/evil/ballast", 0, 0o755)
	m.writeFile("/tmp/evil/ballast/org.json", `{"v":1,"tier":"T1"}`, 0, 0o644)
	if Locked(m, "linux", memOwner) {
		t.Fatal("locked by an org.json outside the compiled-in managed dir")
	}
}

func TestLockedDefaultOwnerOnRealFS(t *testing.T) {
	// Real FS, real ownership: a normal test machine has no root-owned
	// org.json in the managed dir, so the host is not locked. (If it is,
	// the test machine really is a locked host and the result is true.)
	got := Locked(sys.OSFS{}, "linux", nil)
	_, err := (sys.OSFS{}).Lstat("/etc/claude-code/ballast/org.json")
	if err != nil && got {
		t.Errorf("Locked = true although org.json is absent: %v", err)
	}
}

// --- LockedHome ----------------------------------------------------------

type lookupSpec struct {
	Kind    string `json:"kind"`
	Home    string `json:"home"`
	Msg     string `json:"msg"`
	DelayMS int    `json:"delay_ms"`
}

type lockedHomeRow struct {
	Name      string     `json:"name"`
	Euid      int        `json:"euid"`
	Home      string     `json:"home"`
	HomeRoot  []string   `json:"home_root"`
	Lookup    lookupSpec `json:"lookup"`
	Ops       []tableOp  `json:"ops"`
	TimeoutMS int        `json:"timeout_ms"`
	WantHome  string     `json:"want_home"`
	WantSrc   string     `json:"want_source"`
	Err       string     `json:"err"`
}

func TestLockedHomeTable(t *testing.T) {
	for _, r := range loadRows[lockedHomeRow](t, "locked_home.json") {
		t.Run(r.Name, func(t *testing.T) {
			m := newMemFS()
			m.mkdirAll("/home", 0, 0o755)
			m.mkdirAll("/home/u", tUser, 0o755)
			applyOps(t, m, r.Ops)
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			var lookupCalls, wrongUID atomic.Int32 // the lookup may outlive the call (timeout rows)
			lookup := func(uid int) (string, error) {
				lookupCalls.Add(1)
				if uid != r.Euid {
					wrongUID.Add(1)
				}
				switch r.Lookup.Kind {
				case "ok":
					if r.Lookup.DelayMS > 0 {
						time.Sleep(time.Duration(r.Lookup.DelayMS) * time.Millisecond)
					}
					return r.Lookup.Home, nil
				case "noentry":
					return "", sys.ErrNoPasswdEntry
				case "error":
					return "", errors.New(r.Lookup.Msg)
				case "block":
					<-release
					return "", nil
				}
				return "", errors.New("unknown lookup kind " + r.Lookup.Kind)
			}
			env := sys.MapEnv{OS: "linux", Vars: map[string]string{}}
			if r.Home != "" {
				env.Vars["HOME"] = subst.Replace(r.Home)
			}
			timeout := time.Duration(r.TimeoutMS) * time.Millisecond
			opt := HomeOptions{FS: m, Env: env, Owner: memOwner, Euid: r.Euid, Lookup: lookup, HomeRoot: r.HomeRoot, Timeout: timeout}
			start := time.Now()
			home, src, err := LockedHome(opt)
			elapsed := time.Since(start)
			switch {
			case r.Err == "":
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if home != r.WantHome || src != r.WantSrc {
					t.Errorf("got (%q, %q), want (%q, %q)", home, src, r.WantHome, r.WantSrc)
				}
			case r.Err == "timeout":
				if !errors.Is(err, ErrLockedHomeTimeout) {
					t.Fatalf("err = %v, want ErrLockedHomeTimeout", err)
				}
				limit := timeout
				if limit == 0 {
					limit = DefaultLockedHomeTimeout
				}
				if elapsed > limit+5*time.Millisecond+50*time.Millisecond {
					// 50 ms of scheduler slack on top of the contractual 5 ms
					// keeps loaded CI machines from flaking; the tight bound
					// is asserted in TestLockedHomeTimeoutIsTight.
					t.Errorf("returned after %v, want about %v", elapsed, limit)
				}
				if elapsed < limit {
					t.Errorf("returned after %v, before the %v timeout", elapsed, limit)
				}
				if home != "" || src != "" {
					t.Errorf("non-empty result on timeout: %q %q", home, src)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), r.Err) {
					t.Fatalf("err = %v, want it to contain %q", err, r.Err)
				}
				if home != "" || src != "" {
					t.Errorf("non-empty result on error: %q %q", home, src)
				}
			}
			if n := lookupCalls.Load(); n != 1 {
				t.Errorf("Lookup called %d times, want 1", n)
			}
			if wrongUID.Load() != 0 {
				t.Errorf("Lookup called with a uid other than %d", r.Euid)
			}
		})
	}
}

func TestLockedHomeTimeoutIsTight(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var home string
	var err error
	best := time.Hour
	for i := 0; i < 5; i++ { // best of five: scheduler noise only ever adds
		start := time.Now()
		home, _, err = LockedHome(HomeOptions{
			FS: newMemFS(), Env: sys.MapEnv{}, Owner: memOwner, Euid: 1,
			HomeRoot: []string{"/home"}, Timeout: 20 * time.Millisecond,
			Lookup: func(int) (string, error) { <-release; return "", nil },
		})
		if d := time.Since(start); d < best {
			best = d
		}
	}
	if !errors.Is(err, ErrLockedHomeTimeout) || home != "" {
		t.Fatalf("got %q, %v", home, err)
	}
	if best > 25*time.Millisecond {
		t.Errorf("fastest of 5 timeouts took %v, want <= Timeout + 5ms", best)
	}
}

func TestLockedHomeTimeoutSkipsHomeRootRule(t *testing.T) {
	// A slow directory is not "no entry": the home_root rule must not be tried
	// (the FS must see no I/O at all).
	m := newMemFS()
	release := make(chan struct{})
	defer close(release)
	_, _, err := LockedHome(HomeOptions{
		FS: m, Env: sys.MapEnv{Vars: map[string]string{"HOME": "/home/u"}}, Owner: memOwner, Euid: 1000,
		HomeRoot: []string{"/home"}, Timeout: 10 * time.Millisecond,
		Lookup: func(int) (string, error) { <-release; return "", sys.ErrNoPasswdEntry },
	})
	if !errors.Is(err, ErrLockedHomeTimeout) {
		t.Fatalf("err = %v", err)
	}
	if n := m.calls.Load(); n != 0 {
		t.Errorf("home_root rule touched the FS %d times after a timeout", n)
	}
}

func TestLockedHomePasswdSuccessDoesNoFSIO(t *testing.T) {
	m := newMemFS()
	home, src, err := LockedHome(HomeOptions{FS: m, Env: sys.MapEnv{Vars: map[string]string{"HOME": "/other"}}, Euid: 5,
		Lookup: func(int) (string, error) { return "/home/five", nil }})
	if err != nil || home != "/home/five" || src != HomeFromPasswd {
		t.Fatalf("got %q %q %v", home, src, err)
	}
	if n := m.calls.Load(); n != 0 {
		t.Errorf("passwd path performed %d FS calls", n)
	}
}

func TestLockedHomeDefaultsToSysLookupHome(t *testing.T) {
	// Lookup nil ⇒ sys.LookupHome; use an impossible uid so no real data
	// matters, and so the answer is deterministic: no entry ⇒ error.
	_, _, err := LockedHome(HomeOptions{FS: newMemFS(), Env: sys.MapEnv{}, Euid: 2147483000})
	if err == nil {
		t.Fatal("expected an error for an unknown uid")
	}
}

func TestOwnerFuncUnknownIsNotRoot(t *testing.T) {
	m := lockFS(t)
	unknown := func(fs.FileInfo) (uint32, bool) { return 0, false }
	if Locked(m, "linux", unknown) {
		t.Fatal("unknown ownership must never count as root-owned")
	}
}
