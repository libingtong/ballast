// Package config (unit U4) loads the three rule layers, merges them, and
// serves the compiled rule set to the hook with stat-based hot reload,
// last-known-good (LKG) fallback and an on-disk cache. Plan B4;
// docs/architecture.md §3 (cache layout) and dsl-semantics §10 (merge).
//
// Hot path: yes. Imports: model, ports, sys, layout, stdlib. Rule parsing
// and compilation go through ports.RuleCompiler (dsl), injected.
//
// Layer files (LayerPaths):
//
//	org  : <ManagedDir>/ballast/rules.json      (layout.OrgRules)
//	repo : <git toplevel or cwd>/.ballast/rules.json
//	user : ~/.ballast/rules.json                 (layout.UserRules)
//
// Merge semantics (frozen):
//  1. Each layer file is parsed + validated independently. A layer with any
//     error-severity diagnostic is INVALID as a whole: use its LKG bytes
//     (status lkg, notice config_invalid) or, without LKG, treat it as empty
//     (status invalid, notice config_invalid, loud stderr).
//  2. Org rules are immutable: a repo/user rule with an org id is ignored
//     (notice rule_shadowed, audit). Org has no `locked` field.
//  3. repo vs user with the same id: the repo object wins entirely (notice
//     rule_shadowed for the user rule). No field-level merge.
//  4. User rules can only add new ids; they cannot disable or alter upper
//     layers (an `enabled:false` user rule with an upper-layer id is just
//     shadowed).
//  5. Enabled rules are taken in order org (file order) → repo → user up to
//     12; the rest get notice rule_dropped. Org rules are never dropped
//     (org > 12 is a publish-time lint error; at runtime org rules beyond 12
//     are still dropped in order and reported loudly).
//  6. Settings: per key, the first layer (org, repo, user) that sets it wins.
//  7. Generation = config.Generation(merged rules, merged settings).
//
// Hot reload: every Load stats the 3 layer files (project.json is NOT part
// of the generation; evidence reads it lazily). The 3 StatKeys
// (path, exists, size, mtime_ns, inode) are hashed with the binary version to
// a cache key; cache/stat.json maps key → generation and
// cache/rules-<generation>-<key[:8]>.json (contract v1.3: keyed so two repos
// whose rules merge to the same generation do not evict each other) stores
// {sources, merged LoadedRules, settings, notices}. A hit costs 3 stats + 2 small reads + Compile (regexes
// lazy). Cache/LKG write failures are NOT exceptions (stderr diag only).
// Cache growth is bounded: when the stat index overflows (64 keys) and is
// rebuilt, rules files it no longer references are deleted; `ballast gc`
// (GCCache) also removes unreferenced rules files older than 7 days.
// LKG bytes: cache/lkg/<layer>-<sha256(abs path)[:12]>.json, rewritten when a
// valid layer's hash changes.
//
// Acceptance (U4):
//   - hotreload: modifying any layer file changes the next Load's
//     Generation without restarting anything (scripts/hotreload.sh).
//   - Merge table testdata/merge.json (org immutability, repo-wins,
//     user-add-only, 12 cap with rule_dropped, settings precedence).
//   - Invalid repo file with LKG ⇒ LKG rules + config_invalid notice;
//     without LKG ⇒ repo layer empty + notice; other layers unaffected.
//   - Loader implements ports.RulesLoader; cold Load of 12 rules < 1ms.
//   - `config show --explain` data via Explain.
package config
