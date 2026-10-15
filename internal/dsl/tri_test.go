package dsl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

type triFile struct {
	And      [][]string `json:"and"`
	Or       [][]string `json:"or"`
	Not      [][]string `json:"not"`
	Bindings []struct {
		Desc           string     `json:"desc"`
		PerBinding     [][]string `json:"per_binding"`
		MatcherUnknown bool       `json:"matcher_unknown"`
		Want           string     `json:"want"`
	} `json:"bindings"`
	Arbitration []struct {
		Client       model.ClientID  `json:"client"`
		Event        model.EventName `json:"event"`
		Hits         [][]string      `json:"hits"`
		WantAction   string          `json:"want_action"`
		WantReasonID []string        `json:"want_reason_ids"`
		WantDegraded []string        `json:"want_degraded"`
		WantLabels   []string        `json:"want_labels"`
	} `json:"arbitration"`
}

func loadTri(t *testing.T) triFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "tri.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f triFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func rs(t model.Tri) res { return res{t: t} }

func tri(t *testing.T, s string) model.Tri {
	t.Helper()
	v, ok := model.ParseTri(s)
	if !ok {
		t.Fatalf("bad tri %q", s)
	}
	return v
}

func TestTriTables(t *testing.T) {
	f := loadTri(t)
	for _, row := range f.And {
		a, b, want := tri(t, row[0]), tri(t, row[1]), tri(t, row[2])
		if got := rs(a).and(rs(b)).t; got != want {
			t.Errorf("%v AND %v = %v, want %v", a, b, got, want)
		}
		if got := rs(b).and(rs(a)).t; got != want {
			t.Errorf("%v AND %v = %v, want %v (commuted)", b, a, got, want)
		}
	}
	for _, row := range f.Or {
		a, b, want := tri(t, row[0]), tri(t, row[1]), tri(t, row[2])
		if got := rs(a).or(rs(b)).t; got != want {
			t.Errorf("%v OR %v = %v, want %v", a, b, got, want)
		}
		if got := rs(b).or(rs(a)).t; got != want {
			t.Errorf("%v OR %v = %v, want %v (commuted)", b, a, got, want)
		}
	}
	for _, row := range f.Not {
		if got := rs(tri(t, row[0])).not().t; got != tri(t, row[1]) {
			t.Errorf("NOT %s = %v, want %s", row[0], got, row[1])
		}
	}
}

// triWorld answers path_exists per path from a table.
type triWorld struct {
	exists map[string]model.Tri
	env    map[string]string
}

func (w triWorld) PathExists(p string) model.Tri {
	if v, ok := w.exists[p]; ok {
		return v
	}
	return model.TriFalse
}
func (w triWorld) Getenv(n string) (string, bool) { v, ok := w.env[n]; return v, ok }
func (w triWorld) Branch() (string, model.Tri)    { return "", model.TriUnknown }

// triSession answers read_this_session per path from a table.
type triSession struct {
	*fixSession
	reads map[string]bool
}

func (s triSession) HasRead(p string) bool { return s.reads[p] }

func TestTriBindings(t *testing.T) {
	f := loadTri(t)
	e := NewEngine(Options{Paths: fakePaths{}, GOOS: "linux"})
	pc := model.PathContext{Cwd: "/repo", ProjectRoot: "/repo", Home: "/home/user", GOOS: "linux"}

	for _, row := range f.Bindings {
		t.Run(row.Desc, func(t *testing.T) {
			var rule string
			in := &model.EvalInput{Paths: pc, SessionReadable: true, Session: triSession{fixSession: &fixSession{}}}
			ev := &model.Event{Client: model.ClientClaude, Native: "PreToolUse", Name: model.EventPreToolUse}
			world := triWorld{exists: map[string]model.Tri{}, env: map[string]string{}}
			reads := map[string]bool{}

			switch {
			case row.MatcherUnknown:
				rule = `{"id":"tri-shell","event":"PreToolUse","match":{"bash":{"cmd":"x"}},"when":[{"env_equals":{"name":"A","value":"1"}}],"examples":[]}`
				ev.Tool = &model.ToolCall{Name: "Bash", Shell: model.ShellBash, Command: "x"}
				switch {
				case len(row.PerBinding) == 0:
					in.Commands = staticCommands{err: os.ErrInvalid}
				default:
					if tri(t, row.PerBinding[0][0]) == model.TriTrue {
						world.env["A"] = "1"
					}
					in.Commands = staticCommands{cl: &model.CommandList{
						Shell: model.ShellBash, Unknown: true,
						Nodes: []model.CommandNode{{Argv: []model.Word{{Lit: "x"}}, Name: "x"}},
					}}
				}
			case len(row.PerBinding) == 0:
				rule = `{"id":"tri-path","event":"PreToolUse","match":{"tool":"Edit","path":"nomatch/**"},"when":[{"path_exists":{}}],"examples":[]}`
				ev.Tool = &model.ToolCall{Name: "Edit", Targets: []string{"/repo/a.go", "/repo/b.go"}}
			default:
				conds := `{"path_exists":{}}`
				if len(row.PerBinding[0]) > 1 {
					conds += `,{"read_this_session":{}}`
				}
				rule = `{"id":"tri-edit","event":"PreToolUse","match":{"tool":"Edit"},"when":[` + conds + `],"examples":[]}`
				tc := &model.ToolCall{Name: "Edit"}
				for i, vals := range row.PerBinding {
					p := "/repo/t" + string(rune('0'+i))
					tc.Targets = append(tc.Targets, p)
					world.exists[p] = tri(t, vals[0])
					if len(vals) > 1 {
						reads[p] = tri(t, vals[1]) == model.TriTrue
					}
				}
				ev.Tool = tc
			}
			in.Event = ev
			in.World = world
			in.Session = triSession{fixSession: &fixSession{}, reads: reads}
			cr, err := e.Compile([]model.LoadedRule{{Rule: decodeRule(t, rule)}}, model.Settings{}, "g")
			if err != nil {
				t.Fatal(err)
			}
			v, err := e.Evaluate(cr, in)
			if err != nil || len(v.Results) != 1 {
				t.Fatalf("evaluate: %v %+v", err, v)
			}
			if got := v.Results[0].Outcome; got != tri(t, row.Want) {
				t.Errorf("outcome = %v, want %s", got, row.Want)
			}
		})
	}
}

func TestTriArbitration(t *testing.T) {
	f := loadTri(t)
	for i, row := range f.Arbitration {
		var hits []model.RuleHit
		for _, h := range row.Hits {
			hits = append(hits, model.RuleHit{
				RuleID: h[0], Action: model.Action(h[1]), Target: model.DefaultTarget(row.Event), Message: "msg " + h[0],
			})
		}
		v := Arbitrate(row.Client, row.Event, nil, hits)
		if string(v.Action) != row.WantAction {
			t.Errorf("row %d: action = %q, want %q", i, v.Action, row.WantAction)
		}
		if row.WantReasonID != nil {
			var ids []string
			for _, line := range strings.Split(v.Reason, "\n") {
				ids = append(ids, line[1:strings.Index(line, "]")])
			}
			if strings.Join(ids, ",") != strings.Join(row.WantReasonID, ",") {
				t.Errorf("row %d: reason ids = %v, want %v (reason %q)", i, ids, row.WantReasonID, v.Reason)
			}
		}
		if row.WantDegraded != nil {
			var got []string
			for _, h := range v.Hits {
				if h.Effective != h.Action {
					got = append(got, h.RuleID)
				}
			}
			if strings.Join(got, ",") != strings.Join(row.WantDegraded, ",") {
				t.Errorf("row %d: degraded = %v, want %v", i, got, row.WantDegraded)
			}
		}
		if row.WantLabels != nil {
			var got []string
			for _, h := range v.Hits {
				got = append(got, h.Label())
			}
			if strings.Join(got, ",") != strings.Join(row.WantLabels, ",") {
				t.Errorf("row %d: labels = %v, want %v", i, got, row.WantLabels)
			}
		}
	}
}
