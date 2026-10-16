package redact

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

var update = flag.Bool("update", false, "rewrite the \"want\" fields of testdata/samples.json")

type positiveSample struct {
	Name    string   `json:"name"`
	Kind    Kind     `json:"kind"`
	In      string   `json:"in"`
	Secrets []string `json:"secrets"`
	Want    string   `json:"want"`
}

type negativeSample struct {
	Name string `json:"name"`
	In   string `json:"in"`
}

type sampleFile struct {
	Comment  string              `json:"comment"`
	Parts    map[string][]string `json:"parts"`
	Positive []positiveSample    `json:"positive"`
	Negative []negativeSample    `json:"negative"`
}

const samplesPath = "testdata/samples.json"

func loadSamples(t testing.TB) *sampleFile {
	t.Helper()
	raw, err := os.ReadFile(samplesPath)
	if err != nil {
		t.Fatal(err)
	}
	var f sampleFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("%s: %v", samplesPath, err)
	}
	return &f
}

// expand substitutes {{NAME}} with the joined fragments of Parts[NAME], so
// the repository never stores a token-shaped literal.
func (f *sampleFile) expand(s string) string {
	for name, frags := range f.Parts {
		s = strings.ReplaceAll(s, "{{"+name+"}}", strings.Join(frags, ""))
	}
	return s
}

// unexpand turns the PEM delimiters back into placeholders for -update.
func (f *sampleFile) unexpand(s string) string {
	for _, name := range []string{"B", "E"} {
		s = strings.ReplaceAll(s, strings.Join(f.Parts[name], ""), "{{"+name+"}}")
	}
	return s
}

func TestSamples(t *testing.T) {
	f := loadSamples(t)
	if len(f.Positive) < 50 {
		t.Fatalf("only %d positive samples; the contract asks for 50+", len(f.Positive))
	}
	if len(f.Negative) < 20 {
		t.Fatalf("only %d negative samples; the contract asks for 20+", len(f.Negative))
	}
	perKind := map[Kind]int{}
	for _, p := range f.Positive {
		perKind[p.Kind]++
	}
	for _, k := range []Kind{KindPrivateKey, KindURLPassword, KindAuthHeader, KindBearer, KindKeyValue, KindFlag, KindAWS, KindVendorToken, KindJWT} {
		if perKind[k] < 3 {
			t.Errorf("kind %s has %d samples, want ≥ 3", k, perKind[k])
		}
	}

	if *update {
		for i := range f.Positive {
			f.Positive[i].Want = f.unexpand(String(f.expand(f.Positive[i].In)))
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", " ")
		if err := enc.Encode(f); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(samplesPath, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s", samplesPath)
	}

	for _, p := range f.Positive {
		t.Run("pos/"+p.Name, func(t *testing.T) {
			in, want := f.expand(p.In), f.expand(p.Want)
			got := String(in)
			if got != want {
				t.Errorf("String mismatch\n   in: %q\n  got: %q\n want: %q", in, got, want)
			}
			if len(p.Secrets) == 0 {
				t.Fatal("sample plants no secret")
			}
			for _, sec := range p.Secrets {
				sec = f.expand(sec)
				if !strings.Contains(in, sec) {
					t.Fatalf("planted secret %q is not in the input", sec)
				}
				if strings.Contains(got, sec) {
					t.Errorf("secret %q leaked: %q", sec, got)
				}
			}
			if !strings.Contains(got, Mask) {
				t.Errorf("no mask in %q", got)
			}
			if again := String(got); again != got {
				t.Errorf("not idempotent\n  once: %q\n twice: %q", got, again)
			}
			if !utf8.ValidString(got) {
				t.Errorf("invalid UTF-8 produced: %q", got)
			}
			if !MayContainSecret(in) {
				t.Error("MayContainSecret is false for a positive sample")
			}
			ms := Find(in)
			if len(ms) == 0 {
				t.Fatal("Find found nothing")
			}
			var kinds []Kind
			for _, m := range ms {
				kinds = append(kinds, m.Kind)
			}
			ok := false
			for _, k := range kinds {
				ok = ok || k == p.Kind
			}
			if !ok {
				t.Errorf("Find kinds %v do not include %s", kinds, p.Kind)
			}
		})
	}
	for _, n := range f.Negative {
		t.Run("neg/"+n.Name, func(t *testing.T) {
			in := f.expand(n.In)
			if got := String(in); got != in {
				t.Errorf("changed a secret-free input\n  in: %q\n out: %q", in, got)
			}
			if len(Find(in)) != 0 {
				t.Errorf("Find reported matches on a secret-free input: %v", Find(in))
			}
		})
	}
}
