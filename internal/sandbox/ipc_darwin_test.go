//go:build darwin

package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

// The darwin IPC section (docs/p2-design.md §3.4 "macOS 的非文件逃逸通道"):
// every channel by which a sandboxed process can ask a process OUTSIDE the
// sandbox to act for it must fail, with the full profile and with the
// test-only variant that has the process-exec denials removed (so that the
// mach-lookup / job-creation / lsopen / appleevent-send denials are shown to
// work on their own). Cases that write real user state or touch the window
// server and pasteboard run only when CI=true (and clean up after
// themselves).

func randHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

var profileVariants = []struct {
	name string
	fn   func(Spec) string // nil ⇒ the real DarwinProfile
}{
	{"full profile", nil},
	{"without process-exec denials", func(s Spec) string { return darwinProfile(s, false) }},
}

func ipcFix(t *testing.T) *fixture {
	t.Helper()
	f := fix(t, model.NetworkDeny)
	f.extra = append(f.extra, "TMPDIR="+f.tmp)
	return f
}

// waitAbsent waits d, then asserts the marker does not exist.
func waitAbsent(t *testing.T, d time.Duration, paths ...string) {
	t.Helper()
	time.Sleep(d)
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("an outside process acted on the sandbox's behalf: %s exists", p)
		}
	}
}

// A .command file: opened by Terminal it would run `touch <marker>` outside
// every sandbox.
func writeCommandFile(t *testing.T, dir, marker string) string {
	t.Helper()
	p := filepath.Join(dir, "probe.command")
	if err := os.WriteFile(p, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDarwinToolchainsRunInsideTheProfile(t *testing.T) {
	f := ipcFix(t)
	script := `
id -un >/dev/null && echo id-ok
python3 -c 'import pwd,os; pwd.getpwuid(os.getuid()); print("pwd-ok")'
python3 -c 'import ssl; ssl.create_default_context(); print("ssl-ok")'
/usr/bin/env true && echo env-ok
git --version >/dev/null && echo git-ok`
	if p, err := exec.LookPath("node"); err == nil {
		script += "\n" + p + ` -e 'require("os").userInfo(); console.log("node-ok")'`
	}
	res := f.sh(script, runOpts{})
	for _, want := range []string{"id-ok", "pwd-ok", "ssl-ok", "env-ok", "git-ok"} {
		if !strings.Contains(res.Out, want) {
			t.Errorf("%s missing; the IPC allowlist is too small for this tool:\n%s", want, res.Out)
		}
	}
	// A go test that uses os/user and net/http/httptest-free code.
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Log("go not found; skipping the go test part")
		return
	}
	mkfiles(t, f.repo, "gouser/go.mod", "gouser/u_test.go")
	os.WriteFile(rp(f, "gouser/go.mod"), []byte("module example.invalid/u\n\ngo 1.21\n"), 0o644)
	os.WriteFile(rp(f, "gouser/u_test.go"), []byte("package u\nimport (\"os/user\";\"testing\")\nfunc TestU(t *testing.T){ if _, err := user.Current(); err != nil { t.Fatal(err) } }\n"), 0o644)
	goroot, _ := exec.Command(goBin, "env", "GOROOT").Output()
	res = f.sh(`cd "$R/gouser" && go test -count=1 ./... 2>&1`, runOpts{env: []string{"GOROOT=" + strings.TrimSpace(string(goroot)), "GOPROXY=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod"}})
	if res.ExitErr != nil {
		t.Errorf("go test with os/user failed under the profile:\n%s", res.Out)
	}
}

func TestDarwinLaunchServicesAndAppleEvents(t *testing.T) {
	for _, v := range profileVariants {
		t.Run(v.name, func(t *testing.T) {
			f := ipcFix(t)
			marker := filepath.Join(f.outside, "marker-"+randHex(t))
			cmdFile := writeCommandFile(t, f.repo, marker)
			opts := runOpts{darwinProfile: v.fn, timeout: 40 * time.Second}

			// open -a, standard path and a copy placed in TMPDIR.
			res := f.sh(`cp /usr/bin/open "$TMPDIR/open2"; /usr/bin/open -g -j -a Terminal "`+cmdFile+`"; echo "std=$?"; "$TMPDIR/open2" -g -j -a Terminal "`+cmdFile+`"; echo "copy=$?"`, opts)
			t.Logf("open: %s", strings.ReplaceAll(strings.TrimSpace(res.Out), "\n", " | "))
			for _, tag := range []string{"std=0", "copy=0"} {
				if strings.Contains(res.Out, tag) {
					t.Errorf("open succeeded (%s)", tag)
				}
			}

			// osascript, standard path and a copy.
			ev := `tell application "Finder" to make new folder at (POSIX file "` + f.outside + `") with properties {name:"` + filepath.Base(marker) + `-osa"}`
			res = f.sh(`cp /usr/bin/osascript "$TMPDIR/osa2"; /usr/bin/osascript -e '`+ev+`'; echo "std=$?"; "$TMPDIR/osa2" -e '`+ev+`'; echo "copy=$?"`, opts)
			t.Logf("osascript: %s", strings.ReplaceAll(strings.TrimSpace(res.Out), "\n", " | "))
			for _, tag := range []string{"std=0", "copy=0"} {
				if strings.Contains(res.Out, tag) {
					t.Errorf("osascript succeeded (%s)", tag)
				}
			}

			// launchctl submit, standard path and a copy; the job would touch a
			// file outside W.
			label := "net.ballast.probe." + randHex(t)
			t.Cleanup(func() {
				exec.Command("/bin/launchctl", "remove", label).Run()
				exec.Command("/bin/launchctl", "remove", label+"c").Run()
			})
			res = f.sh(`cp /bin/launchctl "$TMPDIR/lc2"; /bin/launchctl submit -l `+label+` -- /usr/bin/touch "`+marker+`-lc"; echo "std=$?"; "$TMPDIR/lc2" submit -l `+label+`c -- /usr/bin/touch "`+marker+`-lc2"; echo "copy=$?"`, opts)
			t.Logf("launchctl: %s", strings.ReplaceAll(strings.TrimSpace(res.Out), "\n", " | "))

			// python3 ctypes straight into LaunchServices and the AppleEvent
			// manager (no process-exec rule can match these).
			py := `
import ctypes, ctypes.util, sys
cf = ctypes.CDLL(ctypes.util.find_library("CoreFoundation"))
cs = ctypes.CDLL(ctypes.util.find_library("CoreServices"))
cf.CFURLCreateFromFileSystemRepresentation.restype = ctypes.c_void_p
cf.CFURLCreateFromFileSystemRepresentation.argtypes = [ctypes.c_void_p, ctypes.c_char_p, ctypes.c_long, ctypes.c_bool]
p = sys.argv[1].encode()
url = cf.CFURLCreateFromFileSystemRepresentation(None, p, len(p), False)
cs.LSOpenCFURLRef.argtypes = [ctypes.c_void_p, ctypes.c_void_p]
cs.LSOpenCFURLRef.restype = ctypes.c_int32
print("LSOPEN", cs.LSOpenCFURLRef(url, None))

class AEDesc(ctypes.Structure):
    _fields_ = [("descriptorType", ctypes.c_uint32), ("dataHandle", ctypes.c_void_p)]
def four(s): return int.from_bytes(s.encode(), "big")
target, event, reply = AEDesc(), AEDesc(), AEDesc()
b = b"com.apple.finder"
cs.AECreateDesc.argtypes = [ctypes.c_uint32, ctypes.c_char_p, ctypes.c_long, ctypes.POINTER(AEDesc)]
cs.AECreateDesc.restype = ctypes.c_int32
rc = cs.AECreateDesc(four("bund"), b, len(b), ctypes.byref(target))
cs.AECreateAppleEvent.argtypes = [ctypes.c_uint32, ctypes.c_uint32, ctypes.POINTER(AEDesc), ctypes.c_int16, ctypes.c_int32, ctypes.POINTER(AEDesc)]
cs.AECreateAppleEvent.restype = ctypes.c_int32
rc2 = cs.AECreateAppleEvent(four("aevt"), four("oapp"), ctypes.byref(target), -1, 0, ctypes.byref(event))
cs.AESendMessage.argtypes = [ctypes.POINTER(AEDesc), ctypes.POINTER(AEDesc), ctypes.c_int32, ctypes.c_long]
cs.AESendMessage.restype = ctypes.c_int32
rc3 = cs.AESendMessage(ctypes.byref(event), ctypes.byref(reply), 0x3 | 0x10, 300)
print("AESEND", rc, rc2, rc3)
`
			res = f.runArgv([]string{"python3", "-c", py, cmdFile}, opts)
			t.Logf("ctypes: %s", strings.ReplaceAll(strings.TrimSpace(res.Out), "\n", " | "))
			if strings.Contains(res.Out, "LSOPEN 0") {
				t.Errorf("LSOpenCFURLRef succeeded")
			}
			if strings.Contains(res.Out, "AESEND 0 0 0") {
				t.Errorf("AESendMessage to Finder succeeded")
			}

			waitAbsent(t, 4*time.Second, marker, marker+"-lc", marker+"-lc2", marker+"-osa")
			if out, _ := exec.Command("/bin/launchctl", "list").CombinedOutput(); strings.Contains(string(out), label) {
				t.Errorf("a launchd job %s was created outside the sandbox", label)
			}
		})
	}
}

// (deny signal): the child cannot signal processes outside its sandbox.
func TestDarwinSignalsStayInside(t *testing.T) {
	f := ipcFix(t)
	victim := exec.Command("/bin/sleep", "60")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { victim.Process.Kill(); victim.Wait() })
	res := f.sh(fmt.Sprintf(`kill -TERM %d; echo "rc=$?"; sleep 1 & p=$!; kill $p && echo inside-ok`, victim.Process.Pid), runOpts{})
	if strings.Contains(res.Out, "rc=0") {
		t.Errorf("signalled a process outside the sandbox: %s", res.Out)
	}
	if !strings.Contains(res.Out, "inside-ok") {
		t.Errorf("signals within the sandbox must keep working: %s", res.Out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "/bin/kill", "-0", fmt.Sprint(victim.Process.Pid)).Run(); err != nil {
		t.Errorf("the outside process died")
	}
}

// ---- cases that need CI=true: real user state ----

func needCI(t *testing.T, why string) {
	t.Helper()
	if !ciTrue() {
		t.Skipf("runs only when CI=true (%s)", why)
	}
}

// cfprefsd writes preferences on the client's behalf, outside the sandbox.
// The assertion uses a NAMED preference domain: an absolute-path domain
// fails even without the denials, so it cannot tell who wrote.
func TestDarwinPreferencesWrite(t *testing.T) {
	needCI(t, "writes the real user's preferences if the sandbox fails")
	for _, v := range profileVariants {
		t.Run(v.name, func(t *testing.T) {
			f := ipcFix(t)
			domain := "net.ballast.probe." + randHex(t)
			home, _ := os.UserHomeDir()
			plist := filepath.Join(home, "Library", "Preferences", domain+".plist")
			t.Cleanup(func() {
				exec.Command("/usr/bin/defaults", "delete", domain).Run()
				os.Remove(plist)
			})
			res := f.sh(`/usr/bin/defaults write `+domain+` k v; echo "rc=$?"`, runOpts{darwinProfile: v.fn})
			t.Logf("defaults write: %s", strings.ReplaceAll(strings.TrimSpace(res.Out), "\n", " | "))
			time.Sleep(time.Second)
			if _, err := os.Stat(plist); err == nil {
				t.Errorf("%s exists: cfprefsd wrote on the sandbox's behalf", plist)
			}
			if out, err := exec.Command("/usr/bin/defaults", "read", domain).CombinedOutput(); err == nil {
				t.Errorf("defaults read outside the sandbox found the domain: %s", out)
			}
		})
	}
	// Evidence for the (deny user-preference-write) rule itself: the same
	// write under a profile WITHOUT it. Recorded, not asserted (if cfprefsd
	// refuses for another reason the log says so); always cleaned up.
	t.Run("record: profile without user-preference-write", func(t *testing.T) {
		f := ipcFix(t)
		domain := "net.ballast.probe." + randHex(t)
		home, _ := os.UserHomeDir()
		plist := filepath.Join(home, "Library", "Preferences", domain+".plist")
		t.Cleanup(func() {
			exec.Command("/usr/bin/defaults", "delete", domain).Run()
			os.Remove(plist)
		})
		noPref := func(s Spec) string {
			return strings.ReplaceAll(darwinProfile(s, true), "(deny user-preference-write)\n", "")
		}
		f.sh(`/usr/bin/defaults write `+domain+` k v`, runOpts{darwinProfile: noPref})
		time.Sleep(time.Second)
		_, err := os.Stat(plist)
		t.Logf("without (deny user-preference-write): plist exists = %v", err == nil)
	})
}

// CGEventPost needs a WindowServer connection; the pasteboard server is a
// mach service of its own.
func TestDarwinWindowServerAndPasteboard(t *testing.T) {
	needCI(t, "touches the window server and the user's pasteboard")
	py := `
import ctypes, ctypes.util
cg = ctypes.CDLL(ctypes.util.find_library("CoreGraphics"))
cg.CGSMainConnectionID.restype = ctypes.c_uint32
print("CID", cg.CGSMainConnectionID())
`
	control, err := exec.Command("python3", "-c", py).CombinedOutput()
	if err != nil {
		t.Fatalf("control: %v\n%s", err, control)
	}
	t.Logf("control (no sandbox): %s", strings.TrimSpace(string(control)))
	for _, v := range profileVariants {
		t.Run(v.name, func(t *testing.T) {
			f := ipcFix(t)
			res := f.runArgv([]string{"python3", "-c", py}, runOpts{darwinProfile: v.fn})
			t.Logf("sandboxed: %s", strings.TrimSpace(res.Out))
			if strings.Contains(string(control), "CID 0") {
				t.Skip("no WindowServer session on this host (the control has no connection either): inconclusive")
			}
			if !strings.Contains(res.Out, "CID 0") {
				t.Errorf("the sandboxed process got a WindowServer connection (CGEventPost is possible): %s", res.Out)
			}
		})
	}
	// Pasteboard: the text the sandbox tries to put there must not appear.
	orig, _ := exec.Command("/usr/bin/pbpaste").Output()
	t.Cleanup(func() {
		now, _ := exec.Command("/usr/bin/pbpaste").Output()
		if string(now) != string(orig) {
			c := exec.Command("/usr/bin/pbcopy")
			c.Stdin = strings.NewReader(string(orig))
			c.Run()
		}
	})
	for _, v := range profileVariants {
		t.Run("pasteboard/"+v.name, func(t *testing.T) {
			f := ipcFix(t)
			probe := "ballast-probe-" + randHex(t)
			res := f.sh(`printf '%s' '`+probe+`' | /usr/bin/pbcopy; echo "rc=$?"; /usr/bin/pbpaste`, runOpts{darwinProfile: v.fn})
			t.Logf("pbcopy: %s", strings.ReplaceAll(strings.TrimSpace(res.Out), "\n", " | "))
			now, _ := exec.Command("/usr/bin/pbpaste").Output()
			if strings.Contains(string(now), probe) {
				t.Errorf("the sandbox wrote to the pasteboard")
			}
			if string(now) != string(orig) {
				t.Errorf("the pasteboard changed outside the sandbox")
			}
		})
	}
}

// Keychain access goes through com.apple.SecurityServer, which is never in
// the allowlist. Recorded, not asserted: the exact failure text differs
// between macOS releases.
func TestDarwinSecurityServerRecord(t *testing.T) {
	f := ipcFix(t)
	res := f.sh(`/usr/bin/security find-generic-password -s ballast-probe-`+randHex(t)+` 2>&1; echo "rc=$?"`, runOpts{})
	t.Logf("security find-generic-password under the profile: %s", strings.ReplaceAll(strings.TrimSpace(res.Out), "\n", " | "))
	if strings.Contains(res.Out, "password:") {
		t.Errorf("the keychain returned data")
	}
}

// Control: the same channels under a profile with the IPC section cut off
// show that the denials (not some other property of the test setup) are what
// close them. Recorded, not asserted, and only when CI=true: the control
// really asks LaunchServices and launchd to do something (a hidden Terminal
// runs a script that touches a marker file; a launchd job touches another).
func TestDarwinIPCControl(t *testing.T) {
	needCI(t, "the control really opens a hidden Terminal and submits a launchd job")
	f := ipcFix(t)
	marker := filepath.Join(f.outside, "marker-"+randHex(t))
	cmdFile := writeCommandFile(t, f.repo, marker)
	label := "net.ballast.probe." + randHex(t)
	t.Cleanup(func() { exec.Command("/bin/launchctl", "remove", label).Run() })
	noIPC := func(s Spec) string {
		p := darwinProfile(s, false)
		return p[:strings.Index(p, "(deny mach-lookup)")]
	}
	py := `
import ctypes, ctypes.util, sys
cf = ctypes.CDLL(ctypes.util.find_library("CoreFoundation"))
cs = ctypes.CDLL(ctypes.util.find_library("CoreServices"))
cf.CFURLCreateFromFileSystemRepresentation.restype = ctypes.c_void_p
cf.CFURLCreateFromFileSystemRepresentation.argtypes = [ctypes.c_void_p, ctypes.c_char_p, ctypes.c_long, ctypes.c_bool]
p = sys.argv[1].encode()
url = cf.CFURLCreateFromFileSystemRepresentation(None, p, len(p), False)
cs.LSOpenCFURLRef.argtypes = [ctypes.c_void_p, ctypes.c_void_p]
cs.LSOpenCFURLRef.restype = ctypes.c_int32
print("LSOPEN", cs.LSOpenCFURLRef(url, None))
`
	res := f.sh(`/bin/launchctl submit -l `+label+` -- /usr/bin/touch "`+marker+`-lc"; echo "launchctl rc=$?"; python3 -c '`+py+`' "`+cmdFile+`"`, runOpts{darwinProfile: noIPC, timeout: 40 * time.Second})
	t.Logf("control output: %s", strings.ReplaceAll(strings.TrimSpace(res.Out), "\n", " | "))
	time.Sleep(5 * time.Second)
	_, errLC := os.Stat(marker + "-lc")
	_, errCmd := os.Stat(marker)
	t.Logf("control (IPC section removed): launchd job ran = %v, Terminal ran the script = %v", errLC == nil, errCmd == nil)
	os.Remove(marker + "-lc")
	os.Remove(marker)
}
