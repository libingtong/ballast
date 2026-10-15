package cli_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// snapshotTree maps every regular file under root to its bytes, skipping the
// directories named in skip (Ballast's own state under .ballast).
func snapshotTree(t *testing.T, root string, skip ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			for _, s := range skip {
				if d.Name() == s {
					return filepath.SkipDir
				}
			}
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel(root, p)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func diffTrees(before, after map[string]string) []string {
	var d []string
	for k, v := range before {
		if av, ok := after[k]; !ok {
			d = append(d, "removed: "+k)
		} else if av != v {
			d = append(d, "changed: "+k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			d = append(d, "added: "+k)
		}
	}
	sort.Strings(d)
	return d
}

// clientFiles are realistic pre-existing user configurations: each already
// holds unrelated settings and one foreign hook that must survive.
func seedClientConfigs(t *testing.T, s *sandbox) {
	t.Helper()
	home := s.sb.Home
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{
  "theme": "dark",
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/usr/bin/true"}]}
    ]
  }
}
`)
	writeFile(t, filepath.Join(home, ".codex", "hooks.json"), `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/bin/true"}]}]}}
`)
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), "model = \"gpt\"\n\n[features]\nhooks = true\n")
	writeFile(t, filepath.Join(home, ".cursor", "hooks.json"), `{"version":1,"hooks":{"stop":[{"command":"echo hi"}]}}
`)
	writeFile(t, filepath.Join(home, ".hermes", "config.yaml"), "model: x\nhooks:\n  pre_tool_call:\n    - command: \"/usr/bin/true\"\n      timeout: 5\ntail: 1\n")
}

var allClients = []string{"claude-code", "codex", "cursor", "hermes"}

// TestInstallRoundTripEveryClient: install then uninstall leaves every
// client configuration byte-identical (empty diff), also when installing
// all clients at once, and a second install is a no-op.
func TestInstallRoundTripEveryClient(t *testing.T) {
	for _, client := range append(append([]string{}, allClients...), "all") {
		t.Run(client, func(t *testing.T) {
			s := newSandbox(t)
			installE2ERules(t, s) // gives the Cursor rules file real content
			seedClientConfigs(t, s)
			before := snapshotTree(t, s.sb.Home, ".ballast")

			r := s.mustExit(0, "", "", "install", "--client", client, "--binary", ballastBin)
			mid := snapshotTree(t, s.sb.Home, ".ballast")
			if len(diffTrees(before, mid)) == 0 {
				t.Fatalf("install changed nothing:\n%s", r.stdout)
			}
			joined := ""
			for _, v := range mid {
				joined += v
			}
			if !strings.Contains(joined, ballastBin) && !strings.Contains(joined, strings.ReplaceAll(ballastBin, `\`, `\\`)) {
				t.Errorf("hook commands should carry the binary path %s", ballastBin)
			}
			// Foreign entries survive the install.
			if client == "claude-code" || client == "all" {
				if !strings.Contains(mid[".claude/settings.json"], "/usr/bin/true") || !strings.Contains(mid[".claude/settings.json"], `"theme"`) {
					t.Errorf("foreign claude settings lost:\n%s", mid[".claude/settings.json"])
				}
			}

			// Idempotent: a second install changes no byte.
			r = s.mustExit(0, "", "", "install", "--client", client, "--binary", ballastBin)
			if again := snapshotTree(t, s.sb.Home, ".ballast"); len(diffTrees(mid, again)) != 0 {
				t.Errorf("second install is not idempotent: %v", diffTrees(mid, again))
			}
			if !strings.Contains(r.stdout, "无变化") {
				t.Errorf("second install should report no change:\n%s", r.stdout)
			}

			s.mustExit(0, "", "", "uninstall", "--client", client)
			after := snapshotTree(t, s.sb.Home, ".ballast")
			if d := diffTrees(before, after); len(d) != 0 {
				t.Errorf("install → uninstall must leave no diff, got %v", d)
			}
		})
	}
}

// TestInstallRoundTripFromEmptyHome: with no pre-existing configuration the
// files install created are deleted again. Codex's [features] hooks = true
// is the documented exception: uninstall never switches it off.
func TestInstallRoundTripFromEmptyHome(t *testing.T) {
	s := newSandbox(t)
	installE2ERules(t, s)
	before := snapshotTree(t, s.sb.Home, ".ballast")
	s.mustExit(0, "", "", "install", "--client", "all", "--force", "--binary", ballastBin)
	mid := snapshotTree(t, s.sb.Home, ".ballast")
	for _, want := range []string{".claude/settings.json", ".codex/hooks.json", ".codex/config.toml", ".cursor/hooks.json", ".cursor/rules/ballast.mdc", ".hermes/config.yaml"} {
		if _, ok := mid[want]; !ok {
			t.Errorf("install did not create %s (have %v)", want, keys(mid))
		}
	}
	mdc := mid[".cursor/rules/ballast.mdc"]
	if !strings.Contains(mdc, "alwaysApply: true") || !strings.Contains(mdc, "no-long-sleep") {
		t.Errorf("ballast.mdc should be rendered from the current rules:\n%s", mdc)
	}
	s.mustExit(0, "", "", "uninstall", "--all")
	after := snapshotTree(t, s.sb.Home, ".ballast")
	var left []string
	for _, d := range diffTrees(before, after) {
		left = append(left, d)
	}
	if len(left) != 1 || left[0] != "added: .codex/config.toml" {
		t.Errorf("only codex config.toml (features.hooks) may remain, got %v", left)
	}
	if got := after[".codex/config.toml"]; strings.Contains(got, "ballast") || !strings.Contains(got, "hooks = true") {
		t.Errorf("codex config.toml after uninstall: %q", got)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestInstallDryRunWritesNothing(t *testing.T) {
	s := newSandbox(t)
	seedClientConfigs(t, s)
	before := snapshotTree(t, s.sb.Home)
	r := s.mustExit(0, "", "", "install", "--client", "all", "--binary", ballastBin, "--dry-run")
	if !strings.Contains(r.stdout, "+++") || !strings.Contains(r.stdout, "---") {
		t.Errorf("--dry-run should print a unified diff:\n%s", r.stdout)
	}
	if d := diffTrees(before, snapshotTree(t, s.sb.Home)); len(d) != 0 {
		t.Errorf("--dry-run wrote files: %v", d)
	}
	s.mustExit(0, "", "", "install", "--client", "all", "--binary", ballastBin)
	installed := snapshotTree(t, s.sb.Home)
	s.mustExit(0, "", "", "uninstall", "--all", "--dry-run")
	if d := diffTrees(installed, snapshotTree(t, s.sb.Home)); len(d) != 0 {
		t.Errorf("uninstall --dry-run wrote files: %v", d)
	}
}

func TestInstallCodexTrust(t *testing.T) {
	s := newSandbox(t)
	seedClientConfigs(t, s)
	before := snapshotTree(t, s.sb.Home, ".ballast")
	s.mustExit(0, "", "", "install", "--client", "codex", "--binary", ballastBin)
	if strings.Contains(readFile(t, filepath.Join(s.sb.Home, ".codex", "config.toml")), "trusted_hash") {
		t.Error("trust entries must not be written without --codex-trust")
	}
	s.mustExit(0, "", "", "install", "--client", "codex", "--binary", ballastBin, "--codex-trust")
	if !strings.Contains(readFile(t, filepath.Join(s.sb.Home, ".codex", "config.toml")), "trusted_hash") {
		t.Error("--codex-trust should write trust entries")
	}
	s.mustExit(0, "", "", "uninstall", "--client", "codex")
	if d := diffTrees(before, snapshotTree(t, s.sb.Home, ".ballast")); len(d) != 0 {
		t.Errorf("round trip with --codex-trust: %v", d)
	}
}

func TestInstallErrors(t *testing.T) {
	s := newSandbox(t)
	empty := filepath.Join(s.sb.Root, "empty-path")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	s.setenv("PATH", empty) // no real client can be detected through PATH
	before := snapshotTree(t, s.sb.Home)

	// Usage errors.
	s.mustExit(2, "", "", "install")                                                 // --client required
	s.mustExit(2, "", "", "install", "--client", "nobody", "--binary", ballastBin)   // unknown client
	s.mustExit(2, "", "", "install", "--client", "claude-code", "--binary", "rel/b") // relative binary
	s.mustExit(2, "", "", "install", "--client", "claude-code", "--binary", ballastBin)
	s.mustExit(2, "", "", "uninstall", "--client", "nobody")
	s.mustExit(2, "", "", "uninstall", "--all", "--client", "codex")

	// The auto-resolved binary lives in a temporary directory (like `go run`
	// output): refused with 2, naming --binary.
	r := s.mustExit(2, "", "", "install", "--client", "claude-code", "--force")
	if !strings.Contains(r.stderr, "--binary") {
		t.Errorf("unstable path message should mention --binary: %q", r.stderr)
	}
	if d := diffTrees(before, snapshotTree(t, s.sb.Home)); len(d) != 0 {
		t.Errorf("failed installs wrote files: %v", d)
	}

	// Corrupt settings: refused with 3, nothing written.
	settings := filepath.Join(s.sb.Home, ".claude", "settings.json")
	writeFile(t, settings, "{ this is not json")
	snap := snapshotTree(t, s.sb.Home)
	s.mustExit(3, "", "", "install", "--client", "claude-code", "--binary", ballastBin)
	if d := diffTrees(snap, snapshotTree(t, s.sb.Home)); len(d) != 0 {
		t.Errorf("install over corrupt settings wrote files: %v", d)
	}
	if err := os.Remove(settings); err != nil {
		t.Fatal(err)
	}

	// A managed deployment of Ballast refuses the user-level install (1).
	managed := filepath.Join(s.sb.ManagedDir, "managed-settings.json")
	writeFile(t, managed, `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"/usr/local/libexec/ballast/ballast","args":["hook","--client","claude-code","--event","PreToolUse"]}]}]}}`)
	r = s.mustExit(1, "", "", "install", "--client", "claude-code", "--binary", ballastBin, "--force")
	if !strings.HasPrefix(r.stderr, "ballast: managed deployment present") || strings.Contains(r.stderr, "ballast: ballast:") {
		t.Errorf("managed refusal should carry exactly one ballast: prefix: %q", r.stderr)
	}
	if err := os.Remove(managed); err != nil {
		t.Fatal(err)
	}

	// Uninstall refuses (3) a manifest-listed file it cannot parse, and keeps
	// (1) an entry the user modified.
	s.mustExit(0, "", "", "install", "--client", "claude-code", "--binary", ballastBin, "--force")
	var doc map[string]any
	if err := json.Unmarshal([]byte(readFile(t, settings)), &doc); err != nil {
		t.Fatal(err)
	}
	orig := readFile(t, settings)
	writeFile(t, settings, strings.Replace(orig, `"type": "command"`, `"timeout": 7, "type": "command"`, 1))
	if readFile(t, settings) == orig {
		t.Fatalf("test setup: settings did not change:\n%s", orig)
	}
	r = s.mustExit(1, "", "", "uninstall", "--client", "claude-code")
	if !strings.Contains(r.stderr, "warning") {
		t.Errorf("uninstall of a user-modified entry should warn: %q", r.stderr)
	}
	writeFile(t, settings, "{ broken")
	s.mustExit(3, "", "", "uninstall", "--client", "claude-code")
}

func TestDoctor(t *testing.T) {
	s := newSandbox(t)
	installE2ERules(t, s)
	seedClientConfigs(t, s)
	s.mustExit(0, "", "", "install", "--client", "all", "--binary", ballastBin)

	r := s.mustExit(0, "", "", "doctor")
	for _, want := range []string{"层级: T0", "CI 是最终裁判", "claude-code", "codex", "cursor", "hermes", "已安装 Ballast"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, r.stdout)
		}
	}
	r = s.mustExit(0, "", "", "doctor", "--json")
	var rep struct {
		Tier    string `json:"tier"`
		OK      bool   `json:"ok"`
		Note    string `json:"note"`
		Clients []struct {
			ID        string `json:"id"`
			Installed bool   `json:"installed"`
			Status    string `json:"status"`
		} `json:"clients"`
		Checks []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, r.stdout)
	}
	if rep.Tier != "T0" || !rep.OK || !strings.Contains(rep.Note, "CI 是最终裁判") || len(rep.Clients) != 4 {
		t.Errorf("doctor json: %+v", rep)
	}
	for _, c := range rep.Clients {
		if !c.Installed || c.Status == "fail" {
			t.Errorf("client %s: %+v", c.ID, c)
		}
	}

	// A missing binary is a failure (exit 1) and the client row says so.
	s.mustExit(0, "", "", "install", "--client", "claude-code", "--force", "--binary", filepath.Join(s.sb.Root, "gone", "ballast"))
	r = s.mustExit(1, "", "", "doctor")
	if !strings.Contains(r.stdout, "binary.exists") || !strings.Contains(r.stdout, "CI 是最终裁判") {
		t.Errorf("doctor on a missing binary:\n%s", r.stdout)
	}
	// A corrupt manifest is a failure too.
	writeFile(t, filepath.Join(s.sb.BallastHome, "install-manifest.json"), "{garbage")
	r = s.mustExit(1, "", "", "doctor")
	if !strings.Contains(r.stdout, "manifest.valid") {
		t.Errorf("doctor on a corrupt manifest:\n%s", r.stdout)
	}
	s.mustExit(2, "", "", "doctor", "--bogus")
}

func TestDoctorFreshHomeOnlyWarns(t *testing.T) {
	s := newSandbox(t)
	r := s.mustExit(0, "", "", "doctor")
	if !strings.Contains(r.stdout, "T0") || !strings.Contains(r.stdout, "未安装 Ballast") {
		t.Errorf("fresh doctor:\n%s", r.stdout)
	}
}

func TestOrgRender(t *testing.T) {
	s := newSandbox(t)
	r := s.mustExit(0, "", "", "org", "render", "--binary", "/usr/local/libexec/ballast/ballast")
	var doc map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatalf("org render output is not JSON: %v\n%s", err, r.stdout)
	}
	if doc["allowManagedHooksOnly"] != true || dig(doc, "sandbox", "enabled") != true || dig(doc, "sandbox", "failIfUnavailable") != true {
		t.Errorf("T1 template keys: %v", doc)
	}
	hooks, _ := doc["hooks"].(map[string]any)
	if len(hooks) < 9 {
		t.Errorf("want a hook per Claude Code event, got %d", len(hooks))
	}
	// Nothing was written anywhere.
	if _, err := os.Stat(filepath.Join(s.sb.ManagedDir, "managed-settings.d")); err == nil {
		t.Error("org render without --write created the managed directory")
	}
	s.mustExit(4, "", "", "org", "render", "--write")
	if os.Geteuid() != 0 { // root may legitimately write into the (redirected) managed dir
		s.mustExit(4, "", "", "org", "render", "--write", "--i-am-admin")
		if _, err := os.Stat(filepath.Join(s.sb.ManagedDir, "managed-settings.d")); err == nil {
			t.Error("denied --write still created the managed directory")
		}
	}
	s.mustExit(2, "", "", "org", "render", "--client", "codex")
	s.mustExit(2, "", "", "org")
	if runtime.GOOS == "windows" {
		t.Log("windows: geteuid is -1; root branch skipped by design")
	}
}
