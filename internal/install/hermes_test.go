package install

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func hermesRoundTrip(t *testing.T, orig string) (installed string, removed *PlanResult) {
	t.Helper()
	tg := gHermesT()
	var cur []byte
	if orig != absent {
		cur = []byte(orig)
	}
	p, err := tg.Plan(cur, gspec(model.ClientHermes), nil)
	if err != nil {
		t.Fatalf("Plan(%q): %v", orig, err)
	}
	r, err := tg.Remove(p.After, manifestFrom(gBinary, p))
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	return string(p.After), r
}

func TestHermesRoundTrip(t *testing.T) {
	cases := map[string]string{
		"plain":                  "model: x\nterminal:\n  a: 1\n",
		"trailing blank":         "model: x\n\n",
		"comments":               "# top\nmodel: x # inline\n\n# end\n",
		"crlf":                   "model: x\r\nterminal:\r\n  a: 1\r\n",
		"empty file":             "",
		"hooks sibling name":     "hooks_auto_accept: true\nhooks_x:\n  a: 1\n",
		"user hooks":             "hooks:\n  pre_tool_call:\n    - command: \"/bin/guard\"\n      timeout: 5\n",
		"user hooks other event": "model: x\nhooks:\n  on_session_end:\n    - command: /bin/bye\nlogging:\n  level: info\n",
		"same-indent items":      "hooks:\n  pre_tool_call:\n  - command: /bin/a\n  - command: /bin/b\n    timeout: 3\n",
		"4-space indent":         "hooks:\n    pre_tool_call:\n        - command: /bin/a\n",
		"comments in hooks":      "hooks:\n  # my hooks\n  pre_tool_call:\n    # first\n    - command: /bin/a\n",
		"block scalar mention":   "terminal:\n  shell: |\n    hooks:\n      pretend\nmodel: x\n",
		"anchors elsewhere":      "base: &b\n  x: 1\nother:\n  <<: *b\n",
		"multi-line string":      "prompt: >\n  line one\n  line two\nmodel: x\n",
	}
	for name, orig := range cases {
		installed, r := hermesRoundTrip(t, orig)
		if !strings.Contains(installed, "hooks:") {
			t.Errorf("%s: nothing installed", name)
		}
		if r.After == nil || string(r.After) != orig {
			if orig == "" && r.After == nil {
				continue
			}
			t.Errorf("%s: round trip changed the file\n--- want\n%q\n--- got\n%q", name, orig, r.After)
		}
		// everything outside the hooks block is byte-identical after install
		if !strings.Contains(strings.ReplaceAll(installed, "\r\n", "\n"), strings.TrimRight(strings.ReplaceAll(orig, "\r\n", "\n"), "\n")) && !strings.HasPrefix(orig, "hooks:") && !strings.Contains(orig, "\nhooks:") {
			t.Errorf("%s: original content not preserved verbatim in\n%s", name, installed)
		}
	}
}

func TestHermesFilesWithoutTrailingNewline(t *testing.T) {
	for _, orig := range []string{"model: x", "hooks:\n  pre_tool_call:\n    - command: a", "hooks:", "hooks:\n  # c", "a: 1\nhooks:\n  x: []"} {
		p, err := gHermesT().Plan([]byte(orig), gspec(model.ClientHermes), nil)
		if err != nil {
			t.Fatalf("%q: %v", orig, err)
		}
		if _, err := parseYAMLDoc(p.After).section(); err != nil {
			t.Errorf("%q: output unusable: %v\n%s", orig, err, p.After)
		}
		if !strings.HasPrefix(string(p.After), strings.SplitN(orig, "\n", 2)[0]+"\n") {
			t.Errorf("%q: first line damaged:\n%s", orig, p.After)
		}
		if !strings.HasSuffix(string(p.After), "\n") {
			t.Errorf("%q: result lacks a final newline", orig)
		}
	}
}

func TestHermesAbsentFileIsDeleted(t *testing.T) {
	_, r := hermesRoundTrip(t, absent)
	if r.After != nil || !r.Changed() {
		t.Fatalf("created file must be deleted: %q", r.After)
	}
}

func TestHermesUserItemsSurvive(t *testing.T) {
	orig := "hooks:\n  pre_tool_call:\n    - command: \"/bin/guard\"\n      timeout: 5\n  on_session_end:\n  - command: /bin/bye\n"
	installed, r := hermesRoundTrip(t, orig)
	if !strings.Contains(installed, "/bin/guard") || !strings.Contains(installed, "/bin/bye") {
		t.Error("user items lost on install")
	}
	if string(r.After) != orig {
		t.Errorf("got %q", r.After)
	}
	// the user's items stay first, ours appended to the event list
	if i, j := strings.Index(installed, "/bin/guard"), strings.Index(installed, "ballast hook --client hermes --event pre_tool_call"); i > j {
		t.Error("our item must be appended after the user's")
	}
}

func TestHermesInlineEmptyHooks(t *testing.T) {
	for _, v := range []string{"hooks: {}", "hooks: []", "hooks: null", "hooks: ~", "hooks: {} # none"} {
		orig := "model: x\n" + v + "\nlogging: 1\n"
		installed, r := hermesRoundTrip(t, orig)
		if strings.Contains(installed, "{}") || !strings.Contains(installed, "hooks:\n  on_session_start:") {
			t.Errorf("%q: not converted to a block:\n%s", v, installed)
		}
		if string(r.After) != "model: x\nlogging: 1\n" {
			t.Errorf("%q: uninstall left %q", v, r.After)
		}
	}
}

func TestHermesEmptyEventFlowList(t *testing.T) {
	orig := "hooks:\n  pre_tool_call: []\n  stop: []\n"
	p, err := gHermesT().Plan([]byte(orig), gspec(model.ClientHermes), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(p.After), "[]") && strings.Contains(string(p.After), "pre_tool_call: []") {
		t.Errorf("pre_tool_call: [] must become a block list:\n%s", p.After)
	}
	if !strings.Contains(string(p.After), "stop: []") {
		t.Errorf("unrelated empty list changed:\n%s", p.After)
	}
	r, _ := gHermesT().Remove(p.After, manifestFrom(gBinary, p))
	if !strings.Contains(string(r.After), "stop: []") || strings.Contains(string(r.After), "ballast") {
		t.Errorf("after uninstall:\n%s", r.After)
	}
}

func TestHermesIdempotentAndInPlace(t *testing.T) {
	tg := gHermesT()
	spec := gspec(model.ClientHermes)
	p1, _ := tg.Plan([]byte("hooks:\n  pre_tool_call:\n    - command: /bin/first\n"), spec, nil)
	m := manifestFrom(gBinary, p1)
	p2, err := tg.Plan(p1.After, spec, m)
	if err != nil || string(p2.After) != string(p1.After) {
		t.Fatalf("not idempotent: %v", err)
	}
	// new binary path: replaced where it is, the user item before it stays first
	spec2 := spec
	spec2.Binary = "/opt/ballast v2/bin/ballast"
	p3, err := tg.Plan(p1.After, spec2, m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(p3.After)
	if strings.Contains(s, "/opt/ballast/bin/ballast") {
		t.Errorf("old binary still present:\n%s", s)
	}
	if !strings.Contains(s, `'/opt/ballast v2/bin/ballast' hook --client hermes --event pre_tool_call`) {
		t.Errorf("new quoted command missing:\n%s", s)
	}
	if strings.Index(s, "/bin/first") > strings.Index(s, "ballast v2") {
		t.Error("order changed")
	}
	// the new entry is recognised on the next run (hash of the quoted command round-trips)
	p4, err := tg.Plan(p3.After, spec2, manifestFrom(spec2.Binary, p3))
	if err != nil || string(p4.After) != s || len(p4.Warnings) != 0 {
		t.Errorf("v2 not idempotent: %v %v", err, p4.Warnings)
	}
	for _, e := range p3.Entries {
		if e.Native == "pre_tool_call" && e.Key != "hooks.pre_tool_call[1]" {
			t.Errorf("key = %s", e.Key)
		}
	}
}

func TestHermesForeignModified(t *testing.T) {
	tg := gHermesT()
	spec := gspec(model.ClientHermes)
	p1, _ := tg.Plan(nil, spec, nil)
	m := manifestFrom(gBinary, p1)
	edited := strings.Replace(string(p1.After), "timeout: 30", "timeout: 99", 1)
	p2, err := tg.Plan([]byte(edited), spec, m)
	if err != nil {
		t.Fatal(err)
	}
	if string(p2.After) != edited || len(p2.Warnings) != 1 {
		t.Errorf("modified entry must be left alone with a warning: %v", p2.Warnings)
	}
	r, err := tg.Remove([]byte(edited), manifestFrom(gBinary, p2))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings) != 1 || len(r.Entries) != 1 || !strings.Contains(string(r.After), "timeout: 99") {
		t.Errorf("warnings=%v kept=%d\n%s", r.Warnings, len(r.Entries), r.After)
	}
	if strings.Count(string(r.After), "- command:") != 1 {
		t.Errorf("only the modified entry may remain:\n%s", r.After)
	}
	// without a manifest nothing can be claimed
	p3, _ := tg.Plan(p1.After, spec, nil)
	if string(p3.After) != string(p1.After) || len(p3.Warnings) != 5 || len(p3.Entries) != 0 {
		t.Errorf("no manifest: warnings=%d entries=%d", len(p3.Warnings), len(p3.Entries))
	}
}

func TestHermesUnsupportedBlocksRefuse(t *testing.T) {
	bad := map[string]string{
		"flow mapping":        "hooks: {pre_tool_call: []}\n",
		"flow sequence":       "hooks: [a, b]\n",
		"scalar":              "hooks: yes\n",
		"anchor":              "hooks: &h\n  pre_tool_call: []\n",
		"alias":               "hooks: *h\n",
		"tag":                 "hooks: !!map\n  a: b\n",
		"duplicate":           "hooks:\n  a: []\nhooks:\n  b: []\n",
		"sequence at top":     "hooks:\n  - a\n",
		"flow event list":     "hooks:\n  pre_tool_call: [{command: x}]\n",
		"scalar event":        "hooks:\n  pre_tool_call: nope\n",
		"merge key":           "hooks:\n  <<: *base\n",
		"tab indent":          "hooks:\n\tpre_tool_call:\n\t\t- command: x\n",
		"inconsistent indent": "hooks:\n    a: []\n  b: []\n",
		"item outside event":  "hooks:\n  # c\n    - command: x\n",
		"mixed item indent":   "hooks:\n  pre_tool_call:\n    - command: a\n   - command: b\n",
		"quoted duplicate":    "\"hooks\":\n  a: []\nhooks:\n  b: []\n",
		"empty flow + body":   "hooks: {}\n  a: []\n",
		"flow item":           "hooks:\n  pre_tool_call:\n    - {command: x}\n",
		"flow item list":      "hooks:\n  pre_tool_call:\n    - [a, b]\n",
		"anchor item":         "hooks:\n  pre_tool_call:\n    - &a\n      command: x\n",
		"anchor item value":   "hooks:\n  pre_tool_call:\n    - command: &a x\n",
		"alias item":          "hooks:\n  pre_tool_call:\n    - *a\n",
		"alias item value":    "hooks:\n  pre_tool_call:\n    - command: x\n      timeout: *t\n",
		"tag item":            "hooks:\n  pre_tool_call:\n    - !tag\n      command: x\n",
		"tag item value":      "hooks:\n  pre_tool_call:\n    - command: !!str x\n",
		"flow field value":    "hooks:\n  pre_tool_call:\n    - command: x\n      env: {A: 1}\n",
		"flow seq field":      "hooks:\n  pre_tool_call:\n    - command: x\n      args: [a]\n",
	}
	for name, in := range bad {
		if _, err := gHermesT().Plan([]byte(in), gspec(model.ClientHermes), nil); !errors.Is(err, model.ErrSettingsParse) {
			t.Errorf("%s: Plan error = %v, want ErrSettingsParse", name, err)
		}
		if _, err := gHermesT().Remove([]byte(in), manifestFrom(gBinary)); !errors.Is(err, model.ErrSettingsParse) {
			t.Errorf("%s: Remove error = %v, want ErrSettingsParse", name, err)
		}
	}
}

func TestHermesSectionParsing(t *testing.T) {
	src := "model: x\nhooks:   # the hooks\n  pre_tool_call:\n    - command: \"a b\"   # c\n      timeout: 30\n    -   command: 'it''s'\n    - command: x\n      nested:\n        deep: 1\n  # comment\n\nafter: 1\n"
	sec, err := parseYAMLDoc([]byte(src)).section()
	if err != nil {
		t.Fatal(err)
	}
	if sec.hdr != 1 || sec.keyIndent != 2 || len(sec.events) != 1 {
		t.Fatalf("section = %+v", sec)
	}
	ev := sec.events[0]
	if ev.name != "pre_tool_call" || ev.itemInd != 4 || len(ev.items) != 3 {
		t.Fatalf("event = %+v", ev)
	}
	got := []string{ev.items[0].command, ev.items[1].command, ev.items[2].command}
	if !reflect.DeepEqual(got, []string{"a b", "it's", ""}) {
		t.Errorf("commands = %q", got)
	}
	if !ev.items[2].opaque {
		t.Error("nested item must be opaque")
	}
	if ev.items[2].command != "" {
		t.Error("opaque items never expose a command")
	}
	if sec.last != 9 {
		t.Errorf("last = %d (the trailing comment belongs to the block, the blank line does not)", sec.last)
	}
}

func TestParseYAMLScalar(t *testing.T) {
	cases := []struct {
		in   string
		want any
		ok   bool
	}{
		{`"a\"b\\c\n\u00e9\x41"`, "a\"b\\c\n\u00e9A", true},
		{`"x" # c`, "x", true},
		{`"x" y`, nil, false},
		{`'it''s'`, "it's", true},
		{`'x' # c`, "x", true},
		{`plain text # c`, "plain text", true},
		{`30`, jn("30"), true},
		{`-5`, jn("-5"), true},
		{`+5`, jn("5"), true},
		{`007`, "007", true},
		{`1.5`, "1.5", true},
		{`true`, true, true},
		{`False`, false, true},
		{`null`, nil, true},
		{`~`, nil, true},
		{`|`, nil, false},
		{`>-`, nil, false},
		{`[a]`, nil, false},
		{`{a: 1}`, nil, false},
		{`&a x`, nil, false},
		{`*a`, nil, false},
		{`!!str x`, nil, false},
		{`- x`, nil, false},
		{``, nil, false},
		{`"bad \q escape"`, nil, false},
		{`"unterminated`, nil, false},
		{`"\x4"`, nil, false},
		{`'unterminated`, nil, false},
	}
	for _, c := range cases {
		got, ok := parseYAMLScalar(c.in)
		if ok != c.ok || (ok && !reflect.DeepEqual(got, c.want)) {
			t.Errorf("parseYAMLScalar(%q) = %#v, %v; want %#v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseYAMLKeyLine(t *testing.T) {
	cases := []struct {
		in         string
		name, rest string
		ok         bool
	}{
		{"a: b", "a", "b", true},
		{"a:", "a", "", true},
		{"a:   # c", "a", "", true},
		{"a: b # c", "a", "b", true},
		{`"q k": v`, "q k", "v", true},
		{`'q': v`, "q", "v", true},
		{"cmd: http://x/y", "cmd", "http://x/y", true},
		{"a b: c", "a b", "c", true},
		{"no colon", "", "", false},
		{"- a: b", "", "", false},
		{"[a]: b", "", "", false},
		{"&a: b", "", "", false},
		{`"unterminated: v`, "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		name, rest, ok := parseYAMLKeyLine(c.in)
		if ok != c.ok || name != c.name || rest != c.rest {
			t.Errorf("parseYAMLKeyLine(%q) = %q, %q, %v; want %q, %q, %v", c.in, name, rest, ok, c.name, c.rest, c.ok)
		}
	}
}

func jn(s string) any { return jsonNumber(s) }

func TestStripYAMLComment(t *testing.T) {
	for in, want := range map[string]string{
		"a # b": "a", "a#b": "a#b", "# only": "", `"a # b" # c`: `"a # b"`, `'a # b'`: `'a # b'`,
		"a  ": "a", `"esc \" # x" # y`: `"esc \" # x"`,
	} {
		if got := stripYAMLComment(in); got != want {
			t.Errorf("stripYAMLComment(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzHermesPlan(f *testing.F) {
	for _, s := range []string{
		"", "model: x\n", "hooks:\n  pre_tool_call:\n    - command: a\n", "hooks: {}\n", "a: 1\r\nhooks:\r\n  x: []\r\n",
		"hooks:\n  pre_tool_call:\n  - command: \"q\"\n    timeout: 5\n# c\n", "hooks:\n\tx: 1\n", "hooks: [\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		tg := gHermesT()
		spec := gspec(model.ClientHermes)
		p, err := tg.Plan([]byte(in), spec, nil)
		if err != nil {
			if !errors.Is(err, model.ErrSettingsParse) {
				t.Fatalf("unexpected error type: %v", err)
			}
			return
		}
		out := string(p.After)
		// a successful plan must itself be a supported document
		if _, err := parseYAMLDoc(p.After).section(); err != nil {
			t.Fatalf("output has an unsupported hooks block: %v\n%q -> %q", err, in, out)
		}
		m := manifestFrom(gBinary, p)
		p2, err := tg.Plan(p.After, spec, m)
		if err != nil || string(p2.After) != out {
			t.Fatalf("second install differs: %v\n%q\n%q", err, out, p2.After)
		}
		r, err := tg.Remove(p.After, m)
		if err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if r.After != nil && strings.Contains(string(r.After), "ballast hook --client hermes") {
			t.Fatalf("uninstall left our entries: %q", r.After)
		}
	})
}
