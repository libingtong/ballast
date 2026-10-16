package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/model"
)

const repoRoot = "../../.."

// ccSources are the ConfigChange "source" values observed by M0b case
// configchange-payload (Claude Code 2.1.300, docs/facts/M0b-CONCLUSIONS.md A16).
var ccSources = map[string]bool{
	"user_settings": true, "project_settings": true, "local_settings": true,
	"policy_settings": true, "skills": true,
}

func TestConfigChangeGolden(t *testing.T) {
	var tbl struct {
		Rows []struct {
			Desc  string         `json:"desc"`
			File  string         `json:"file"`
			Stdin string         `json:"stdin"`
			Want  map[string]any `json:"want"`
		} `json:"rows"`
	}
	b, err := os.ReadFile("testdata/configchange.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &tbl); err != nil {
		t.Fatal(err)
	}
	if len(tbl.Rows) < 10 {
		t.Fatalf("only %d rows", len(tbl.Rows))
	}
	files := 0
	for i, r := range tbl.Rows {
		stdin := []byte(r.Stdin)
		if r.File != "" {
			files++
			if stdin, err = os.ReadFile(filepath.Join(repoRoot, r.File)); err != nil {
				t.Fatalf("row %d: %v", i, err)
			}
		}
		ev, err := New().ParseEvent("ConfigChange", stdin)
		if err != nil {
			t.Errorf("row %d (%s): %v", i, r.Desc, err)
			continue
		}
		raw, _ := json.Marshal(ev)
		got := map[string]any{}
		_ = json.Unmarshal(raw, &got)
		for k, w := range r.Want {
			g, present := got[k]
			if !present {
				g = ""
			}
			if g != w {
				t.Errorf("row %d (%s): %s = %#v, want %#v", i, r.Desc, k, g, w)
			}
		}
		if ev.Tool != nil || ev.Name != model.EventConfigChange || ev.Client != model.ClientClaude {
			t.Errorf("row %d (%s): unexpected event shape %+v", i, r.Desc, ev)
		}
	}
	// 6 = docs/facts/payloads/m0b/configchange-payload/hang/03..08-managed-
	// ConfigChange.json (M0b-CONCLUSIONS A16: user/project/local settings,
	// skill and managed-settings edits). Re-capturing may change the count:
	// update it here and in TestConfigChangeAllCaptures together.
	if files != 6 {
		t.Errorf("%d capture rows, want the 6 M0b ConfigChange payloads", files)
	}
}

// Every M0b ConfigChange capture parses with a known source and a non-empty
// ConfigFile equal to the raw file_path (nothing else is added or dropped).
func TestConfigChangeAllCaptures(t *testing.T) {
	dir := filepath.Join(repoRoot, "docs/facts/payloads/m0b/configchange-payload")
	n := 0
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, "-ConfigChange.json") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var raw struct {
			Name     string `json:"hook_event_name"`
			Source   string `json:"source"`
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			return err
		}
		if raw.Name != "ConfigChange" {
			t.Errorf("%s: hook_event_name = %q", path, raw.Name)
		}
		ev, err := New().ParseEvent(raw.Name, b)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		if ev.ConfigFile == "" || ev.ConfigFile != raw.FilePath || ev.Source != raw.Source || !ccSources[ev.Source] {
			t.Errorf("%s: Source=%q ConfigFile=%q, payload %q/%q", path, ev.Source, ev.ConfigFile, raw.Source, raw.FilePath)
		}
		n++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// See TestConfigChangeGolden for what the 6 captures are.
	if n != 6 {
		t.Fatalf("%d ConfigChange captures, want 6", n)
	}
}

// ConfigFile is filled for ConfigChange only: a top-level file_path on any
// other event (e.g. a hypothetical future payload) must not leak into it.
func TestConfigFileOnlyForConfigChange(t *testing.T) {
	for _, ne := range model.NativeEventTable[model.ClientClaude] {
		if ne.Event == model.EventConfigChange {
			continue
		}
		ev, err := New().ParseEvent(ne.Native, []byte(`{"session_id":"s","file_path":"/x"}`))
		if err != nil {
			t.Fatalf("%s: %v", ne.Native, err)
		}
		if ev.ConfigFile != "" {
			t.Errorf("%s: ConfigFile = %q", ne.Native, ev.ConfigFile)
		}
	}
}

// The ConfigChange row is installed and verified after M0b (§5.1): audit
// only, no rule role, and its event is not a tool event.
func TestConfigChangeTableRow(t *testing.T) {
	ne, ok := model.LookupNative(model.ClientClaude, "ConfigChange")
	if !ok {
		t.Fatal("no ConfigChange row")
	}
	if !ne.Install || !ne.Verified || len(ne.Roles) != 1 || ne.Roles[0] != model.RoleAudit || ne.Event != model.EventConfigChange {
		t.Errorf("row = %+v, want Install+Verified, audit only", ne)
	}
	if ne.Event.IsToolEvent() || ne.Event.IsStopFamily() {
		t.Error("ConfigChange classified as tool or stop event")
	}
}

// Audit only: nothing may be rendered for ConfigChange (never blocks, never
// injects). Actions the capability set does not offer are errors.
func TestConfigChangeRenderIsSilent(t *testing.T) {
	a := New()
	ev := &model.Event{Client: model.ClientClaude, Name: model.EventConfigChange, Native: "ConfigChange"}
	for _, d := range []*model.Decision{
		nil, {}, {Action: model.ActionNone}, {Action: model.ActionLog}, {Action: model.ActionNone, Context: "ctx"},
	} {
		out, err := a.Render(ev, d)
		if err != nil || len(out) != 0 {
			t.Errorf("%+v: out=%q err=%v", d, out, err)
		}
	}
	for _, act := range []model.Action{model.ActionDeny, model.ActionAsk, model.ActionWarn} {
		out, err := a.Render(ev, &model.Decision{Action: act, Reason: "r"})
		if err == nil || len(out) != 0 {
			t.Errorf("%s: out=%q err=%v, want error and no output", act, out, err)
		}
	}
}

// FuzzParseConfigChange: arbitrary payloads never panic; success keeps the
// invariants (no Tool, ConfigFile only ever a verbatim string file_path).
func FuzzParseConfigChange(f *testing.F) {
	for _, s := range []string{
		``, `{}`, `[]`, `null`, `{"file_path":`, `{"file_path":"/a","source":"skills"}`,
		`{"file_path":7,"source":null}`, `{"file_path":["/a"]}`, `{"file_path":"\u0000\ud800"}`,
		`{"file_path":"C:\\x","source":"policy_settings","session_id":"s","cwd":"/r"}`,
		`{"file_path":"/a","file_path":"/b"}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, stdin []byte) {
		ev, err := New().ParseEvent("ConfigChange", stdin)
		if err != nil {
			if ev != nil {
				t.Fatalf("event returned with error %v", err)
			}
			return
		}
		if ev.Name != model.EventConfigChange || ev.Tool != nil || !ev.ReceivedAt.IsZero() {
			t.Fatalf("bad event %+v", ev)
		}
		if !utf8.ValidString(ev.ConfigFile) {
			t.Fatalf("ConfigFile not valid UTF-8: %q", ev.ConfigFile)
		}
		var raw struct {
			FilePath json.RawMessage `json:"file_path"`
		}
		if json.Unmarshal(stdin, &raw) == nil && ev.ConfigFile != "" {
			var s string
			if json.Unmarshal(raw.FilePath, &s) != nil || s != ev.ConfigFile {
				t.Fatalf("ConfigFile %q does not equal the payload's file_path %s", ev.ConfigFile, raw.FilePath)
			}
		}
		if _, err := json.Marshal(ev); err != nil {
			t.Fatalf("not marshalable: %v", err)
		}
	})
}
