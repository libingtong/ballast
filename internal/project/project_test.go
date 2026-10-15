package project

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

type invalidRow struct {
	ID   string `json:"id"`
	Why  string `json:"why"`
	JSON string `json:"json"`
}

func loadInvalid(t *testing.T) []invalidRow {
	t.Helper()
	data, err := os.ReadFile("testdata/invalid.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Rows []invalidRow `json:"rows"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Rows) < 30 {
		t.Fatalf("invalid.json has only %d rows", len(f.Rows))
	}
	return f.Rows
}

func TestInvalidTable(t *testing.T) {
	rows := loadInvalid(t)
	dir := t.TempDir()
	ids := map[string]bool{}
	for _, r := range rows {
		if ids[r.ID] {
			t.Errorf("duplicate id %s", r.ID)
		}
		ids[r.ID] = true
		t.Run(r.ID, func(t *testing.T) {
			if cfg, err := Decode([]byte(r.JSON)); err == nil {
				t.Fatalf("Decode accepted (%s): %+v", r.Why, cfg)
			}
			root := filepath.Join(dir, r.ID)
			if err := os.MkdirAll(filepath.Join(root, ".ballast"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".ballast", "project.json"), []byte(r.JSON), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := (&Loader{FS: sys.OSFS{}}).LoadProject(root)
			if err == nil || cfg != nil {
				t.Fatalf("LoadProject = %+v, %v; want nil, error", cfg, err)
			}
			if !strings.Contains(err.Error(), "project.json") {
				t.Errorf("error does not name the file: %v", err)
			}
		})
	}
}

func TestDecodeValid(t *testing.T) {
	long := strings.Repeat("a", 32)
	tests := []struct {
		name string
		in   string
		want model.ProjectConfig
	}{
		{"empty object", `{}`, model.ProjectConfig{}},
		{"empty verify", `{"version":1,"verify":[]}`, model.ProjectConfig{Version: 1}},
		{"null verify", `{"verify":null}`, model.ProjectConfig{}},
		{"minimal", `{"verify":[{"name":"test","argv":["go","test","./..."]}]}`,
			model.ProjectConfig{Verify: []model.VerifySpec{{Name: "test", Argv: []string{"go", "test", "./..."}}}}},
		{"full", `{"version":1,"verify":[{"name":"t_1-x","argv":["a"],"timeout_s":86400,"required":true,"env":["A=","B=c=d"],"cwd":"sub/dir"},
			{"name":"` + long + `","argv":["b","--flag"],"timeout_s":1,"cwd":"."}],"exempt":["**/*.md","docs/**","{a,b}/*.txt","[ab]c"]}`,
			model.ProjectConfig{Version: 1,
				Verify: []model.VerifySpec{
					{Name: "t_1-x", Argv: []string{"a"}, TimeoutS: 86400, Required: true, Env: []string{"A=", "B=c=d"}, Cwd: "sub/dir"},
					{Name: long, Argv: []string{"b", "--flag"}, TimeoutS: 1, Cwd: "."},
				},
				Exempt: []string{"**/*.md", "docs/**", "{a,b}/*.txt", "[ab]c"}}},
		{"digit first name", `{"verify":[{"name":"9lives","argv":["x"]}]}`,
			model.ProjectConfig{Verify: []model.VerifySpec{{Name: "9lives", Argv: []string{"x"}}}}},
		{"whitespace and newline", "  \n{\"verify\":[]}\n\n", model.ProjectConfig{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Decode([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*got, tc.want) {
				t.Errorf("got %+v\nwant %+v", *got, tc.want)
			}
		})
	}
}

func TestRequired(t *testing.T) {
	cfg, err := Decode([]byte(`{"verify":[{"name":"test","argv":["a"],"required":true},{"name":"lint","argv":["b"]},{"name":"e2e","argv":["c"],"required":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Required(); !reflect.DeepEqual(got, []string{"test", "e2e"}) {
		t.Errorf("Required = %v", got)
	}
}

func TestLoaderMissingFile(t *testing.T) {
	cfg, err := (&Loader{FS: sys.OSFS{}}).LoadProject(t.TempDir())
	if cfg != nil || err != nil {
		t.Fatalf("missing file: got %+v, %v; want nil, nil", cfg, err)
	}
	// A nil FS means the real one.
	cfg, err = (&Loader{}).LoadProject(t.TempDir())
	if cfg != nil || err != nil {
		t.Fatalf("nil FS, missing file: got %+v, %v", cfg, err)
	}
}

func TestLoaderReadError(t *testing.T) {
	root := t.TempDir()
	// project.json is a directory: not "missing", an error.
	if err := os.MkdirAll(filepath.Join(root, ".ballast", "project.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := (&Loader{}).LoadProject(root)
	if err == nil || cfg != nil {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}

func TestLoaderBytesAndHash(t *testing.T) {
	root := t.TempDir()
	body := []byte(`{"verify":[{"name":"test","argv":["go","test"],"required":true}]}`)
	if err := os.MkdirAll(filepath.Join(root, ".ballast"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ballast", "project.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, data, err := (&Loader{}).LoadProjectBytes(root)
	if err != nil || cfg == nil || string(data) != string(body) {
		t.Fatalf("LoadProjectBytes = %+v, %q, %v", cfg, data, err)
	}
	// sha256("") is a well-known constant.
	if got := Hash(nil); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("Hash(nil) = %s", got)
	}
	if h := Hash(body); len(h) != 64 || h == Hash(append(body, ' ')) {
		t.Errorf("Hash not content-sensitive: %s", h)
	}
}

func TestLoaderFaultFS(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".ballast"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ballast", "project.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	l := &Loader{FS: sys.FaultFS{FS: sys.OSFS{}, Fail: map[string]string{"ReadFile": ""}, Err: boom}}
	if _, err := l.LoadProject(root); !errors.Is(err, boom) {
		t.Fatalf("err = %v; want wrapping boom", err)
	}
}

func FuzzDecode(f *testing.F) {
	for _, s := range []string{
		`{}`, `{"verify":[{"name":"test","argv":["go","test"],"required":true}],"exempt":["docs/**"]}`,
		`{"version":1}`, `[]`, `{"verify":[{"name":"","argv":[]}]}`, `{"exempt":["[","{"]}`, "\x00", `{"verify":[{"name":"a","argv":["x"],"cwd":"../"}]}`,
	} {
		f.Add([]byte(s))
	}
	if rows, err := os.ReadFile("testdata/invalid.json"); err == nil {
		var raw struct {
			Rows []struct {
				JSON string `json:"json"`
			} `json:"rows"`
		}
		if json.Unmarshal(rows, &raw) == nil {
			for _, r := range raw.Rows {
				f.Add([]byte(r.JSON))
			}
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, err := Decode(data)
		if err != nil {
			if cfg != nil {
				t.Fatal("non-nil config with error")
			}
			return
		}
		// Whatever is accepted satisfies the invariants.
		seen := map[string]bool{}
		for _, v := range cfg.Verify {
			if validateName(v.Name) != nil || seen[v.Name] || len(v.Argv) == 0 || v.Argv[0] == "" || validateCwd(v.Cwd) != nil {
				t.Fatalf("accepted invalid entry %+v", v)
			}
			seen[v.Name] = true
			if v.TimeoutS != 0 && (v.TimeoutS < 1 || v.TimeoutS > MaxTimeoutS) {
				t.Fatalf("timeout %d", v.TimeoutS)
			}
		}
		// Re-encoding and decoding again is stable.
		out, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		again, err := Decode(out)
		if err != nil {
			t.Fatalf("re-decode %s: %v", out, err)
		}
		if !reflect.DeepEqual(cfg, again) {
			t.Fatalf("round trip differs: %+v vs %+v", cfg, again)
		}
	})
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
