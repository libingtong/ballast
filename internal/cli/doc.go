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
//	ballast verify --hook-start --request <id> --cwd <dir> --client <c> [--session <id>] [--agent <id>]
//	               [--project-dir <dir>] -- [name...]   internal (T1: spawned by the hook; model.ParseHookStartArgv)
//	ballast init [--yes] [--force] [--dry-run]
//	ballast stats [--since 7d] [--by rule|session|repo] [--rule <id> --sample N] [--json]
//	ballast rules promote <id> --to warn|ask|deny [--force]
//	ballast replay <transcript> [--client <id>] [--subagents] [--evidence auto|<file>]
//	               [--initial-state <file>] [--rules <path>] [--format json|md]
//	ballast audit lint [--json]
//	ballast report --out <file> [--since 30d] [--force]
//	ballast install --client <id>[,<id>...] [--binary <abs>] [--dry-run] [--codex-trust] [--force]
//	ballast uninstall [--client <id>|--all] [--dry-run]
//	ballast doctor [--json]
//	ballast org render [--client claude-code] [--binary <abs>] [--home-root <dir>]... [--protect <path>]... [--part managed-settings|org-json] [--write --i-am-admin]
//	ballast gc                                      internal (detached from SessionStart)
//	ballast version
//
// Hot-path rule: Main must dispatch "hook" BEFORE constructing anything
// not needed by the hook (no flag.FlagSet for other commands, no reading of
// config, no package-level regexp). `--help`/`-h` on any command prints help
// and exits 0 without side effects.
//
// Implementation notes (U12a):
//
//   - Main dispatches "hook" before anything else; WireHook performs no
//     I/O beyond locating the running binary, and every other command is
//     looked up through a switch (no package-level tables or regexps).
//     Help is detected before the runtime context (layout, working
//     directory) is built, so --help touches nothing.
//   - Flags use the stdlib flag package with interspersed positional
//     arguments ("--" ends flag parsing); usage errors exit 2 with the
//     command's help on stderr.
//   - lint/test-rules take files and directories (directories expand to their
//     *.json files, non-recursive); without paths they use the existing
//     layer files of the rule stack in effect.
//   - verify --status aggregates several required entries into the most
//     severe exit code (running 6, infra 5, signal 4, timeout 3, fail 1,
//     superseded 7, no record 11). verify --check maps the FreshResult as
//     the e2e freshness table does: true 0; false 10 (no_record 11);
//     unknown 12 (job_running 13).
//   - install leaves binary resolution to install.Options.Binary == "" (os.Executable,
//     symlinks resolved, temporary/build directories refused with exit 2).
//   - doctor prints the tier (T0/T1), one row per client (detected, installed,
//     worst check status), every check, and restates that CI is the final
//     arbiter.
//
// P2 wiring (docs/p2-design.md §3.6; wire.go):
//
//   - newCoreWith runs tier.LockState first, then layout.ResolveWith. On a
//     redirect-locked host (root-owned org.json declaring T1) the redirect
//     variables are ignored and <home> comes from tier.LockedHome (passwd, or
//     the org.json home_root rule) with its own 50 ms limit; a home that
//     cannot be determined is an error (the hook fails open per B7: exit 1,
//     empty stdout). `ballast doctor` alone falls back to $HOME (redirects
//     still ignored) so it can report tier.locked_home.
//   - WireHook fills hook.Deps.Tier (tier.Prober, lazy), IDs (always), and,
//     only when locked, RedirectsIgnored (the variables that are set, in the
//     order BALLAST_HOME, BALLAST_MANAGED_DIR, BALLAST_INSTALL_ROOT),
//     ProjectSandbox (tier.ProjectChecker) and WeakenedProject
//     (tier.EffectiveWeakenedProject). runner.Options.Tier gets the same
//     prober.
//   - verify --status <id> falls back to runner.RequestStatus when no job has
//     that id (no request file either: 11); verify --hook-start runs
//     runner.StartRequest. report --out runs report.Build / report.Write.
//     org render prints install.OrgRenderFull (stdout is one pure JSON
//     document: --part managed-settings (default) or org-json; the org.json
//     risk notes go to stderr); doctor gets Self, Cwd and the sandbox probe.
//     gc also runs runner.GCRequests.
//   - Binaries built with `-tags ballast_fault` read BALLAST_TEST_LOCKED,
//     BALLAST_TEST_WEAKENED and BALLAST_TEST_TIER (faults_on.go, bench
//     scenarios 12-14); production builds ignore them (faults_off.go).
//
// End-to-end tests (internal/cli/*_e2e_test.go) build the binary in TestMain
// and run it as a subprocess with a fully redirected environment; they cover
// the integration acceptance list of architecture §8.3 (U3/U11 content files,
// U8 commitform.json through bashparse, hook protocols for the four clients,
// verify → status → check, install round trips).
//
// Acceptance (U12): scripts/accept-m1.sh … accept-m4.sh, regress.sh,
// fault.sh, concurrency.sh, hotreload.sh, freshness.sh, matrix.sh,
// replay.sh, install-e2e.sh; scripts/bench (ported from proto/m0c/bench)
// enforces p95 ≤ 30ms for scenarios 1–11; .github/workflows/ci.yml runs
// on ubuntu, macos, windows (Git Bash) with `go test ./... -race`.
//
// Acceptance (P2-CLI): scripts/accept-p2.sh (docs/p2-design.md §10), scripts/boundary-t0.sh,
// scripts/boundary-t1.sh (Docker), scripts/bench.sh scenarios 12-14 and C-configchange.
package cli
