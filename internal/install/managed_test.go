package install

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeManaged(e *tenv, rel, content string) string {
	p := filepath.Join(e.lay.ManagedDir, rel)
	e.write(p, content)
	return p
}

func TestDetectManaged(t *testing.T) {
	const hookArgs = `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":%q,"args":["hook","--client","claude-code","--event","PreToolUse"]}]}]}}`
	const hookCmd = `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":%q}]}]}}`
	// The command string is split with the host platform's quoting rules
	// (POSIX single quotes; CommandLineToArgvW double quotes on Windows).
	quotedHookCommand := `"'/opt/my dir/ballast' hook --client claude-code"`
	if runtime.GOOS == "windows" {
		quotedHookCommand = `"\"C:\\opt\\my dir\\ballast.exe\" hook --client claude-code"`
	}
	cases := []struct {
		name, rel, content string
		want               bool
	}{
		{"args form", "managed-settings.json", strings.Replace(hookArgs, "%q", `"/usr/local/libexec/ballast/ballast"`, 1), true},
		{"windows exe", "managed-settings.json", strings.Replace(hookArgs, "%q", `"C:\\Program Files\\Ballast\\ballast.exe"`, 1), true},
		{"command string", "managed-settings.json", strings.Replace(hookCmd, "%q", `"/opt/b/ballast hook --client claude-code --event Stop"`, 1), true},
		{"quoted command string", "managed-settings.json", strings.Replace(hookCmd, "%q", quotedHookCommand, 1), true},
		{"drop-in directory", "managed-settings.d/50-ballast.json", strings.Replace(hookArgs, "%q", `"/usr/local/libexec/ballast/ballast"`, 1), true},
		{"other binary", "managed-settings.json", strings.Replace(hookArgs, "%q", `"/usr/bin/audit"`, 1), false},
		{"ballast without hook subcommand", "managed-settings.json", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/ballast","args":["verify"]}]}]}}`, false},
		{"name only as substring", "managed-settings.json", strings.Replace(hookArgs, "%q", `"/x/not-ballast-tool"`, 1), false},
		{"no hooks", "managed-settings.json", `{"allowManagedHooksOnly":true}`, false},
		{"hooks of wrong shape", "managed-settings.json", `{"hooks":{"Stop":"nope"}}`, false},
		{"non-json drop-in ignored", "managed-settings.d/readme.txt", "ballast hook", false},
	}
	for _, c := range cases {
		e := newTenv(t)
		file := writeManaged(e, c.rel, c.content)
		got, path, err := DetectManaged(e.lay, e.fs)
		if got != c.want {
			t.Errorf("%s: managed = %v (err %v), want %v", c.name, got, err, c.want)
		}
		if c.want && path != file {
			t.Errorf("%s: path = %q, want %q", c.name, path, file)
		}
		if !c.want && path != "" {
			t.Errorf("%s: path = %q", c.name, path)
		}
	}
}

func TestDetectManagedNothingDeployed(t *testing.T) {
	e := newTenv(t)
	got, path, err := DetectManaged(e.lay, e.fs)
	if got || path != "" || err != nil {
		t.Fatalf("%v %q %v", got, path, err)
	}
}

func TestDetectManagedToleratesBrokenFiles(t *testing.T) {
	e := newTenv(t)
	writeManaged(e, "managed-settings.json", "{broken")
	good := writeManaged(e, "managed-settings.d/b.json", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/ballast","args":["hook"]}]}]}}`)
	got, path, err := DetectManaged(e.lay, e.fs)
	if !got || path != good || err != nil {
		t.Fatalf("a broken file must not hide a good one: %v %q %v", got, path, err)
	}
	// only broken files: not managed, but the problem is reported
	e2 := newTenv(t)
	writeManaged(e2, "managed-settings.json", "{broken")
	got, _, err = DetectManaged(e2.lay, e2.fs)
	if got || err == nil {
		t.Fatalf("got=%v err=%v, want not managed + error", got, err)
	}
}

func TestDetectManagedDropInOrder(t *testing.T) {
	e := newTenv(t)
	hook := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/ballast","args":["hook"]}]}]}}`
	writeManaged(e, "managed-settings.d/20-b.json", hook)
	first := writeManaged(e, "managed-settings.d/10-a.json", hook)
	_, path, _ := DetectManaged(e.lay, e.fs)
	if path != first {
		t.Errorf("drop-ins must be read in lexical order, got %s", path)
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatal(err)
	}
}
