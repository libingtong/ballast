// Package install (unit U10) installs/uninstalls Ballast hook entries into
// client configurations, keeps the install manifest and backups, detects a
// managed deployment, renders the org (managed) settings template and
// implements `ballast doctor` checks (plan B12, v0.4 §1;
// docs/architecture.md §5 "安装目标清单").
//
// Not hot path. Imports: model, ports, sys, layout, stdlib. The Cursor
// rules file arrives pre-rendered in Options.MDC (the CLI calls
// inject.RenderMDC), so install does not import inject. No third-party
// TOML/YAML libraries: Codex config.toml and Hermes config.yaml are edited
// surgically at line level.
//
// Ownership (frozen): an entry is Ballast-owned iff
//
//	(a) its command prefix is exactly  <Binary> hook --client <id>
//	    (Claude: command == Binary && args[0:3] == ["hook","--client",<id>];
//	    others: the single command string, parsed with SplitCommand, starts
//	    with those 4 words), AND
//	(b) sha256(canonical JSON of the entry object) equals a ManagedEntry.Hash
//	    in install-manifest.json.
//
// (a) without (b) is "foreign-modified": install leaves it in place, does
// not add a duplicate for that event, and warns; uninstall keeps it and
// warns. Substring matching is forbidden.
//
// Install algorithm per Target (pure Plan, then apply):
//  1. Read the file (missing ⇒ empty document, Created=true). Unparsable ⇒
//     model.ErrSettingsParse: refuse, change nothing (exit 3).
//  2. For each native event with NativeEvent.Install: if an owned entry
//     exists, replace it IN PLACE (same index, order unchanged); else append
//     one entry to that event's list. Idempotent: a second install yields
//     byte-identical output.
//  3. Diff old/new; --dry-run prints a unified diff and stops.
//  4. Back up every file that will change into one new directory
//     ~/.ballast/backups/<UTC yyyymmddThhmmssZ>-<rand6>/ (dir 0700, files
//     0600, file name "<sha256(abs path)[:16]>-<basename ≤64B>", original paths
//     recorded in the directory's index.json so long paths never hit NAME_MAX),
//     rotate to keep the 10 newest directories.
//  5. Write each file atomically (fsync), then the manifest (fsync).
//
// Uninstall: driven by the manifest (covers clients no longer present).
// Removes exactly the entries whose hash still matches; empty event lists
// and an empty "hooks" object are removed; a file created by install that
// becomes empty ({} / {"version":1} / blank YAML) is deleted; whole owned
// files (.mdc) are deleted; Codex trust entries recorded in Flags are
// removed; features.hooks is left on and reported. Load/parse failure ⇒
// non-zero exit (never pinrule's silent `return 0`).
//
// JSON editing is surgical down to the byte (contract v1.7, jsonast.go):
// every source byte is kept in the tree (node text, the whitespace around
// each member, the text between key and value, the inside of an empty
// container, the lead/trail of the document). Install inserts only its own
// entries; a new member takes its separator from the adjacent member (blank
// lines are not copied; indentation, tabs, CRLF, "key":value spacing and the
// missing final newline follow the document). Uninstall removes exactly what
// was inserted, so install followed by uninstall reproduces the original
// file byte for byte. Containers that were empty before install come back
// as written ("[ ]", "{\n}") through their flag; a file that held only
// whitespace (also 0 bytes) is restored as it was.
//
// Structural flags (contract v1.3, internal/install/structflags.go): Plan
// records in ManagedFlag (keys "json:hooks", "json:hooks.<event>",
// "json:version", "json:root", "json:blank", "yaml:hooks",
// "yaml:hooks.<event>", "dir:kept") whether each container it
// fills existed before the first install (with its original YAML text) or
// was created by Ballast; re-installs carry the record over. Uninstall then
// deletes an emptied container only when Ballast created it (or nothing is
// recorded, the pre-v1.3 behaviour) and otherwise restores it: a user's
// empty "hooks": {} object, "hooks: {}" / "hooks: # note" header and
// "pre_tool_call: []" key come back; the Cursor "version": 1 that install
// added to an existing hooks.json is removed again once no hooks remain.
// Directories emptied by deleting a created file are pruned up to, but not
// including, the nearest directory that existed before install ("dir:kept");
// a client root created by a --force install is pruned as well.
//
// Managed deployment: if any Claude managed settings file
// (<ManagedDir>/managed-settings.json or managed-settings.d/*.json) contains
// a hook whose command basename is "ballast" (or "ballast.exe") with first
// arg "hook", user-level install for claude-code is refused
// (model.ErrManagedPresent, exit 1) to avoid double execution (plan A15).
//
// Acceptance (U10): scripts/install-e2e.sh per client with
// BALLAST_HOME/BALLAST_INSTALL_ROOT/CLAUDE_CONFIG_DIR/CODEX_HOME/HERMES_HOME
// redirected: install→uninstall round-trip diff empty; double install
// idempotent; a user-added hook survives uninstall; missing original file
// handled (created, then deleted); managed present ⇒ refusal; corrupt
// settings ⇒ refusal with no write; backups capped at 10; testdata golden
// files for each target's Plan output.
package install
