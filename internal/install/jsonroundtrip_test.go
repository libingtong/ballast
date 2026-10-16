package install

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// Surgical JSON editing: for every layout a user may have, install touches
// nothing but the entries it inserts, and uninstall gives the file back byte
// for byte (audit round 4, finding 1/2/3).

type jclient struct {
	id    model.ClientID
	tg    func() *jsonHooksTarget
	event string // an event the client installs
	other string // an event it does not install by default
	entry string // a flat or nested user entry for that event
}

var jclients = []jclient{
	{model.ClientClaude, gClaudeT, "Stop", "Notification", `{"hooks":[{"type":"command","command":"/bin/user"}]}`},
	{model.ClientCodex, gCodexHT, "Stop", "Notification", `{"hooks":[{"type":"command","command":"/bin/user"}]}`},
	{model.ClientCursor, gCursorT, "stop", "afterFileEdit", `{"command":"/bin/user"}`},
}

// jvariant is a document template; {EV} {OTHER} {ENT} are per client.
type jvariant struct{ name, doc string }

func handVariants() []jvariant {
	return []jvariant{
		{"zero bytes", ""},
		{"newline only", "\n"},
		{"spaces only", "  \r\n  "},
		{"bom only", "\xef\xbb\xbf"},
		{"empty compact", "{}"},
		{"empty newline", "{}\n"},
		{"empty padded", "{ }\n"},
		{"empty multi-line", "{\n}\n"},
		{"empty crlf", "{\r\n}\r\n"},
		{"bom empty", "\xef\xbb\xbf{}\n"},
		{"compact", `{"theme":"dark"}`},
		{"compact newline", "{\"theme\":\"dark\",\"n\":1}\n"},
		{"inline padded", "{ \"theme\": \"dark\" }\n"},
		{"inline colon space only", `{"theme": "dark", "n": [1, 2]}`},
		{"tab no colon space", "{\n\t\"theme\":\"dark\"\n}\n"},
		{"two spaces blank line", "{\n  \"theme\": \"dark\",\n\n  \"model\": \"opus\"\n}\n"},
		{"four spaces", "{\n    \"theme\": \"dark\",\n    \"model\": \"opus\"\n}\n"},
		{"three spaces", "{\n   \"theme\": \"dark\"\n}\n"},
		{"space before colon", "{\n  \"theme\" : \"dark\",\n  \"model\"  :  \"opus\"\n}\n"},
		{"crlf", "{\r\n  \"theme\": \"dark\"\r\n}\r\n"},
		{"crlf blank line tab", "{\r\n\t\"theme\":\"dark\",\r\n\r\n\t\"model\":\"opus\"\r\n}\r\n"},
		{"no final newline", "{\n  \"theme\": \"dark\"\n}"},
		{"several final newlines", "{\n  \"theme\": \"dark\"\n}\n\n\n"},
		{"leading blank lines", "\n\n  {\n  \"theme\": \"dark\"\n  }  \n"},
		{"bom multi-line", "\xef\xbb\xbf{\n  \"theme\": \"dark\"\n}\n"},
		{"hooks empty compact", `{"theme":"dark","hooks":{}}`},
		{"hooks empty padded", "{\n  \"hooks\": { }\n}\n"},
		{"hooks empty multi-line", "{\n  \"hooks\": {\n  }\n}\n"},
		{"hooks only empty", "{\"hooks\":{}}\n"},
		{"event empty compact", `{"hooks":{"{EV}":[]}}`},
		{"event empty padded", "{\n  \"hooks\": {\n    \"{EV}\": [ ]\n  }\n}\n"},
		{"event empty multi-line", "{\n  \"hooks\": {\n    \"{EV}\": [\n    ]\n  }\n}\n"},
		{"event empty after blank line", "{\n  \"hooks\": {\n\n    \"{EV}\": []\n  }\n}\n"},
		{"other event empty", "{\n  \"hooks\": {\n    \"{OTHER}\": []\n  }\n}\n"},
		{"user entry compact", `{"hooks":{"{EV}":[{ENT}]}}`},
		{"user entry multi-line", "{\n  \"hooks\": {\n    \"{EV}\": [\n      {ENT}\n    ]\n  }\n}\n"},
		{"user entries blank lines", "{\n  \"hooks\": {\n    \"{EV}\": [\n      {ENT},\n\n      {ENT}\n    ],\n\n    \"{OTHER}\": [{ENT}]\n  },\n\n  \"theme\": \"dark\"\n}\n"},
		{"user entry other event", "{\n\t\"hooks\": {\n\t\t\"{OTHER}\": [\n\t\t\t{ENT}\n\t\t]\n\t}\n}\n"},
		{"inline hooks in multi-line root", "{\n  \"theme\":\"dark\",\n  \"hooks\": {\"{EV}\":[{ENT}]}\n}"},
		{"inline hooks first", "{\"hooks\":{\"{EV}\":[{ENT}]},\n \"theme\":\"dark\"}\n"},
		{"hooks between members", "{\n  \"a\": 1,\n  \"hooks\": {\n    \"{OTHER}\": [{ENT}]\n  },\n  \"z\": [\n    1,\n    2\n  ]\n}\n"},
		{"version present", "{\n  \"version\": 1,\n  \"theme\": \"dark\"\n}\n"},
		{"version other value", "{\"version\":2,\"theme\":\"dark\"}\n"},
		{"odd values", "{\n  \"a\" : [ 1 , 2.5e3 , -0 , true , false , null , \"x\\u00e9\\n\" ] ,\n  \"b\" : { \"c\" : { } , \"d\" : [ ] }\n}\n"},
		{"unicode and escapes", "{\"t\u00e9st\":\"\\\"q\\\" \u4e2d\u6587 \\\\\",\"\\u0041\":null}\n"},
		{"long minified", "{\"a\":" + strings.Repeat(`{"b":[1,2,{"c":"d"}],"k":`, 40) + "0" + strings.Repeat("}", 40) + ",\"theme\":\"dark\"}"},
		{"event arrays inline in multi-line", "{\n  \"hooks\": {\n    \"{EV}\": [{ENT}],\n    \"{OTHER}\": []\n  }\n}\n"},
		{"padded everywhere", "{ \"hooks\" : { \"{EV}\" : [ {ENT} ] } , \"theme\" : \"dark\" }\n"},
	}
}

// genVariants varies indent, line ending, colon spacing, blank lines and the
// final newline over one document shape (with and without a hooks object).
func genVariants() []jvariant {
	var out []jvariant
	for _, ind := range []string{"", "  ", "    ", "\t"} {
		for _, eol := range []string{"\n", "\r\n"} {
			for _, colon := range []string{": ", ":", " : "} {
				for _, blank := range []bool{false, true} {
					for _, finalNL := range []bool{false, true} {
						for _, withHooks := range []bool{false, true} {
							nl := eol
							if ind == "" {
								nl = "" // minified
							}
							if ind == "" && (blank || eol != "\n") {
								continue
							}
							i1, i2, i3 := ind, ind+ind, ind+ind+ind
							var b strings.Builder
							b.WriteString("{" + nl)
							b.WriteString(i1 + `"theme"` + colon + `"dark",` + nl)
							if blank {
								b.WriteString(nl)
							}
							if withHooks {
								b.WriteString(i1 + `"model"` + colon + `"opus",` + nl)
								b.WriteString(i1 + `"hooks"` + colon + "{" + nl)
								b.WriteString(i2 + `"{EV}"` + colon + "[" + nl)
								b.WriteString(i3 + "{ENT}" + nl)
								b.WriteString(i2 + "]" + nl)
								b.WriteString(i1 + "}" + nl)
							} else {
								b.WriteString(i1 + `"model"` + colon + `"opus"` + nl)
							}
							b.WriteString("}")
							if finalNL {
								b.WriteString(eol)
							}
							out = append(out, jvariant{
								name: fmt.Sprintf("gen indent=%q eol=%q colon=%q blank=%v nl=%v hooks=%v", ind, eol, colon, blank, finalNL, withHooks),
								doc:  b.String(),
							})
						}
					}
				}
			}
		}
	}
	return out
}

func (c jclient) expand(doc string) string {
	return strings.NewReplacer("{EV}", c.event, "{OTHER}", c.other, "{ENT}", c.entry).Replace(doc)
}

// isSubsequence reports whether every byte of orig appears in after, in order.
func isSubsequence(orig, after string) bool {
	i := 0
	for j := 0; j < len(after) && i < len(orig); j++ {
		if after[j] == orig[i] {
			i++
		}
	}
	return i == len(orig)
}

func decodeObj(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(stripBOM(b), &v); err != nil {
		t.Fatalf("not a JSON object: %v\n%q", err, b)
	}
	return v
}

func TestJSONInstallUninstallIsSurgical(t *testing.T) {
	variants := append(handVariants(), genVariants()...)
	if len(variants) < 30 {
		t.Fatalf("only %d variants", len(variants))
	}
	for _, c := range jclients {
		for _, v := range variants {
			doc := c.expand(v.doc)
			name := string(c.id) + "/" + v.name
			tg := c.tg()
			spec := gspec(c.id)

			p, err := tg.Plan([]byte(doc), spec, nil)
			if err != nil {
				t.Errorf("%s: Plan: %v\n%q", name, err, doc)
				continue
			}
			after := string(p.After)
			if !json.Valid(stripBOM(p.After)) {
				t.Errorf("%s: install produced invalid JSON:\n%q", name, after)
				continue
			}
			if !isSubsequence(doc, after) {
				t.Errorf("%s: install changed bytes that are not its own:\n  before %q\n  after  %q", name, doc, after)
				continue
			}
			// every client event has a Ballast entry
			hooks, _ := decodeObj(t, p.After)["hooks"].(map[string]any)
			for _, ne := range defaultEvents(c.id) {
				if l, _ := hooks[ne.Native].([]any); len(l) == 0 || !strings.Contains(fmt.Sprint(l[len(l)-1]), "ballast") {
					t.Errorf("%s: no Ballast entry for %s in %q", name, ne.Native, after)
					break
				}
			}
			// untouched members keep their values
			before, after2 := map[string]any{}, decodeObj(t, p.After)
			if strings.TrimSpace(strings.TrimPrefix(doc, "\xef\xbb\xbf")) != "" {
				before = decodeObj(t, []byte(doc))
			}
			for k, bv := range before {
				if k == "hooks" {
					continue
				}
				if fmt.Sprint(bv) != fmt.Sprint(after2[k]) {
					t.Errorf("%s: member %q changed", name, k)
				}
			}

			m := manifestFrom(gBinary, p)
			p2, err := tg.Plan(p.After, spec, m)
			if err != nil || string(p2.After) != after {
				t.Errorf("%s: second install differs (%v)\n%q\n%q", name, err, after, p2.After)
			}
			r, err := tg.Remove(p.After, m)
			if err != nil {
				t.Errorf("%s: Remove: %v", name, err)
				continue
			}
			got := string(r.After)
			if c.id == model.ClientCursor && !strings.Contains(doc, `"version"`) && strings.Contains(got, `"version"`) {
				// Cursor keeps the version:1 it added while user hooks remain (§5.3)
				if !strings.Contains(got, "/bin/user") {
					t.Errorf("%s: version left behind: %q", name, got)
				}
				continue
			}
			if got != doc {
				t.Errorf("%s: install+uninstall changed the file:\n  original %q\n  after    %q\n  restored %q", name, doc, after, got)
			}
		}
	}
}

// Install inserts, it never rewrites: these lines of the audit's
// counter-examples must come out exactly as they went in.
func TestJSONInstallLeavesSiblingsAlone(t *testing.T) {
	cases := []struct {
		name, in string
		keep     []string
	}{
		{"tab no colon space", "{\n\t\"theme\":\"dark\"\n}\n", []string{"{\n\t\"theme\":\"dark\",\n\t\"hooks\""}},
		{"blank line", "{\n  \"theme\": \"dark\",\n\n  \"model\": \"opus\"\n}\n", []string{"\"theme\": \"dark\",\n\n  \"model\": \"opus\","}},
		{"inline hooks", "{\n  \"theme\":\"dark\",\n  \"hooks\": {\"Notification\":[]}\n}", []string{"  \"theme\":\"dark\",\n  \"hooks\": {\"Notification\":[],"}},
		{"crlf", "{\r\n  \"a\"  :  1\r\n}\r\n", []string{"{\r\n  \"a\"  :  1,\r\n  \"hooks\""}},
	}
	for _, c := range cases {
		p := planAfter(t, gClaudeT(), c.in, gspec(model.ClientClaude), nil)
		if !isSubsequence(c.in, string(p.After)) {
			t.Errorf("%s: bytes of the user's file changed:\n%q\n%q", c.name, c.in, p.After)
		}
		for _, k := range c.keep {
			if !strings.Contains(string(p.After), k) {
				t.Errorf("%s: %q not kept in\n%q", c.name, k, p.After)
			}
		}
	}
}

// A user's own empty event array survives install + uninstall (finding 2);
// the one Ballast created next to it does not.
func TestEmptyEventArrayOfTheUserStays(t *testing.T) {
	for _, c := range jclients {
		in := c.expand("{\n  \"hooks\": {\n    \"{EV}\": [],\n\n    \"{OTHER}\": [ ]\n  }\n}\n")
		tg := c.tg()
		p := planAfter(t, tg, in, gspec(c.id), nil)
		var keys []string
		for _, f := range p.Flags {
			keys = append(keys, f.Key)
		}
		if !hasFlagKey(p.Flags, flagJSONEventPrefix+c.event) {
			t.Errorf("%s: no event flag recorded: %v", c.id, keys)
		}
		r, err := tg.Remove(p.After, manifestFrom(gBinary, p))
		if err != nil {
			t.Fatal(err)
		}
		if string(r.After) != in {
			t.Errorf("%s: got %q want %q", c.id, r.After, in)
		}
	}
	// the flag decides, not the array's name: an old manifest without it
	// keeps the previous behaviour (empty event removed)
	in := "{\"hooks\":{\"Stop\":[]}}"
	p := planAfter(t, gClaudeT(), in, gspec(model.ClientClaude), nil)
	m := manifestFrom(gBinary, p)
	var kept []model.ManagedFlag
	for _, f := range m.Flags {
		if !strings.HasPrefix(f.Key, flagJSONEventPrefix) {
			kept = append(kept, f)
		}
	}
	m.Flags = kept
	r, err := gClaudeT().Remove(p.After, m)
	if err != nil || string(r.After) != `{"hooks":{}}` {
		t.Errorf("pre-flag manifest: %q %v", r.After, err)
	}
}

// A file that existed with zero bytes (or only whitespace) is restored to
// exactly that, not deleted and not turned into "{}" (finding 3).
func TestBlankJSONFileIsRestored(t *testing.T) {
	for _, c := range jclients {
		for _, in := range []string{"", "\n", " \t\r\n", "\xef\xbb\xbf"} {
			tg := c.tg()
			p := planAfter(t, tg, in, gspec(c.id), nil)
			if p.Created {
				t.Errorf("%s %q: an existing blank file is not Created", c.id, in)
			}
			r, err := tg.Remove(p.After, manifestFrom(gBinary, p))
			if err != nil {
				t.Fatal(err)
			}
			if r.After == nil || string(r.After) != in {
				t.Errorf("%s: blank %q restored as %q (nil=%v)", c.id, in, r.After, r.After == nil)
			}
		}
	}
}

// End to end through the file system, including the 0-byte file staying
// a 0-byte file (the runner must not treat it as absent or delete it).
func TestInstallUninstallRoundTripOnDisk(t *testing.T) {
	docs := map[string]string{
		"zero":      "",
		"tabs":      "{\n\t\"theme\":\"dark\"\n}\n",
		"blank":     "{\n  \"theme\": \"dark\",\n\n  \"model\": \"opus\"\n}\n",
		"crlf":      "{\r\n  \"theme\": \"dark\"\r\n}",
		"empty ev":  "{\"hooks\":{\"Stop\":[],\"stop\":[]}}",
		"empty obj": "{ }",
	}
	for name, doc := range docs {
		e := newTenv(t)
		e.mkClientDirs()
		paths := []string{e.claudeSettings(), e.codexHooks(), e.cursorHooks()}
		for _, p := range paths {
			e.write(p, doc)
		}
		mustInstall(t, e.opts(model.ClientClaude, model.ClientCodex, model.ClientCursor))
		if e.read(e.claudeSettings()) == doc {
			t.Errorf("%s: install changed nothing", name)
		}
		mustUninstall(t, e.opts())
		for _, p := range paths {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s: %s is gone after uninstall", name, p)
				continue
			}
			if got := e.read(p); got != doc {
				t.Errorf("%s: %s = %q, want the original %q", name, p, got, doc)
			}
		}
	}
}
