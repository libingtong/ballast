// Package evidence (unit U8) stores verify records and job status, answers
// verify_fresh / verify_failed_since_read, and backs `verify --status` and
// `verify --check` (plan B3; docs/architecture.md §3, §7).
//
// Hot path: Provider/View yes. Imports: model, ports, sys, layout,
// snapshot, stdlib.
//
// Files:
//
//	evidence/<repo_inst>/<name>/<t_start unix ns, 20 digits>-<job_id>.json  VerifyRecord, fsync, 0600
//	jobs/<repo_inst>/<name>/<job_id>.json                                     JobStatus (atomic, no fsync)
//	jobs/<repo_inst>/<name>/<job_id>.log                                      runner stdout+stderr
//
// "Latest record of a name" = lexicographically greatest file name in the
// name directory (zero-padded t_start makes it chronological): one ReadDir
// + one ReadFile on the hot path.
//
// Laziness: Provider.Open(repo, deadline) does no I/O. project.json is read
// through Options.Project only when a view method needs it, memoized per
// toplevel within the view. LastFailureAfter and FinishedSince never read
// project.json.
//
// verify_fresh(names) (FreshQuery → FreshResult), frozen:
//  1. repo nil ⇒ unknown(not_a_repo).
//  2. Target repo: Kind commit ⇒ ParseCommitForm(Git, Cwd); unparseable ⇒
//     unknown(unparseable_commit). Form.Dir set and Locator.Locate(Dir)
//     yields a different Inst ⇒ the TARGET repo is that repo (ErrNotRepo ⇒
//     unknown(not_a_repo)); otherwise the event repo. Kind worktree ⇒ the
//     event repo.
//  3. Project of the TARGET repo (Options.Project.LoadProject(target
//     Toplevel)): nil ⇒ unknown(no_project); error ⇒
//     unknown(project_invalid); Names empty and no required entries ⇒
//     unknown(no_required). Names nil ⇒ the target project's required list;
//     exempt = the target project's exempt.
//  4. Any name with a RUNNING job under the target repo_inst (JobStatus
//     running, PID alive, before Deadline) ⇒ unknown(job_running) — never
//     fresh while running.
//  5. C: Kind commit ⇒ snapshot.TreeC(target, form, exempt); Kind worktree
//     ⇒ snapshot.TreeS. Snapshot ErrDeadline ⇒ unknown(fallback_timeout);
//     other errors ⇒ unknown(snapshot_error).
//  6. For each name: latest record under the target repo_inst (by t_start,
//     regardless of outcome); none ⇒ false(no_record); !ValidPass ⇒
//     false(not_valid_pass); S_end ≠ C ⇒ false(stale). All pass ⇒
//     true(fresh). The first failing name decides Cause; Stale lists all.
//
// Check (`verify --check`, CheckOptions): repo = Locate(cwd) (ErrNotRepo ⇒
// unknown(not_a_repo) ⇒ exit 12); steps 3–6 on that repo with C =
// F(CheckOptions.IndexFile or <GitDir>/index) in commit mode (plain form on
// the given index) or S in worktree mode. No GIT_INDEX_FILE handling happens
// inside this package: the CLI reads the variable and passes IndexFile.
// The hot path (Locate, FreshQuery) never looks at GIT_INDEX_FILE.
//
// LastFailureAfter(t): latest record over all names of the EVENT repo_inst
// with outcome fail|timeout|signal and t_end > t.
//
// ParseCommitForm: flags -a/--all ⇒ all; --amend sets Amend (same C);
// -i/--include + paths ⇒ paths(include); -o/--only or bare paths ⇒ paths;
// "--" separates paths; -m/-F/-C/-c/--author/--date/... consume a value;
// --dry-run/--porcelain ⇒ still commit form; --interactive/-p/--patch ⇒
// unparseable; dynamic words ⇒ unparseable; --fixup/--squash values consumed.
// Dir = clean(join(cwd, Git.Cwd)) when Git.Cwd is set. It sees only the
// commit node: a preceding `git add ... &&` in the same command has NOT run
// at PreToolUse time, so `git add -A && git commit -m x` is judged on the
// current index (documented limitation, dsl-semantics §14; the default
// evidence-before-commit message tells the agent to run them separately).
//
// GC (`ballast gc`): keep records younger than 30 days, but always the
// latest record per name; delete finished job files older than 7 days.
//
// Acceptance (U8, unit level — no other unit's implementation needed):
//   - testdata/commitform.json: every row's "git" (a model.GitInvocation,
//     constructed directly by the test) through ParseCommitForm(git,
//     "/repo") equals "want". The "cmd" column is NOT parsed here; U12 adds
//     the end-to-end test bashparse → ParseCommitForm over the same rows.
//   - testdata/freshness.json against local `git init` fixture repos (skip
//     when git is missing; CI treats missing git as failure): the 11
//     counter-examples are stale/unknown with the listed check codes (13
//     for a running job), the 3 positives fresh, the S/C consistency row;
//     the "check" column is produced as described in that file's comment
//     (real pre-commit hook capturing GIT_INDEX_FILE).
//   - Provider implements ports.EvidenceProvider; Open performs no I/O
//     (FS fake records zero calls).
//
// Implementation notes (U8b; additive, the contract above stays in force):
//
//   - Options.Env (optional) feeds snapshot (global git config, TMPDIR); nil
//     means the process environment.
//   - Store also exposes JobPath, LogPath, SpecPath (the runner's per-job
//     sidecar) and SupersededPath (the runner's supersede marker); GC removes
//     both with the job. ReadJob and JobActive are exported as well.
//   - A running job whose PID is not recorded yet (the detached child writes
//     its own PID when it starts) counts as running for JobStartGrace (30s);
//     afterwards only a live PID before the job Deadline counts.
//   - A latest record that cannot be read or decoded is no evidence:
//     false(not_valid_pass), never a pass from an older record.
//   - FreshQuery.Kind "" is treated as worktree; any other unknown kind is
//     unknown(unparseable_commit).
//   - ParseCommitForm leaves CommitForm.Reason empty (the acceptance table
//     compares whole forms). Unknown options, --git-dir/--work-tree, a -a
//     with paths and -i/-o without paths are unparseable.
//   - DEVIATION, pending U0/architect confirmation (it relaxes the frozen
//     "dynamic words => unparseable" rule, which still holds for the
//     subcommand, -C, options and paths): a dynamic word that is the value of
//     a value-taking option (-m "$(...)") is accepted, since it cannot change
//     which tree is committed. The deviation only turns unknown into false
//     (never into a pass), and the pre-commit --check on the real index
//     remains the backstop for an unquoted -m $MSG split into several words.
//   - Check resolves a relative CheckOptions.IndexFile against cwd.
//   - FinishedSince reads only files whose t_start is within 25h before
//     `since` (a record cannot run longer than the 86400s timeout cap).
//
// Integration acceptance (U12): scripts/freshness.sh with the real binary
// and an installed pre-commit hook running `ballast verify --check`.
package evidence
