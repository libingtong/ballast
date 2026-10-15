// Package layout is the frozen on-disk layout of Ballast: the ~/.ballast
// tree, the repo-local .ballast directory, the managed (org) directory and
// each client's configuration root. See docs/architecture.md §3.
//
// Owner: architect (contract, implemented). Every unit resolves paths through
// Layout; nobody concatenates "~/.ballast/..." by hand.
//
// Environment overrides (tests and e2e only; never documented for end users
// except BALLAST_HOME):
//
//	BALLAST_HOME          replaces ~/.ballast
//	BALLAST_INSTALL_ROOT  replaces ~ as the parent of .claude/.codex/.cursor/.hermes
//	BALLAST_MANAGED_DIR   replaces the OS managed-settings directory
//	CLAUDE_CONFIG_DIR     Claude Code's own override (honoured, plan v0.4 §1)
//	CODEX_HOME            Codex's own override (honoured)
//	HERMES_HOME           Hermes override (honoured)
package layout
