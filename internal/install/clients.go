package install

import (
	"path/filepath"
	"strings"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// Clients returns the four clients in model.AllClients order.
func Clients() []Client {
	return []Client{claudeClient{}, codexClient{}, cursorClient{}, hermesClient{}}
}

func envGOOS(lay layout.Layout, env sys.Env) string {
	if lay.GOOS != "" {
		return lay.GOOS
	}
	if env != nil {
		return env.GOOS()
	}
	return ""
}

// lookPath finds an executable on PATH using only fsys (never runs it).
func lookPath(fsys sys.FS, env sys.Env, goos, name string) string {
	if env == nil {
		return ""
	}
	pathVar, _ := env.Getenv("PATH")
	if pathVar == "" {
		return ""
	}
	sep, exts := ":", []string{""}
	if goos == "windows" {
		sep, exts = ";", []string{".exe", ".cmd", ".bat", ".com", ""}
	}
	for _, dir := range strings.Split(pathVar, sep) {
		if dir == "" {
			continue
		}
		for _, ext := range exts {
			p := filepath.Join(dir, name+ext)
			if fi, err := fsys.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
		}
	}
	return ""
}

func detect(id model.ClientID, cmd string, lay layout.Layout, fsys sys.FS, env sys.Env) Detection {
	d := Detection{ConfigDir: lay.ClientDir(id)}
	if d.ConfigDir != "" {
		if fi, err := fsys.Stat(d.ConfigDir); err == nil && fi.IsDir() {
			d.Installed = true
		}
	}
	if p := lookPath(fsys, env, envGOOS(lay, env), cmd); p != "" {
		d.Installed = true
		d.Notes = append(d.Notes, cmd+" found at "+p)
	}
	return d
}

func spanGOOS(lay layout.Layout, spec EntrySpec) string {
	return firstNonEmpty(spec.GOOS, lay.GOOS)
}

// ---- Claude Code ---------------------------------------------------------

type claudeClient struct{}

func (claudeClient) ID() model.ClientID { return model.ClientClaude }

func (claudeClient) Detect(lay layout.Layout, fsys sys.FS, env sys.Env) Detection {
	d := detect(model.ClientClaude, "claude", lay, fsys, env)
	managed, file, err := DetectManaged(lay, fsys)
	d.Managed = managed
	if managed {
		d.Notes = append(d.Notes, "Ballast hooks are deployed in managed settings: "+file)
	} else if err != nil {
		d.Notes = append(d.Notes, "managed settings could not be fully inspected: "+err.Error())
	}
	return d
}

func (claudeClient) Targets(lay layout.Layout, spec EntrySpec) []Target {
	if lay.ClaudeDir == "" {
		return nil
	}
	return []Target{&jsonHooksTarget{client: model.ClientClaude, path: filepath.Join(lay.ClaudeDir, "settings.json"), goos: spanGOOS(lay, spec)}}
}

func (claudeClient) PostInstallNotes() []string {
	return []string{"Claude Code: hooks apply to new sessions; restart running sessions."}
}

// ---- Codex ---------------------------------------------------------------

type codexClient struct{}

func (codexClient) ID() model.ClientID { return model.ClientCodex }

func (codexClient) Detect(lay layout.Layout, fsys sys.FS, env sys.Env) Detection {
	return detect(model.ClientCodex, "codex", lay, fsys, env)
}

func (codexClient) Targets(lay layout.Layout, spec EntrySpec) []Target {
	if lay.CodexDir == "" {
		return nil
	}
	goos := spanGOOS(lay, spec)
	hooks := &jsonHooksTarget{client: model.ClientCodex, path: filepath.Join(lay.CodexDir, "hooks.json"), goos: goos}
	return []Target{hooks, &codexConfigTarget{path: filepath.Join(lay.CodexDir, "config.toml"), hooks: hooks, goos: goos}}
}

func (codexClient) PostInstallNotes() []string {
	return []string{
		"Codex: approve the new hooks with /hooks in the Codex TUI (or reinstall with --codex-trust); hooks fire only in interactive TUI sessions (unverified).",
	}
}

// ---- Cursor --------------------------------------------------------------

type cursorClient struct{}

func (cursorClient) ID() model.ClientID { return model.ClientCursor }

func (cursorClient) Detect(lay layout.Layout, fsys sys.FS, env sys.Env) Detection {
	return detect(model.ClientCursor, "cursor", lay, fsys, env)
}

func (cursorClient) Targets(lay layout.Layout, spec EntrySpec) []Target {
	if lay.CursorDir == "" {
		return nil
	}
	return []Target{
		&jsonHooksTarget{client: model.ClientCursor, path: filepath.Join(lay.CursorDir, "hooks.json"), goos: spanGOOS(lay, spec)},
		&mdcTarget{path: filepath.Join(lay.CursorDir, "rules", "ballast.mdc")},
	}
}

func (cursorClient) PostInstallNotes() []string {
	return []string{"Cursor: run \"Developer: Reload Window\" for the hooks and ballast.mdc to take effect."}
}

// ---- Hermes --------------------------------------------------------------

type hermesClient struct{}

func (hermesClient) ID() model.ClientID { return model.ClientHermes }

func (hermesClient) Detect(lay layout.Layout, fsys sys.FS, env sys.Env) Detection {
	return detect(model.ClientHermes, "hermes", lay, fsys, env)
}

func (hermesClient) Targets(lay layout.Layout, spec EntrySpec) []Target {
	if lay.HermesDir == "" {
		return nil
	}
	return []Target{&hermesTarget{path: filepath.Join(lay.HermesDir, "config.yaml"), goos: spanGOOS(lay, spec)}}
}

func (hermesClient) PostInstallNotes() []string {
	return []string{"Hermes: start it with --accept-hooks to consent to the new hooks."}
}

// targetFor rebuilds the Target of a manifest-recorded path (uninstall does
// not depend on the current layout).
func targetFor(c model.ClientID, path, goos string) Target {
	switch c {
	case model.ClientClaude:
		return &jsonHooksTarget{client: c, path: path, goos: goos}
	case model.ClientCodex:
		if filepath.Base(path) == "config.toml" {
			return &codexConfigTarget{
				path:  path,
				hooks: &jsonHooksTarget{client: c, path: filepath.Join(filepath.Dir(path), "hooks.json"), goos: goos},
				goos:  goos,
			}
		}
		return &jsonHooksTarget{client: c, path: path, goos: goos}
	case model.ClientCursor:
		if strings.HasSuffix(path, ".mdc") {
			return &mdcTarget{path: path}
		}
		return &jsonHooksTarget{client: c, path: path, goos: goos}
	case model.ClientHermes:
		return &hermesTarget{path: path, goos: goos}
	}
	return nil
}
