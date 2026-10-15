package model

import "time"

// ManifestVersion is the install-manifest.json schema version.
const ManifestVersion = 1

// InstallManifest is ~/.ballast/install-manifest.json (plan B12, v0.4 §1).
// It is the source of truth for uninstall (covers clients that are no longer
// installed on the machine).
type InstallManifest struct {
	V       int            `json:"v"`
	Binary  string         `json:"binary"` // absolute path written into hook commands
	Entries []ManagedEntry `json:"entries"`
	Files   []ManagedFile  `json:"files,omitempty"`
	Flags   []ManagedFlag  `json:"flags,omitempty"`
	Updated time.Time      `json:"updated"`
	Backups []BackupRecord `json:"backups,omitempty"` // most recent first, ≤10 kept on disk
}

// ManagedEntry is one hook entry Ballast inserted into a client config.
type ManagedEntry struct {
	Client ClientID `json:"client"`
	File   string   `json:"file"`   // absolute config path
	Native string   `json:"native"` // native event key
	// Key locates the entry: for nested JSON "hooks.<Event>[i].hooks[j]";
	// for Cursor "hooks.<event>[i]"; for Hermes "hooks.<event>[i]".
	Key string `json:"key"`
	// Hash = sha256 of the canonical JSON of the entry object as written.
	Hash string `json:"hash"`
	// Command/Args as written (Claude uses both; others a single command string).
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// ManagedFile is a whole file Ballast owns (Cursor ballast.mdc) or created.
type ManagedFile struct {
	Client  ClientID `json:"client"`
	Path    string   `json:"path"`
	Hash    string   `json:"hash"`
	Created bool     `json:"created"` // created by install ⇒ delete on uninstall when empty
	Whole   bool     `json:"whole"`   // fully owned (delete on uninstall)
}

// ManagedFlag is a config switch Ballast turned on (Codex [features] hooks=true,
// Codex trust entries). Uninstall removes trust entries; features flags are
// never turned off automatically (only reported).
type ManagedFlag struct {
	Client       ClientID `json:"client"`
	File         string   `json:"file"`
	Key          string   `json:"key"`   // e.g. "features.hooks" or "hooks.state.'<path>:pre_tool_use:0:0'"
	Value        string   `json:"value"` // as written
	SetByBallast bool     `json:"set_by_ballast"`
}

// BackupRecord is one backup directory ~/.ballast/backups/<ts>-<rand>/.
type BackupRecord struct {
	Dir   string    `json:"dir"`
	At    time.Time `json:"at"`
	Files []string  `json:"files"` // original absolute paths backed up
}
