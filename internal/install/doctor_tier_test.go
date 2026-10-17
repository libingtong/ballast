package install

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/tier"
)

// ---- fixtures -------------------------------------------------------------

// machoFixture builds a minimal thin arm64 Mach-O whose only load command is
// LC_CODE_SIGNATURE pointing at an embedded SuperBlob with one CodeDirectory
// carrying flags. signed=false omits the load command.
func machoFixture(signed bool, flags uint32) []byte {
	le, be := binary.LittleEndian, binary.BigEndian
	cd := make([]byte, 44)
	be.PutUint32(cd[0:], csMagicCodeDir)
	be.PutUint32(cd[4:], uint32(len(cd)))
	be.PutUint32(cd[8:], 0x20400)
	be.PutUint32(cd[12:], flags)
	blob := make([]byte, 20, 20+len(cd))
	be.PutUint32(blob[0:], csMagicEmbedded)
	be.PutUint32(blob[4:], uint32(20+len(cd)))
	be.PutUint32(blob[8:], 1)
	be.PutUint32(blob[12:], csSlotCodeDir)
	be.PutUint32(blob[16:], 20)
	blob = append(blob, cd...)

	ncmds, sizeof := uint32(0), uint32(0)
	if signed {
		ncmds, sizeof = 1, 16
	}
	h := make([]byte, 32)
	le.PutUint32(h[0:], 0xfeedfacf)
	le.PutUint32(h[4:], 0x0100000c)
	le.PutUint32(h[12:], 2)
	le.PutUint32(h[16:], ncmds)
	le.PutUint32(h[20:], sizeof)
	out := h
	if signed {
		lc := make([]byte, 16)
		le.PutUint32(lc[0:], lcCodeSignature)
		le.PutUint32(lc[4:], 16)
		le.PutUint32(lc[8:], 48)
		le.PutUint32(lc[12:], uint32(len(blob)))
		out = append(out, lc...)
		out = append(out, blob...)
	}
	return out
}

func universalFixture(slices ...[]byte) []byte {
	be := binary.BigEndian
	h := make([]byte, 8+20*len(slices))
	be.PutUint32(h[0:], 0xcafebabe)
	be.PutUint32(h[4:], uint32(len(slices)))
	off := len(h)
	var body []byte
	for i, s := range slices {
		e := h[8+20*i:]
		be.PutUint32(e[0:], []uint32{0x0100000c, 0x01000007, 0x0000000c}[i%3]) // distinct CPUs, or Go rejects the duplicate
		be.PutUint32(e[8:], uint32(off+len(body)))
		be.PutUint32(e[12:], uint32(len(s)))
		body = append(body, s...)
	}
	return append(h, body...)
}

// elfFixture builds a minimal ELF64 x86-64 executable with a PT_LOAD and,
// when dynamic, a PT_INTERP program header.
func elfFixture(dynamic bool) []byte {
	le := binary.LittleEndian
	n := 1
	if dynamic {
		n = 2
	}
	h := make([]byte, 64+56*n)
	copy(h, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	le.PutUint16(h[16:], 2)
	le.PutUint16(h[18:], 0x3e)
	le.PutUint32(h[20:], 1)
	le.PutUint64(h[32:], 64)
	le.PutUint16(h[52:], 64)
	le.PutUint16(h[54:], 56)
	le.PutUint16(h[56:], uint16(n))
	le.PutUint16(h[58:], 64)
	ph := func(i int, typ uint32, off, size uint64) {
		p := h[64+56*i:]
		le.PutUint32(p[0:], typ)
		le.PutUint32(p[4:], 5)
		le.PutUint64(p[8:], off)
		le.PutUint64(p[32:], size)
		le.PutUint64(p[40:], size)
	}
	ph(0, 1, 0, uint64(len(h)))
	if !dynamic {
		return h
	}
	interp := []byte("/lib64/ld-linux-x86-64.so.2\x00")
	ph(1, 3, uint64(len(h)), uint64(len(interp)))
	return append(h, interp...)
}

func TestMachoRuntime(t *testing.T) {
	for _, c := range []struct {
		name string
		data []byte
		want machoState
	}{
		{"hardened runtime", machoFixture(true, csRuntime|0x2), machoHardened},
		{"signed, no runtime flag", machoFixture(true, 0x2), machoSignedNoRuntime},
		{"adhoc flag only", machoFixture(true, 0x2|0x20000), machoSignedNoRuntime},
		{"unsigned", machoFixture(false, 0), machoUnsigned},
		{"universal, all hardened", universalFixture(machoFixture(true, csRuntime), machoFixture(true, csRuntime)), machoHardened},
		{"universal, one slice without runtime", universalFixture(machoFixture(true, csRuntime), machoFixture(true, 0)), machoSignedNoRuntime},
		{"universal, one slice unsigned", universalFixture(machoFixture(true, csRuntime), machoFixture(false, 0)), machoUnsigned},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := machoRuntime(c.data)
			if err != nil || got != c.want {
				t.Errorf("machoRuntime = %v, %v; want %v", got, err, c.want)
			}
		})
	}
	for name, data := range map[string][]byte{
		"shell script": []byte("#!/bin/sh\n"),
		"empty":        nil,
		"truncated":    machoFixture(true, csRuntime)[:50],
	} {
		if _, err := machoRuntime(data); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestElfDynamic(t *testing.T) {
	for _, dynamic := range []bool{false, true} {
		got, err := elfDynamic(elfFixture(dynamic))
		if err != nil || got != dynamic {
			t.Errorf("elfDynamic(dynamic=%v) = %v, %v", dynamic, got, err)
		}
	}
	if _, err := elfDynamic([]byte("#!/bin/sh\n")); err == nil {
		t.Error("a script is not an ELF")
	}
}

// ---- doctor wiring --------------------------------------------------------

// tierOpt is a doctor configuration for the tier.* checks that never reads
// the real managed directory.
func (e *tenv) tierOpt(goos string) DoctorOptions {
	lay := e.lay
	lay.GOOS = goos
	return DoctorOptions{
		Layout: lay, FS: e.fs, Env: sys.MapEnv{Vars: map[string]string{"HOME": e.sb.Home}},
		TierManagedDir: e.lay.ManagedDir,
		TierLock:       func() (tier.OrgConfig, bool) { return tier.OrgConfig{}, false },
	}
}

func TestDoctorTierDeclaredMismatch(t *testing.T) {
	e := newTenv(t)
	for _, c := range []struct {
		name string
		info model.TierInfo
		want CheckStatus
		msg  string
	}{
		{"declared T1, detected T0", model.TierInfo{Tier: model.TierT0, Declared: model.TierT1, Missing: []string{"managed_hooks", "sandbox"}}, CheckFail, "managed_hooks, sandbox"},
		{"declared T1, detected T1", model.TierInfo{Tier: model.TierT1, Declared: model.TierT1}, CheckOK, "一致"},
		{"nothing declared", model.TierInfo{Tier: model.TierT0}, CheckOK, "没有可信的 org.json 声明"},
		{"declared T0", model.TierInfo{Tier: model.TierT0, Declared: model.TierT0}, CheckOK, "T0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			opt := e.tierOpt("linux")
			opt.Tier = fakeProbe{c.info}
			got := byID(Doctor(opt))["tier.declared_mismatch"]
			if got.Status != c.want || !strings.Contains(got.Message, c.msg) {
				t.Errorf("%+v", got)
			}
		})
	}
}

func TestDoctorTierRunnerSandbox(t *testing.T) {
	e := newTenv(t)
	boom := errors.New("bwrap: no permission to create a user namespace")
	for _, c := range []struct {
		name  string
		tier  model.Tier
		avail func() error
		want  CheckStatus
		msg   string
	}{
		{"T1 available", model.TierT1, func() error { return nil }, CheckOK, "可用"},
		{"T1 unavailable", model.TierT1, func() error { return boom }, CheckFail, "全部记为 infra"},
		{"T1 not probed", model.TierT1, nil, CheckWarn, "not evaluated"},
		{"T0 unavailable", model.TierT0, func() error { return boom }, CheckOK, "不是必需的"},
		{"T0 not probed", model.TierT0, nil, CheckOK, "不是必需的"},
	} {
		t.Run(c.name, func(t *testing.T) {
			opt := e.tierOpt("linux")
			opt.Tier = fakeProbe{model.TierInfo{Tier: c.tier}}
			opt.SandboxAvailable = c.avail
			got := byID(Doctor(opt))["tier.runner_sandbox"]
			if got.Status != c.want || !strings.Contains(got.Message, c.msg) {
				t.Errorf("%+v", got)
			}
			if c.want == CheckFail && got.Fix == "" {
				t.Error("a failing check needs a fix")
			}
		})
	}
}

func TestDoctorTierRedirectIgnored(t *testing.T) {
	e := newTenv(t)
	run := func(locked bool, vars map[string]string) Check {
		opt := e.tierOpt("linux")
		opt.Env = sys.MapEnv{Vars: vars}
		opt.TierLock = func() (tier.OrgConfig, bool) { return tier.OrgConfig{}, locked }
		return byID(Doctor(opt))["tier.redirect_ignored"]
	}
	c := run(true, map[string]string{"BALLAST_HOME": "/x/secret-value", "BALLAST_INSTALL_ROOT": "/y", "HOME": "/h"})
	if c.Status != CheckWarn || !strings.Contains(c.Message, "BALLAST_HOME, BALLAST_INSTALL_ROOT") || strings.Contains(c.Message, "secret-value") {
		t.Errorf("locked + vars: %+v (names only, in the frozen order)", c)
	}
	if c := run(true, map[string]string{"BALLAST_HOME": ""}); c.Status != CheckOK {
		t.Errorf("locked, no vars: %+v", c)
	}
	if c := run(false, map[string]string{"BALLAST_MANAGED_DIR": "/m"}); c.Status != CheckOK || !strings.Contains(c.Message, "BALLAST_MANAGED_DIR") {
		t.Errorf("unlocked + vars: %+v", c)
	}
}

// renderChecks prints id, status, message and fix for golden comparison with
// the temp dir normalised.
func renderChecks(cs []Check, root string, ids ...string) string {
	by := byID(cs)
	var b strings.Builder
	for _, id := range ids {
		c, ok := by[id]
		if !ok {
			fmt.Fprintf(&b, "%s: MISSING\n", id)
			continue
		}
		fmt.Fprintf(&b, "%s [%s]\n  %s\n", c.ID, c.Status, c.Message)
		if c.Fix != "" {
			fmt.Fprintf(&b, "  fix: %s\n", c.Fix)
		}
	}
	out := strings.ReplaceAll(b.String(), root, "<tmp>")
	return strings.ReplaceAll(out, fmt.Sprintf("uid %d", sys.Euid()), "uid <euid>")
}

func lockedWith(homeRoot ...string) func() (tier.OrgConfig, bool) {
	return func() (tier.OrgConfig, bool) {
		return tier.OrgConfig{V: 1, Tier: model.TierT1, HomeRoot: homeRoot}, true
	}
}

func TestDoctorTierLockedHome(t *testing.T) {
	defer func(f func() bool) { cgoEnabled = f }(cgoEnabled)
	e := newTenv(t)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })

	cases := []struct {
		name   string
		goos   string
		cgo    bool
		lookup func(int) (string, error)
		root   []string
		status CheckStatus
	}{
		{"linux static passwd ok", "linux", false, func(int) (string, error) { return "/home/u", nil }, nil, CheckOK},
		{"linux static no entry no home_root", "linux", false, func(int) (string, error) { return "", sys.ErrNoPasswdEntry }, nil, CheckFail},
		{"linux cgo passwd ok", "linux", true, func(int) (string, error) { return "/home/u", nil }, nil, CheckOK},
		{"linux cgo no entry", "linux", true, func(int) (string, error) { return "", sys.ErrNoPasswdEntry }, nil, CheckFail},
		{"linux lookup error", "linux", false, func(int) (string, error) { return "", errors.New("nss: backend unavailable") }, []string{"/home"}, CheckFail},
		{"linux blocked lookup", "linux", false, func(int) (string, error) { <-release; return "", nil }, []string{"/home"}, CheckFail},
		{"darwin fast lookup", "darwin", false, func(int) (string, error) { return "/Users/u", nil }, nil, CheckOK},
		{"darwin slow lookup", "darwin", false, func(int) (string, error) { time.Sleep(10 * time.Millisecond); return "/Users/u", nil }, nil, CheckOK},
		{"darwin blocked lookup", "darwin", false, func(int) (string, error) { <-release; return "", nil }, []string{"/Users"}, CheckFail},
	}
	var golden strings.Builder
	for _, c := range cases {
		cgoEnabled = func() bool { return c.cgo }
		opt := e.tierOpt(c.goos)
		opt.Tier = fakeProbe{model.TierInfo{Tier: model.TierT1, Declared: model.TierT1, Locked: true}}
		opt.TierLock = lockedWith(c.root...)
		opt.HomeLookup = c.lookup
		opt.HomeTimeout = 40 * time.Millisecond
		start := time.Now()
		got := byID(Doctor(opt))["tier.locked_home"]
		if got.Status != c.status {
			t.Errorf("%s: %+v", c.name, got)
		}
		if strings.Contains(c.name, "blocked") {
			if !strings.Contains(got.Message, "时限") || !strings.Contains(got.Message, "超时而放行") || !strings.Contains(got.Message, "40ms") {
				t.Errorf("%s: timeout not explained: %+v", c.name, got)
			}
			// the abandoned lookup measured nothing, but a timeout is far
			// above the slow-lookup threshold: darwin gets the note.
			if hasNote := strings.Contains(got.Message, "opendirectoryd"); hasNote != (c.goos == "darwin") {
				t.Errorf("%s: directory-account note present=%v on %s: %+v", c.name, hasNote, c.goos, got)
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Errorf("%s: doctor blocked for %v", c.name, d)
			}
		}
		if got.Status == CheckFail && !strings.Contains(got.Fix, "--home-root /home") {
			t.Errorf("%s: fix lacks the home_root hint: %q", c.name, got.Fix)
		}
		fmt.Fprintf(&golden, "== %s\n%s", c.name, renderChecks([]Check{got}, e.sb.Root, "tier.locked_home"))
	}
	once.Do(func() { close(release) })
	checkGolden(t, "doctor_locked_home", golden.String())
}

// The parent of the real $HOME is not a home_root entry (rule b). The
// sandbox $HOME is a POSIX path only on unix hosts (on Windows it is a
// drive path and the passwd-less rule (a) applies), so this case is unix-only.
func TestDoctorTierLockedHomeRootMismatch(t *testing.T) {
	if !runtimeIsUnix() {
		t.Skip("$HOME is a POSIX path only on unix hosts")
	}
	defer func(f func() bool) { cgoEnabled = f }(cgoEnabled)
	cgoEnabled = func() bool { return false }
	e := newTenv(t)
	opt := e.tierOpt("linux")
	opt.Tier = fakeProbe{model.TierInfo{Tier: model.TierT1, Declared: model.TierT1, Locked: true}}
	opt.TierLock = lockedWith("/nonexistent-root")
	opt.HomeLookup = func(int) (string, error) { return "", sys.ErrNoPasswdEntry }
	opt.HomeTimeout = 40 * time.Millisecond
	got := byID(Doctor(opt))["tier.locked_home"]
	if got.Status != CheckFail || !strings.Contains(got.Fix, "--home-root /home") {
		t.Errorf("%+v", got)
	}
	checkGolden(t, "doctor_locked_home_mismatch", renderChecks([]Check{got}, e.sb.Root, "tier.locked_home"))
}

func TestDoctorTierLockedHomeNotesAreScoped(t *testing.T) {
	defer func(f func() bool) { cgoEnabled = f }(cgoEnabled)
	e := newTenv(t)
	noEntry := func(int) (string, error) { return "", sys.ErrNoPasswdEntry }
	get := func(goos string, cgo bool, lookup func(int) (string, error)) string {
		cgoEnabled = func() bool { return cgo }
		opt := e.tierOpt(goos)
		opt.TierLock = lockedWith()
		opt.HomeLookup = lookup
		return byID(Doctor(opt))["tier.locked_home"].Message
	}
	if m := get("linux", false, noEntry); !strings.Contains(m, "本构建为静态构建,只读 /etc/passwd") || strings.Contains(m, "NSS") {
		t.Errorf("static: %s", m)
	}
	for _, lookup := range []func(int) (string, error){noEntry, func(int) (string, error) { return "/home/u", nil }} {
		m := get("linux", true, lookup)
		for _, want := range []string{"NSS", "sssd 本地缓存或 nscd", "nss_ldap", "推荐改用 home_root"} {
			if !strings.Contains(m, want) {
				t.Errorf("cgo note lacks %q: %s", want, m)
			}
		}
	}
	slow := func(int) (string, error) { time.Sleep(10 * time.Millisecond); return "/Users/u", nil }
	fast := func(int) (string, error) { return "/Users/u", nil }
	if m := get("darwin", false, slow); !strings.Contains(m, "opendirectoryd") || !strings.Contains(m, "请启用移动账户或目录服务本地缓存") {
		t.Errorf("darwin slow: %s", m)
	}
	if m := get("darwin", false, fast); strings.Contains(m, "opendirectoryd") {
		t.Errorf("darwin fast must carry no note: %s", m)
	}
	if m := get("linux", false, slow); strings.Contains(m, "opendirectoryd") {
		t.Errorf("the darwin note is darwin-only: %s", m)
	}
	// not locked: not applicable, and the lookup is never made
	opt := e.tierOpt("linux")
	opt.HomeLookup = func(int) (string, error) { t.Fatal("lookup on an unlocked host"); return "", nil }
	if c := byID(Doctor(opt))["tier.locked_home"]; c.Status != CheckOK || !strings.Contains(c.Message, "未锁定") {
		t.Errorf("unlocked: %+v", c)
	}
}

// lstatFS makes every directory look root-owned-and-private-to-root for the
// home_root rule: group/other-writable bits are cleared in Lstat results
// (the temp dir sits below a sticky world-writable /tmp on Linux).
type lstatFS struct{ sys.FS }

type cleanInfo struct{ fs.FileInfo }

func (c cleanInfo) Mode() fs.FileMode { return c.FileInfo.Mode() &^ 0o022 }

func (l lstatFS) Lstat(name string) (fs.FileInfo, error) {
	fi, err := l.FS.Lstat(name)
	if err != nil {
		return nil, err
	}
	return cleanInfo{fi}, nil
}

func TestDoctorTierLockedHomeFromHomeRoot(t *testing.T) {
	if !runtimeIsUnix() {
		t.Skip("ownership rules")
	}
	defer func(f func() bool) { cgoEnabled = f }(cgoEnabled)
	cgoEnabled = func() bool { return false }
	e := newTenv(t)
	resolved, err := filepath.EvalSymlinks(e.sb.Root) // the rule refuses symlinks in the chain (macOS /var)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(resolved, "homes")
	home := filepath.Join(root, "u")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	euid := uint32(sys.Euid())
	opt := e.tierOpt("linux")
	opt.FS = lstatFS{e.fs}
	opt.Env = sys.MapEnv{Vars: map[string]string{"HOME": home}}
	opt.TierOwner = func(fi fs.FileInfo) (uint32, bool) {
		if fi.Name() == "u" {
			return euid, true
		}
		return 0, true
	}
	opt.TierLock = lockedWith(root)
	opt.HomeLookup = func(int) (string, error) { return "", sys.ErrNoPasswdEntry }
	c := byID(Doctor(opt))["tier.locked_home"]
	if c.Status != CheckOK || !strings.Contains(c.Message, home) || !strings.Contains(c.Message, "来源 home_root") {
		t.Fatalf("home_root rule: %+v", c)
	}
	// the source is shown for passwd too
	opt.HomeLookup = func(int) (string, error) { return "/home/u", nil }
	if c := byID(Doctor(opt))["tier.locked_home"]; !strings.Contains(c.Message, "来源 passwd") {
		t.Errorf("passwd source: %+v", c)
	}
	// a rule violation is named by the error
	opt.HomeLookup = func(int) (string, error) { return "", sys.ErrNoPasswdEntry }
	opt.Env = sys.MapEnv{Vars: map[string]string{"HOME": filepath.Join(resolved, "elsewhere", "u")}}
	if c := byID(Doctor(opt))["tier.locked_home"]; c.Status != CheckFail || !strings.Contains(c.Message, "无法确定家目录") {
		t.Errorf("home outside home_root: %+v", c)
	}
}

func TestDoctorTierRepoSandboxWeakened(t *testing.T) {
	e := newTenv(t)
	repo := filepath.Join(e.sb.Root, "repo")
	sub := filepath.Join(repo, "pkg", "a")
	for _, d := range []string{filepath.Join(repo, ".git"), sub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := func(t1 bool, cwd string) Check {
		opt := e.tierOpt("linux")
		tr := model.TierT0
		if t1 {
			tr = model.TierT1
		}
		opt.Tier = fakeProbe{model.TierInfo{Tier: tr}}
		opt.Cwd = cwd
		return byID(Doctor(opt))["tier.repo_sandbox_weakened"]
	}
	if c := run(true, sub); c.Status != CheckOK {
		t.Errorf("clean: %+v", c)
	}
	if c := run(true, ""); c.Status != CheckWarn || !strings.Contains(c.Message, "not evaluated") {
		t.Errorf("no cwd: %+v", c)
	}

	// the repository top is weakened, the session starts in a subdirectory
	e.write(filepath.Join(repo, ".claude", "settings.json"), `{"sandbox":{"excludedCommands":["npm"]}}`)
	c := run(true, sub)
	if c.Status != CheckFail || !strings.Contains(c.Message, repo+": settings.json:sandbox.excludedCommands") ||
		!strings.Contains(c.Fix, "只会记为 T0") || !strings.Contains(c.Fix, "绕过沙箱直接写证据") {
		t.Errorf("T1 + weakened top: %+v", c)
	}
	if c := run(false, sub); c.Status != CheckWarn {
		t.Errorf("T0 + weakened top: %+v", c)
	}

	// the current directory itself is weakened as well (local layer): both are listed
	e.write(filepath.Join(sub, ".claude", "settings.local.json"), `{"sandbox":{"network":{"allowUnixSockets":["/var/run/docker.sock"]}}}`)
	c = run(true, sub)
	if !strings.Contains(c.Message, sub+": settings.local.json:sandbox.network.allowUnixSockets") || !strings.Contains(c.Message, repo+": settings.json:") {
		t.Errorf("both directories must be checked: %+v", c)
	}
	// cwd == repo top is reported once
	c = run(true, repo)
	if strings.Count(c.Message, repo+":") != 1 {
		t.Errorf("duplicate listing: %s", c.Message)
	}
	// a symlinked .claude is reported as such
	if runtimeIsUnix() {
		other := filepath.Join(e.sb.Root, "other")
		e.write(filepath.Join(other, "settings.json"), `{}`)
		clean := filepath.Join(e.sb.Root, "linked")
		if err := os.MkdirAll(clean, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(other, filepath.Join(clean, ".claude")); err != nil {
			t.Fatal(err)
		}
		if c := run(true, clean); c.Status != CheckFail || !strings.Contains(c.Message, ":symlink") {
			t.Errorf("symlinked .claude: %+v", c)
		}
	}
}

func TestDoctorTierBinaryHardening(t *testing.T) {
	e := newTenv(t)
	put := func(name string, data []byte) string {
		p := filepath.Join(e.sb.Root, "bin", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	run := func(goos string, t1 bool, self string) Check {
		opt := e.tierOpt(goos)
		tr := model.TierT0
		if t1 {
			tr = model.TierT1
		}
		opt.Tier = fakeProbe{model.TierInfo{Tier: tr}}
		opt.Self = self
		return byID(Doctor(opt))["tier.binary_hardening"]
	}
	hardened := put("hardened", machoFixture(true, csRuntime))
	noRuntime := put("noruntime", machoFixture(true, 0))
	unsigned := put("unsigned", machoFixture(false, 0))
	static := put("static", elfFixture(false))
	dynamic := put("dynamic", elfFixture(true))
	script := put("script", []byte("#!/bin/sh\n"))

	var golden strings.Builder
	record := func(name string, c Check) {
		out := renderChecks([]Check{c}, e.sb.Root, "tier.binary_hardening")
		// The golden is platform-neutral: native Windows spells paths with
		// backslashes and phrases a missing file in its own words.
		out = strings.ReplaceAll(out, "\\", "/")
		out = strings.ReplaceAll(out, "The system cannot find the file specified.", "no such file or directory")
		fmt.Fprintf(&golden, "== %s\n%s", name, out)
	}
	for _, c := range []struct {
		name string
		goos string
		t1   bool
		self string
		want CheckStatus
		msg  string
	}{
		{"darwin hardened T1", "darwin", true, hardened, CheckOK, "hardened runtime (CS_RUNTIME)"},
		{"darwin no runtime T1", "darwin", true, noRuntime, CheckFail, "DYLD_INSERT_LIBRARIES"},
		{"darwin no runtime T0", "darwin", false, noRuntime, CheckWarn, "DYLD_INSERT_LIBRARIES"},
		{"darwin unsigned T1", "darwin", true, unsigned, CheckFail, "not code-signed"},
		{"darwin not a mach-o", "darwin", true, script, CheckWarn, "not evaluated"},
		{"linux static T1", "linux", true, static, CheckOK, "无 PT_INTERP"},
		{"linux dynamic T1", "linux", true, dynamic, CheckWarn, "LD_PRELOAD"},
		{"linux dynamic T0", "linux", false, dynamic, CheckWarn, "LD_PRELOAD"},
		{"linux not an elf", "linux", true, script, CheckWarn, "not evaluated"},
		{"missing file", "linux", true, filepath.Join(e.sb.Root, "bin", "absent"), CheckWarn, "not evaluated"},
		{"no path", "linux", true, "", CheckWarn, "not evaluated"},
	} {
		got := run(c.goos, c.t1, c.self)
		record(c.name, got)
		if got.Status != c.want || !strings.Contains(got.Message, c.msg) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if c := run("darwin", true, noRuntime); !strings.Contains(c.Fix, "codesign --force -s - -o runtime ") {
		t.Errorf("darwin fix: %q", c.Fix)
	}
	checkGolden(t, "doctor_binary_hardening", golden.String())
}

func TestDoctorTierWindowsOnlyHasTheLegacyRows(t *testing.T) {
	e := newTenv(t)
	cs := byID(Doctor(e.tierOpt("windows")))
	for id := range cs {
		if strings.HasPrefix(id, "tier.") && id != "tier.level" && id != "tier.windows_t0" {
			t.Errorf("windows emitted %s", id)
		}
	}
}

// Every tier.* id is emitted on darwin/linux.
func TestDoctorTierIDs(t *testing.T) {
	e := newTenv(t)
	cs := byID(Doctor(e.tierOpt("linux")))
	for _, id := range []string{"tier.level", "tier.declared_mismatch", "tier.runner_sandbox", "tier.redirect_ignored",
		"tier.locked_home", "tier.repo_sandbox_weakened", "tier.binary_hardening"} {
		if c, ok := cs[id]; !ok || c.Message == "" {
			t.Errorf("%s missing or empty", id)
		}
	}
	if _, ok := cs["tier.windows_t0"]; ok {
		t.Error("tier.windows_t0 is windows-only")
	}
}

var _ = bytes.Equal

// The hardening probes parse bytes of a file on disk; they must reject or
// classify anything without panicking.
func FuzzBinaryHardening(f *testing.F) {
	f.Add(machoFixture(true, csRuntime))
	f.Add(machoFixture(false, 0))
	f.Add(universalFixture(machoFixture(true, csRuntime), machoFixture(true, 0)))
	f.Add(elfFixture(true))
	f.Add(elfFixture(false))
	f.Add([]byte("#!/bin/sh\n"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = machoRuntime(b)
		_, _ = elfDynamic(b)
	})
}
