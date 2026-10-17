package install

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// TestCodexTrustKeyUsesCanonicalCodexHome: Codex canonicalizes CODEX_HOME
// before it builds the hooks.state key (docs/facts/codex-static.md §1 row 6),
// so a CODEX_HOME that goes through a symbolic link (dotfiles, macOS
// /var/folders) must get keys with the resolved path, or the trusted_hash
// never matches and the hooks stay untrusted. doctor flags keys written in the
// literal form by an earlier version.
func TestCodexTrustKeyUsesCanonicalCodexHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	e := newTenv(t)
	root, err := filepath.EvalSymlinks(e.sb.Root)
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "realcx")
	link := filepath.Join(root, "linkcx")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	e.lay.CodexDir = link
	opt := e.opts(model.ClientCodex)
	opt.Force = true
	opt.CodexTrust = true
	opt.Binary = filepath.Join(root, "ballast")
	e.write(opt.Binary, "#!/bin/sh\n")
	if err := os.Chmod(opt.Binary, 0o755); err != nil {
		t.Fatal(err)
	}
	mustInstall(t, opt)

	cfg := e.read(filepath.Join(real, "config.toml"))
	if !strings.Contains(cfg, "[hooks.state.'"+filepath.Join(real, "hooks.json")+":") {
		t.Errorf("trust keys must name the canonical path %s:\n%s", real, cfg)
	}
	if strings.Contains(cfg, link) {
		t.Errorf("trust keys must not name the symlinked CODEX_HOME %s:\n%s", link, cfg)
	}
	// The hooks.json entries still sit at the literal path (the manifest keeps it).
	if !e.exists(filepath.Join(link, "hooks.json")) {
		t.Error("hooks.json not written through the CODEX_HOME link")
	}
	mustStatus(t, byID(e.doctor(opt.Binary, nil, nil)), "codex.trust", CheckOK)

	// Keys written by an earlier version (literal path) never match: doctor says so.
	lit := strings.ReplaceAll(cfg, filepath.Join(real, "hooks.json"), filepath.Join(link, "hooks.json"))
	m, err := LoadManifest(e.fs, e.lay.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	for i := range m.Flags {
		m.Flags[i].Key = strings.ReplaceAll(m.Flags[i].Key, filepath.Join(real, "hooks.json"), filepath.Join(link, "hooks.json"))
	}
	if err := SaveManifest(e.fs, e.ids, e.lay.Manifest, m); err != nil {
		t.Fatal(err)
	}
	e.write(filepath.Join(real, "config.toml"), lit)
	c := mustStatus(t, byID(e.doctor(opt.Binary, nil, nil)), "codex.trust", CheckWarn)
	if !strings.Contains(c.Message, "canonical") || !strings.Contains(c.Fix, "--codex-trust") {
		t.Errorf("doctor message = %q fix = %q", c.Message, c.Fix)
	}
	// Reinstalling repairs them.
	mustInstall(t, opt)
	mustStatus(t, byID(e.doctor(opt.Binary, nil, nil)), "codex.trust", CheckOK)
}

// CODEX_HOME that does not exist yet (the install creates it): the deepest
// existing ancestor is resolved and the rest appended, also when HOME itself is
// a symbolic link, so the trust key names the path Codex will canonicalize to.
func TestCodexHomeRealResolvesTheDeepestExistingAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	e := newTenv(t)
	root, err := filepath.EvalSymlinks(e.sb.Root)
	if err != nil {
		t.Fatal(err)
	}
	realHome := filepath.Join(root, "realhome")
	linkHome := filepath.Join(root, "linkhome")
	if err := os.MkdirAll(realHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realHome, linkHome); err != nil {
		t.Fatal(err)
	}
	// helper-level cases
	for _, c := range []struct{ in, want string }{
		{filepath.Join(linkHome, ".codex"), filepath.Join(realHome, ".codex")},
		{filepath.Join(linkHome, "a", "b", ".codex"), filepath.Join(realHome, "a", "b", ".codex")},
		{linkHome, realHome},
	} {
		got, err := evalSymlinksDeepest(e.fs, c.in)
		if err != nil || got != c.want {
			t.Errorf("evalSymlinksDeepest(%s) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}

	// end to end: HOME is a link, CODEX_HOME=<link>/.codex does not exist.
	e.lay.CodexDir = filepath.Join(linkHome, ".codex")
	opt := e.opts(model.ClientCodex)
	opt.Force = true
	opt.CodexTrust = true
	opt.Binary = filepath.Join(root, "ballast")
	e.write(opt.Binary, "#!/bin/sh\n")
	if err := os.Chmod(opt.Binary, 0o755); err != nil {
		t.Fatal(err)
	}
	mustInstall(t, opt)
	cfg := e.read(filepath.Join(realHome, ".codex", "config.toml"))
	if !strings.Contains(cfg, "[hooks.state.'"+filepath.Join(realHome, ".codex", "hooks.json")+":") {
		t.Errorf("trust keys must name the resolved path under the real HOME:\n%s", cfg)
	}
	if strings.Contains(cfg, linkHome) {
		t.Errorf("trust keys must not name the symlinked HOME %s:\n%s", linkHome, cfg)
	}
}
