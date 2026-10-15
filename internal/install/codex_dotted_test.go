package install

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// requireValidTOML runs the output through Python's tomllib when available;
// it is the independent check that the editor never writes a table twice.
func requireValidTOML(t *testing.T, name, doc string) {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		return
	}
	cmd := exec.Command(py, "-I", "-c", "import sys\ntry:\n import tomllib\nexcept ImportError:\n sys.exit(0)\ntomllib.loads(sys.stdin.read())")
	cmd.Stdin = strings.NewReader(doc)
	var out bytes.Buffer
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Errorf("%s: output is not valid TOML: %v %s\n%s", name, err, out.String(), doc)
	}
}

func TestFeaturesHooksDottedKeys(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"root dotted key", "model = \"o3\"\nfeatures.web_search_request = true\n",
			"model = \"o3\"\nfeatures.web_search_request = true\nfeatures.hooks = true\n"},
		{"root dotted key before a table", "model = \"o3\"\nfeatures.web_search_request = true\n\n[profiles.x]\na = 1\n",
			"model = \"o3\"\nfeatures.web_search_request = true\nfeatures.hooks = true\n\n[profiles.x]\na = 1\n"},
		{"several dotted keys, last wins", "features.a = 1\nfeatures.b = 2\nx = 3\n",
			"features.a = 1\nfeatures.b = 2\nfeatures.hooks = true\nx = 3\n"},
		{"dotted key without trailing newline", "features.a = 1", "features.a = 1\nfeatures.hooks = true\n"},
		{"dotted key with multi-line value", "features.l = [\n  1,\n]\n[x]\n", "features.l = [\n  1,\n]\nfeatures.hooks = true\n[x]\n"},
		{"quoted dotted key", "\"features\".a = 1\n", "\"features\".a = 1\nfeatures.hooks = true\n"},
		{"dotted key under another table is not features", "[tui]\nfeatures.a = 1\n",
			"[tui]\nfeatures.a = 1\n\n[features]\nhooks = true\n"},
		{"dotted hooks under the header", "[features]\nweb = true\n", "[features]\nhooks = true\nweb = true\n"},
		{"dotted false flips", "features.hooks = false\nfeatures.a = 1\n", "features.hooks = true\nfeatures.a = 1\n"},
	}
	for _, c := range cases {
		p := planConfig(t, c.in, nil)
		if string(p.After) != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, p.After, c.want)
		}
		requireValidTOML(t, c.name, string(p.After))
		p2 := planConfig(t, string(p.After), manifestFrom(gBinary, p))
		if string(p2.After) != string(p.After) {
			t.Errorf("%s: not idempotent: %q", c.name, p2.After)
		}
	}
}

func TestFeaturesHooksRefusesUneditableLayouts(t *testing.T) {
	bad := []string{
		"features.a = 1\n[features]\nb = 2\n",
		"features.hooks.x = 1\n",
		"[features.hooks]\nx = 1\n",
		"[features]\nhooks.x = 1\n",
		"[[features]]\na = 1\n",
		"[[features.hooks]]\na = 1\n",
		"features = 1\n",
		"features.hooks = true\n[features]\n",
		"features.hooks = true\nfeatures.hooks = false\n",
	}
	for _, in := range bad {
		if _, err := gCodexCT().Plan([]byte(in), gspec(model.ClientCodex), nil); !errors.Is(err, model.ErrSettingsParse) {
			t.Errorf("Plan(%q) error = %v, want ErrSettingsParse", in, err)
		}
	}
}

func trustConfigPlan(cfg string) (*PlanResult, error) {
	spec := gspec(model.ClientCodex)
	spec.CodexTrust = true
	hp, err := gCodexHT().Plan(nil, spec, nil)
	if err != nil {
		return nil, err
	}
	return gCodexCT().planWith([]byte(cfg), spec, nil, []*PlanResult{hp})
}

func TestTrustRefusesDottedDefinitions(t *testing.T) {
	hp, _ := gCodexHT().Plan(nil, func() EntrySpec { s := gspec(model.ClientCodex); s.CodexTrust = true; return s }(), nil)
	k := fmt.Sprintf("%s:%s:0:0", gCodexH, snakeEvent(hp.Entries[0].Native))
	q := "'" + k + "'"
	bad := []string{
		"[hooks.state]\n" + q + ".enabled = true\n",
		"[hooks.state]\n" + q + " = { enabled = true }\n",
		"[hooks]\nstate." + q + ".enabled = true\n",
		"hooks.state." + q + ".enabled = true\n",
		"hooks = { state = {} }\n",
		"hooks.state = 1\n",
		"[hooks]\nstate = 1\n",
		"[[hooks.state]]\nx = 1\n",
	}
	for _, in := range bad {
		if _, err := trustConfigPlan(in); !errors.Is(err, model.ErrSettingsParse) {
			t.Errorf("trust Plan(%q) error = %v, want ErrSettingsParse", in, err)
		}
	}
	// siblings defined by dotted keys do not collide with a new sub-table
	good := []string{
		"hooks.state.other.enabled = true\n",
		"[hooks.state]\nother.enabled = true\n",
		"[hooks]\nx = 1\n",
		"[hooks.state]\n",
	}
	for _, in := range good {
		p, err := trustConfigPlan(in)
		if err != nil {
			t.Errorf("trust Plan(%q): %v", in, err)
			continue
		}
		requireValidTOML(t, in, string(p.After))
	}
}
