package tier

import (
	"strings"
	"testing"
)

func TestLoadOrgConfigValidation(t *testing.T) {
	const p = "/etc/claude-code/ballast/org.json"
	good := []string{
		`{"v":1}`,
		`{"v":1,"tier":"T1","runner_sandbox":"require","runner_network":"deny","runner_allow_write":[],"home_root":["/home"],"weakened_project":"deny","protected_extra":["~/.config/acme/ci-token"]}`,
		`{"v":1,"tier":"T0","runner_sandbox":"off","runner_network":"allow","weakened_project":"audit"}`,
		`{"v":1,"runner_network":"loopback","unknown_key":[1,2]}`,
		`{"v":1,"runner_allow_write":["~/.cache/go-build","/srv/shared/cache","/home/corp.example/u/x","/home/u/cache"]}`,
		`{"v":1,"home_root":["/home/corp.example","/Users"]}`,
		`{"v":1,"protected_extra":["/srv/policy","~/x"]}`,
	}
	for _, s := range good {
		m := newMemFS()
		m.writeFile(p, s, 0, 0o644)
		if _, err := LoadOrgConfig(m, p); err != nil {
			t.Errorf("rejected %s: %v", s, err)
		}
	}
	bad := map[string]string{
		`{`:                                 "",
		``:                                  "",
		`{"v":0}`:                           "unsupported v",
		`{"v":2}`:                           "unsupported v",
		`{}`:                                "unsupported v",
		`{"v":1} {"v":1}`:                   "trailing",
		`{"v":1,"tier":"T2"}`:               "tier",
		`{"v":1,"tier":"t1"}`:               "tier",
		`{"v":1,"runner_sandbox":"strict"}`: "runner_sandbox",
		`{"v":1,"runner_network":"open"}`:   "runner_network",
		`{"v":1,"weakened_project":"warn"}`: "weakened_project",
		`{"v":1,"protected_extra":["relative/path"]}`:                 "protected_extra",
		`{"v":1,"protected_extra":["~other/x"]}`:                      "protected_extra",
		`{"v":1,"runner_allow_write":["relative"]}`:                   "runner_allow_write",
		`{"v":1,"runner_allow_write":["~"]}`:                          "runner_allow_write",
		`{"v":1,"runner_allow_write":["~/"]}`:                         "runner_allow_write",
		`{"v":1,"runner_allow_write":["/"]}`:                          "ancestors",
		`{"v":1,"runner_allow_write":["/home"]}`:                      "ancestors",
		`{"v":1,"runner_allow_write":["/home/"]}`:                     "ancestors",
		`{"v":1,"runner_allow_write":["/Users"]}`:                     "ancestors",
		`{"v":1,"runner_allow_write":["/root"]}`:                      "ancestors",
		`{"v":1,"runner_allow_write":["/var/root"]}`:                  "ancestors",
		`{"v":1,"runner_allow_write":["/home/alice"]}`:                "home directory",
		`{"v":1,"runner_allow_write":["/Users/bob/"]}`:                "home directory",
		`{"v":1,"runner_allow_write":["/home/x/.."]}`:                 "ancestors",
		`{"v":1,"runner_allow_write":["/etc/claude-code/ballast"]}`:   "org directory",
		`{"v":1,"runner_allow_write":["/etc/claude-code/ballast/x"]}`: "org directory",
		`{"v":1,"runner_allow_write":["~/../.."]}`:                    "escapes",
		`{"v":1,"home_root":["home"]}`:                                "absolute",
		`{"v":1,"home_root":["/"]}`:                                   "/",
		`{"v":1,"home_root":["/home/"]}`:                              "clean",
		`{"v":1,"home_root":["/home/../etc"]}`:                        "clean",
		`{"v":1,"home_root":["~/x"]}`:                                 "absolute",
		`{"v":1,"home_root":["/home/*"]}`:                             "glob",
		`{"v":1,"home_root":["/home/{a,b}"]}`:                         "glob",
		`{"v":1,"home_root":["/home/a~b"]}`:                           "glob",
	}
	for s, want := range bad {
		m := newMemFS()
		m.writeFile(p, s, 0, 0o644)
		_, err := LoadOrgConfig(m, p)
		if err == nil {
			t.Errorf("accepted %q", s)
			continue
		}
		if want != "" && !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %q lacks %q", s, err, want)
		}
	}
	if _, err := LoadOrgConfig(newMemFS(), p); err == nil {
		t.Error("absent file accepted")
	}
}

func TestLoadOrgConfigDecodesFields(t *testing.T) {
	const p = "/etc/claude-code/ballast/org.json"
	m := newMemFS()
	m.writeFile(p, `{"v":1,"tier":"T1","runner_sandbox":"prefer","runner_network":"loopback","weakened_project":"audit","runner_allow_write":["/srv/c"],"home_root":["/home"],"protected_extra":["/srv/p"]}`, 0, 0o644)
	o, err := LoadOrgConfig(m, p)
	if err != nil {
		t.Fatal(err)
	}
	if o.V != 1 || o.Tier != "T1" || o.RunnerSandbox != "prefer" || o.RunnerNetwork != "loopback" || o.WeakenedProject != "audit" ||
		len(o.RunnerAllowWrite) != 1 || len(o.HomeRoot) != 1 || len(o.ProtectedExtra) != 1 {
		t.Errorf("decoded = %+v", o)
	}
}

func FuzzLoadOrgConfig(f *testing.F) {
	for _, s := range []string{`{"v":1}`, `{"v":1,"home_root":["/home"]}`, `[`, `{"v":1,"runner_allow_write":["/home/a"]}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		o, err := parseOrgConfig(b, "/etc/claude-code/ballast")
		if err != nil {
			return
		}
		// Accepted configs satisfy the documented invariants.
		for _, e := range o.HomeRoot {
			if e == "/" || !strings.HasPrefix(e, "/") || strings.ContainsAny(e, "~*?[]{}") {
				t.Fatalf("accepted home_root %q", e)
			}
		}
		for _, e := range o.RunnerAllowWrite {
			if e == "/" || e == "~" || e == "~/" || e == "/home" || e == "/root" {
				t.Fatalf("accepted runner_allow_write %q", e)
			}
		}
	})
}
