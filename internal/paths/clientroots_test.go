package paths

import (
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// TestCompileGlobsExpandsRedirectedClientRoots: the literal "~/.claude/",
// "~/.codex/" and "~/.hermes/" prefixes also protect the roots named by
// CLAUDE_CONFIG_DIR / CODEX_HOME / HERMES_HOME; unset roots and patterns that
// are not under those prefixes are untouched.
func TestCompileGlobsExpandsRedirectedClientRoots(t *testing.T) {
	s := &Service{}
	pats := []string{"~/.claude/settings*.json", "~/.codex/hooks.json", "~/.hermes/config.yaml", "~/.ballast/**", "!~/.codex/keep.json"}
	ctx := model.PathContext{
		GOOS: "linux", Home: "/home/u",
		ClientRoots: model.ClientRoots{Claude: "/srv/c", Codex: "/srv/x", Hermes: "/home/u/.hermes"},
	}
	m, err := s.CompileGlobs(pats, ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		p    string
		want bool
	}{
		{"/home/u/.claude/settings.json", true},
		{"/srv/c/settings.local.json", true},
		{"/srv/c/other.json", false},
		{"/srv/x/hooks.json", true},
		{"/srv/x/keep.json", false},
		{"/home/u/.hermes/config.yaml", true},
		{"/srv/h/config.yaml", false}, // Hermes root not redirected here
		{"/home/u/.ballast/rules.json", true},
	} {
		if got := m.Match(c.p); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.p, got, c.want)
		}
	}
	// No redirected roots: only the literal forms.
	ctx.ClientRoots = model.ClientRoots{}
	m, _ = s.CompileGlobs(pats, ctx)
	if m.Match("/srv/c/settings.json") {
		t.Error("an unset root must not widen the match")
	}
}
