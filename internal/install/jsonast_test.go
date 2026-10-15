package install

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseJSONDocRoundTripsUnmodified(t *testing.T) {
	docs := []string{
		"{}",
		"{}\n",
		"{\"a\":1}",
		"{ \"a\" : [ 1 , 2.5e3 , -0 , true , false , null , \"x\\u00e9\\n\" ] }\n\n",
		"\xef\xbb\xbf{\n\t\"a\": {\"b\": []}\r\n}\r\n",
		"  \n{\n    \"k\": \"v\"\n}   \n",
		`[1,2,3]`,
		`"just a string"`,
	}
	for _, d := range docs {
		doc, err := parseJSONDoc([]byte(d))
		if err != nil {
			t.Errorf("%q: %v", d, err)
			continue
		}
		if got := string(doc.bytes()); got != d {
			t.Errorf("round trip changed %q -> %q", d, got)
		}
	}
}

func TestParseJSONDocErrors(t *testing.T) {
	bad := []string{
		`{`, `{"a"}`, `{"a":}`, `{"a":1,}`, `{a:1}`, `[1,]`, `{"a":01}`, `{"a":1.}`, `{"a":-}`,
		`{"a":"\x"}`, `{"a":"\u12"}`, "{\"a\":\"line\nbreak\"}", `{"a":tru}`, `{} x`, `{"a":1} {"b":2}`,
		`{"a":"unterminated}`, `// c` + "\n{}", `{"a":1 "b":2}`, strings.Repeat("[", jMaxDepth+2),
	}
	for _, d := range bad {
		if _, err := parseJSONDoc([]byte(d)); err == nil {
			t.Errorf("%q should not parse", d)
		}
	}
}

func TestBlankDocIsEmptyObject(t *testing.T) {
	for _, d := range []string{"", "  \n", "\xef\xbb\xbf"} {
		doc, err := parseJSONDoc([]byte(d))
		if err != nil || doc.root.kind != jObject || len(doc.root.members) != 0 {
			t.Errorf("%q: %v", d, err)
		}
	}
}

func TestSerializationPreservesStyle(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"two spaces", "{\n  \"a\": 1\n}\n", "{\n  \"a\": 1,\n  \"n\": {\n    \"x\": [\n      1\n    ]\n  }\n}\n"},
		{"four spaces", "{\n    \"a\": 1\n}\n", "{\n    \"a\": 1,\n    \"n\": {\n        \"x\": [\n            1\n        ]\n    }\n}\n"},
		{"tabs", "{\n\t\"a\": 1\n}", "{\n\t\"a\": 1,\n\t\"n\": {\n\t\t\"x\": [\n\t\t\t1\n\t\t]\n\t}\n}"},
		{"compact", `{"a":1}`, `{"a":1,"n":{"x":[1]}}`},
		{"inline spaced", `{ "a": 1 }`, `{"a": 1, "n": {"x": [1]}}`},
		{"crlf", "{\r\n  \"a\": 1\r\n}\r\n", "{\r\n  \"a\": 1,\r\n  \"n\": {\r\n    \"x\": [\r\n      1\r\n    ]\r\n  }\r\n}\r\n"},
		{"empty object grows multi-line", "{}\n", "{\n  \"n\": {\n    \"x\": [\n      1\n    ]\n  }\n}\n"},
	}
	for _, c := range cases {
		doc, err := parseJSONDoc([]byte(c.in))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		inner := newObject()
		inner.set("x", arrOf(newInt(1)))
		doc.root.set("n", inner)
		if got := string(doc.bytes()); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
		if err := json.Unmarshal(doc.bytes(), new(any)); err != nil {
			t.Errorf("%s: output is not valid JSON: %v", c.name, err)
		}
	}
}

func TestUntouchedChildrenKeepTheirBytes(t *testing.T) {
	in := "{\n  \"keep\": {  \"odd\":   [1,2 ,3]  },\n  \"x\": 1\n}\n"
	doc, _ := parseJSONDoc([]byte(in))
	doc.root.set("y", newInt(2))
	want := "{\n  \"keep\": {  \"odd\":   [1,2 ,3]  },\n  \"x\": 1,\n  \"y\": 2\n}\n"
	if got := string(doc.bytes()); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestNodeMutators(t *testing.T) {
	doc, _ := parseJSONDoc([]byte(`{"a":1,"b":[1,2,3],"a":9}`))
	r := doc.root
	if r.count("a") != 2 || r.get("a").raw[0] != '9' {
		t.Error("duplicate keys: get must return the last value, count 2")
	}
	r.del("a")
	if r.count("a") != 0 {
		t.Error("del removes all occurrences")
	}
	b := r.get("b")
	b.removeItem(1)
	b.replaceItem(0, newString("x<&>"))
	b.appendItem(newInt(7))
	if got := string(doc.bytes()); got != `{"b":["x<&>",3,7]}` {
		t.Errorf("got %s", got)
	}
	r.setFront("v", newInt(1))
	if got := string(doc.bytes()); !strings.HasPrefix(got, `{"v":1,`) {
		t.Errorf("got %s", got)
	}
	if s, ok := newString("é\"\n").str(); !ok || s != "é\"\n" {
		t.Errorf("str round trip: %q %v", s, ok)
	}
	if _, ok := newInt(1).str(); ok {
		t.Error("an int is not a string")
	}
	if l, ok := stringArray([]string{"a", "b"}).strList(); !ok || len(l) != 2 {
		t.Error("strList")
	}
	if _, ok := arrOf(newInt(1)).strList(); ok {
		t.Error("strList of ints must fail")
	}
}

func FuzzParseJSONDoc(f *testing.F) {
	for _, s := range []string{`{}`, `{"a":[1,{"b":null}],"c":"d"}`, "{\n  \"hooks\": {}\n}\n", `[`, `{"a":`, "\xef\xbb\xbf {}", `{"\u00e9":1e5}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		doc, err := parseJSONDoc(in)
		if err != nil {
			return
		}
		out := doc.bytes()
		if len(strings.TrimSpace(strings.TrimPrefix(string(in), "\ufeff"))) != 0 && string(out) != string(in) {
			t.Fatalf("unmodified document changed: %q -> %q", in, out)
		}
		// mutate and make sure the result still parses and encoding/json agrees
		if doc.root.kind == jObject {
			doc.root.set("zz", arrOf(newString("x")))
			out2 := doc.bytes()
			if !json.Valid([]byte(strings.TrimPrefix(string(out2), "\ufeff"))) {
				t.Fatalf("mutated output invalid: %q", out2)
			}
			if _, err := parseJSONDoc(out2); err != nil {
				t.Fatalf("mutated output does not reparse: %v", err)
			}
		}
	})
}
