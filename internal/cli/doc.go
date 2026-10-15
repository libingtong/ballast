// Package cli (unit U12) implements every `ballast` subcommand: argument
// parsing (stdlib flag), wiring of real implementations into the ports,
// output formatting, and exit codes (docs/architecture.md §7).
//
// Commands (frozen names and flags; see architecture §7 for exit codes):
//
//	ballast hook --client <id> --event <native>
//	ballast lint [--layer org|repo|user] [--client <id>|all] [--stack] [--examples] [--json] <file|dir>...
//	ballast test-rules [--json] <file|dir>...
//	ballast config show [--explain] [--json]
//	ballast verify [name...]                        start background job(s)
//	ballast verify --status [job|name] [--json]
//	ballast verify --check [--commit|--worktree]    for git pre-commit; --commit reads
//	                                                GIT_INDEX_FILE (relative ⇒ against cwd) into
//	                                                evidence.CheckOptions.IndexFile
//	ballast verify --foreground <name>
//	ballast verify --run-job <job_id>               internal (detached runner)
//	ballast init [--yes] [--force] [--dry-run]
//	ballast stats [--since 7d] [--by rule|session|repo] [--rule <id> --sample N] [--json]
//	ballast rules promote <id> --to warn|ask|deny [--force]
//	ballast replay <transcript> [--client <id>] [--subagents] [--evidence auto|<file>]
//	               [--initial-state <file>] [--rules <path>] [--format json|md]
//	ballast audit lint [--json]
//	ballast install --client <id>[,<id>...] [--binary <abs>] [--dry-run] [--codex-trust] [--force]
//	ballast uninstall [--client <id>|--all] [--dry-run]
//	ballast doctor [--json]
//	ballast org render [--client claude-code] [--binary <abs>] [--write --i-am-admin]
//	ballast gc                                      internal (detached from SessionStart)
//	ballast version
//
// Hot-path rule: Main must dispatch "hook" BEFORE constructing anything
// not needed by the hook (no flag.FlagSet for other commands, no reading of
// config, no package-level regexp). `--help`/`-h` on any command prints help
// and exits 0 without side effects.
//
// Acceptance (U12): scripts/accept-m1.sh … accept-m4.sh, regress.sh,
// fault.sh, concurrency.sh, hotreload.sh, freshness.sh, matrix.sh,
// replay.sh, install-e2e.sh; scripts/bench (ported from proto/m0c/bench)
// enforces p95 ≤ 30ms for scenarios 1–11; .github/workflows/ci.yml runs
// on ubuntu, macos, windows (Git Bash) with `go test ./... -race`.
package cli
