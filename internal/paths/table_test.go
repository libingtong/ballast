package paths

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

type table struct {
	Normalize []struct {
		GOOS string `json:"goos"`
		Base string `json:"base"`
		Home string `json:"home"`
		In   string `json:"in"`
		Want string `json:"want"`
	} `json:"normalize"`
	Equal []struct {
		GOOS string `json:"goos"`
		A    string `json:"a"`
		B    string `json:"b"`
		Want bool   `json:"want"`
	} `json:"equal"`
	Rel []struct {
		GOOS string `json:"goos"`
		Root string `json:"root"`
		P    string `json:"p"`
		Want string `json:"want"`
		OK   bool   `json:"ok"`
	} `json:"rel"`
	Globs []struct {
		Patterns []string `json:"patterns"`
		CI       bool     `json:"ci"`
		Home     string   `json:"home"`
		Cand     []string `json:"cand"`
		Want     bool     `json:"want"`
		Error    string   `json:"error"`
	} `json:"globs"`
}

func loadTable(t *testing.T, name string) table {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var tb table
	if err := json.Unmarshal(data, &tb); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return tb
}

var tableFiles = []string{"normalize.json", "normalize_more.json"}

func TestNormalizeTable(t *testing.T) {
	for _, f := range tableFiles {
		tb := loadTable(t, f)
		if len(tb.Normalize) == 0 {
			t.Fatalf("%s: no normalize rows", f)
		}
		for _, r := range tb.Normalize {
			ctx := model.PathContext{GOOS: r.GOOS, Home: r.Home, Cwd: r.Base}
			got := Normalize(r.In, r.Base, ctx)
			if got != r.Want {
				t.Errorf("%s: Normalize(%q, base=%q, home=%q, %s) = %q, want %q", f, r.In, r.Base, r.Home, r.GOOS, got, r.Want)
			}
			if again := Normalize(got, r.Base, ctx); again != got {
				t.Errorf("%s: not idempotent for %q: %q then %q", f, r.In, got, again)
			}
			if again := Normalize(got, "", ctx); again != got {
				t.Errorf("%s: not stable without base for %q: %q then %q", f, r.In, got, again)
			}
			if s := NewService(nil).Normalize(r.In, r.Base, ctx); s != got {
				t.Errorf("%s: Service.Normalize(%q) = %q, want %q", f, r.In, s, got)
			}
		}
	}
}

func TestEqualTable(t *testing.T) {
	for _, f := range tableFiles {
		for _, r := range loadTable(t, f).Equal {
			if got := Equal(r.A, r.B, r.GOOS); got != r.Want {
				t.Errorf("%s: Equal(%q, %q, %q) = %v, want %v", f, r.A, r.B, r.GOOS, got, r.Want)
			}
			if got := Equal(r.B, r.A, r.GOOS); got != r.Want {
				t.Errorf("%s: Equal not symmetric for %q, %q", f, r.A, r.B)
			}
		}
	}
}

func TestRelTable(t *testing.T) {
	for _, f := range tableFiles {
		for _, r := range loadTable(t, f).Rel {
			got, ok := Rel(r.Root, r.P, r.GOOS)
			if got != r.Want || ok != r.OK {
				t.Errorf("%s: Rel(%q, %q, %q) = (%q, %v), want (%q, %v)", f, r.Root, r.P, r.GOOS, got, ok, r.Want, r.OK)
			}
			if w := Within(r.P, r.Root, r.GOOS); w != r.OK {
				t.Errorf("%s: Within(%q, %q, %q) = %v, want %v", f, r.P, r.Root, r.GOOS, w, r.OK)
			}
		}
	}
}

func TestGlobTable(t *testing.T) {
	for _, f := range tableFiles {
		for _, r := range loadTable(t, f).Globs {
			g, err := CompileGlobs(r.Patterns, GlobOptions{Home: r.Home, CaseInsensitive: r.CI})
			if r.Error != "" {
				want := map[string]error{
					"only-negation": ErrOnlyNegation,
					"bad-pattern":   ErrBadPattern,
					"empty":         ErrEmptyPatternList,
				}[r.Error]
				if want == nil {
					t.Fatalf("%s: unknown error kind %q", f, r.Error)
				}
				if !errors.Is(err, want) || g != nil {
					t.Errorf("%s: CompileGlobs(%q) = (%v, %v), want error %v", f, r.Patterns, g, err, want)
				}
				continue
			}
			if err != nil {
				t.Errorf("%s: CompileGlobs(%q): %v", f, r.Patterns, err)
				continue
			}
			if got := g.Match(r.Cand...); got != r.Want {
				t.Errorf("%s: %q ci=%v home=%q Match(%q) = %v, want %v", f, r.Patterns, r.CI, r.Home, r.Cand, got, r.Want)
			}
			if got := g.Patterns(); len(got) != len(r.Patterns) {
				t.Errorf("%s: Patterns() = %q", f, got)
			}
		}
	}
}
