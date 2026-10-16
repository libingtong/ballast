package tier

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

type weakenedRow struct {
	Name  string          `json:"name"`
	Doc   json.RawMessage `json:"doc"`
	Raw   *string         `json:"raw"`
	User  []string        `json:"user"`
	Repo  *[]string       `json:"repo"`
	Error bool            `json:"error"`
}

func (r weakenedRow) bytes() []byte {
	if r.Raw != nil {
		return []byte(*r.Raw)
	}
	return r.Doc
}

func TestWeakenedSandboxKeysTable(t *testing.T) {
	for _, r := range loadRows[weakenedRow](t, "weakened.json") {
		t.Run(r.Name, func(t *testing.T) {
			wantUser := r.User
			wantRepo := r.User
			if r.Repo != nil {
				wantRepo = *r.Repo
			}
			for _, tc := range []struct {
				layer SettingsLayer
				want  []string
			}{{LayerUser, wantUser}, {LayerRepo, wantRepo}} {
				got, err := WeakenedSandboxKeys(r.bytes(), tc.layer)
				if r.Error {
					if err == nil {
						t.Errorf("%s: want error, got %v", tc.layer, got)
					}
					continue
				}
				if err != nil {
					t.Errorf("%s: unexpected error %v", tc.layer, err)
					continue
				}
				if len(tc.want) == 0 && len(got) == 0 {
					continue
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Errorf("%s: got %v, want %v", tc.layer, got, tc.want)
				}
				if !sort.StringsAreSorted(got) {
					t.Errorf("%s: result not sorted: %v", tc.layer, got)
				}
			}
		})
	}
}

// The user-layer-only keys named by the contract.
func TestWeakenedUserOnlyKeysCoverContractList(t *testing.T) {
	want := []string{"env", "statusLine", "apiKeyHelper", "awsAuthRefresh", "awsCredentialExport", "otelHeadersHelper", "enabledPlugins", "extraKnownMarketplaces"}
	for _, k := range want {
		doc := []byte(`{"` + k + `":{"x":1}}`)
		u, _ := WeakenedSandboxKeys(doc, LayerUser)
		r, _ := WeakenedSandboxKeys(doc, LayerRepo)
		if len(u) != 0 || !reflect.DeepEqual(r, []string{k}) {
			t.Errorf("%s: user %v repo %v", k, u, r)
		}
	}
	for _, k := range []string{"model", "foo", "permissions"} {
		if harmlessTopUser(k) {
			t.Errorf("%q must not be user-layer-only harmless", k)
		}
	}
}

func FuzzWeakenedSandboxKeys(f *testing.F) {
	for _, s := range []string{`{}`, `{"sandbox":{"excludedCommands":["npm"]}}`, `{"permissions":"x"}`, `[`, `{"env":{"A":"b"}}`, `{"sandbox":{"a":{"b":[1]}}}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, layer := range []SettingsLayer{LayerUser, LayerRepo} {
			keys, err := WeakenedSandboxKeys(b, layer)
			if err != nil && keys != nil {
				t.Fatalf("error with keys %v", keys)
			}
			if !sort.StringsAreSorted(keys) {
				t.Fatalf("unsorted %v", keys)
			}
			for i := 1; i < len(keys); i++ {
				if keys[i] == keys[i-1] {
					t.Fatalf("duplicate %v", keys)
				}
			}
		}
		// The repo layer is never more permissive than the user layer.
		u, uerr := WeakenedSandboxKeys(b, LayerUser)
		r, rerr := WeakenedSandboxKeys(b, LayerRepo)
		if (uerr == nil) != (rerr == nil) {
			t.Fatalf("layers disagree on validity")
		}
		in := map[string]bool{}
		for _, k := range r {
			in[k] = true
		}
		for _, k := range u {
			if !in[k] {
				t.Fatalf("user layer reports %q that the repo layer does not", k)
			}
		}
	})
}

// --- RepoSandboxWeakened / ProjectChecker -----------------------------------

func repoFS(t testing.TB, files map[string]string) *memFS {
	t.Helper()
	m := newMemFS()
	m.mkdirAll("/work/repo", tUser, 0o755)
	for p, c := range files {
		m.writeFile("/work/repo/"+p, c, tUser, 0o644)
	}
	return m
}

func TestRepoSandboxWeakened(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		setup func(*memFS)
		want  []string
	}{
		{"no-claude-dir", nil, nil, nil},
		{"clean-project-layer", map[string]string{".claude/settings.json": `{"permissions":{"allow":["Bash(ls)"]},"model":"x"}`}, nil, nil},
		{"project-excluded-commands", map[string]string{".claude/settings.json": `{"sandbox":{"excludedCommands":["npm"]}}`}, nil, []string{"settings.json:sandbox.excludedCommands"}},
		{"mixed-case-settings-name", map[string]string{".claude/Settings.json": `{"sandbox":{"excludedCommands":["npm"]}}`}, nil, []string{"Settings.json:sandbox.excludedCommands"}},
		{"upper-case-local-name", map[string]string{".claude/SETTINGS.LOCAL.JSON": `{"sandbox":{"network":{"allowUnixSockets":["/s"]}}}`}, nil, []string{"SETTINGS.LOCAL.JSON:sandbox.network.allowUnixSockets"}},
		{"unicode-fold-settings-name", map[string]string{".claude/\u017fettings.json": `{"sandbox":{"excludedCommands":["npm"]}}`}, nil, []string{"\u017fettings.json:sandbox.excludedCommands"}},
		{"oversized-settings-unreadable", map[string]string{".claude/settings.json": `{"model":"` + strings.Repeat("x", maxSettingsBytes) + `"}`}, nil, []string{"settings.json:unreadable"}},
		{"mixed-case-unparsable", map[string]string{".claude/Settings.json": `{oops`}, nil, []string{"Settings.json:unreadable"}},
		{"too-short-name-ignored", map[string]string{".claude/settings.json.bak": `{"sandbox":{"excludedCommands":["npm"]}}`, ".claude/settingsjson": `x`}, nil, nil},
		{"local-allow-unix-sockets", map[string]string{".claude/settings.local.json": `{"sandbox":{"network":{"allowUnixSockets":["/var/run/docker.sock"]}}}`}, nil, []string{"settings.local.json:sandbox.network.allowUnixSockets"}},
		{"both-layers-sorted", map[string]string{
			".claude/settings.json":       `{"sandbox":{"excludedCommands":["npm"]}}`,
			".claude/settings.local.json": `{"sandbox":{"network":{"allowUnixSockets":["/s"]}}}`,
		}, nil, []string{"settings.json:sandbox.excludedCommands", "settings.local.json:sandbox.network.allowUnixSockets"}},
		{"repo-env-is-weakening", map[string]string{".claude/settings.json": `{"env":{"LD_PRELOAD":"/x.so"}}`}, nil, []string{"settings.json:env"}},
		{"repo-api-key-helper", map[string]string{".claude/settings.json": `{"apiKeyHelper":"/x"}`}, nil, []string{"settings.json:apiKeyHelper"}},
		{"unparsable", map[string]string{".claude/settings.json": `{oops`}, nil, []string{"settings.json:unreadable"}},
		{"not-an-object", map[string]string{".claude/settings.local.json": `[]`}, nil, []string{"settings.local.json:unreadable"}},
		{"unrelated-file-ignored", map[string]string{".claude/other.json": `{"sandbox":{"excludedCommands":["npm"]}}`, ".claude/settings.txt": `x`}, nil, nil},
		{"symlinked-claude-dir", nil, func(m *memFS) {
			m.mkdirAll("/elsewhere", tUser, 0o755)
			m.writeFile("/elsewhere/settings.json", `{"sandbox":{"excludedCommands":["npm"]}}`, tUser, 0o644)
			m.symlink("/work/repo/.claude", "/elsewhere", tUser)
		}, []string{".claude:symlink"}},
		{"symlinked-settings-file", nil, func(m *memFS) {
			m.mkdirAll("/work/repo/.claude", tUser, 0o755)
			m.writeFile("/elsewhere.json", `{}`, tUser, 0o644)
			m.symlink("/work/repo/.claude/settings.json", "/elsewhere.json", tUser)
		}, []string{"settings.json:symlink"}},
		{"settings-json-is-a-directory-ignored", nil, func(m *memFS) {
			m.mkdirAll("/work/repo/.claude/settings.json", tUser, 0o755)
		}, nil},
		{"claude-is-a-regular-file", nil, func(m *memFS) {
			m.writeFile("/work/repo/.claude", "x", tUser, 0o644)
		}, []string{".claude:unreadable"}},
		{"empty-claude-dir", nil, func(m *memFS) { m.mkdirAll("/work/repo/.claude", tUser, 0o755) }, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := repoFS(t, tc.files)
			if tc.setup != nil {
				tc.setup(m)
			}
			got := RepoSandboxWeakened(m, "/work/repo")
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
			// ProjectChecker.Weakened == RepoSandboxWeakened.
			if pc := (ProjectChecker{FS: m}).Weakened("/work/repo"); !reflect.DeepEqual(pc, got) {
				t.Errorf("ProjectChecker.Weakened = %v, RepoSandboxWeakened = %v", pc, got)
			}
		})
	}
}

func TestRepoSandboxWeakenedAbsentCostsOneLstat(t *testing.T) {
	m := repoFS(t, nil)
	var ops []string
	m.hook = func(op, name string) { ops = append(ops, op) }
	if got := RepoSandboxWeakened(m, "/work/repo"); got != nil {
		t.Fatalf("got %v", got)
	}
	if !reflect.DeepEqual(ops, []string{"Lstat"}) {
		t.Errorf("I/O = %v, want one Lstat", ops)
	}
}

func TestRepoSandboxWeakenedReadError(t *testing.T) {
	m := repoFS(t, map[string]string{".claude/settings.json": `{}`})
	f := &failReadFS{memFS: m, fail: "settings.json"}
	if got := RepoSandboxWeakened(f, "/work/repo"); !reflect.DeepEqual(got, []string{"settings.json:unreadable"}) {
		t.Errorf("got %v", got)
	}
}

func TestProjectCheckerImplementsPort(t *testing.T) {
	var _ interface{ Weakened(dir string) []string } = ProjectChecker{}
}

func TestEffectiveWeakenedProject(t *testing.T) {
	if got := EffectiveWeakenedProject(OrgConfig{}); got != DefaultWeakenedProject {
		t.Errorf("empty ⇒ %q, want DefaultWeakenedProject %q", got, DefaultWeakenedProject)
	}
	for _, v := range []model.WeakenedProjectPolicy{model.WeakenedProjectAudit, model.WeakenedProjectDeny} {
		if got := EffectiveWeakenedProject(OrgConfig{WeakenedProject: v}); got != v {
			t.Errorf("explicit %q ⇒ %q", v, got)
		}
	}
}

// The constant's value is finalized by P2-CLI (M0b conclusion, asserted by the
// cli unit test); this package only requires that it is a valid policy and
// that an org.json without weakened_project resolves to it.
func TestDefaultWeakenedProjectIsValidAndUsedAsFallback(t *testing.T) {
	switch DefaultWeakenedProject {
	case model.WeakenedProjectAudit, model.WeakenedProjectDeny:
	default:
		t.Fatalf("DefaultWeakenedProject = %q, want audit or deny", DefaultWeakenedProject)
	}
	if got := EffectiveWeakenedProject(OrgConfig{}); got != DefaultWeakenedProject {
		t.Errorf("EffectiveWeakenedProject(OrgConfig{}) = %q, want %q", got, DefaultWeakenedProject)
	}
}
