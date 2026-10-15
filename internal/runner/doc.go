// Package runner (unit U8) starts and executes background verify jobs and
// provides the detached-process Spawner (plan B3 "后台运行与结果记录").
//
// NOT hot path (may import os/exec). The hook reaches it only through
// ports.Spawner.
//
// Frozen protocol:
//   - Start(name): resolve repo + project.json; for each requested name
//     (none ⇒ every required entry): mark running jobs of the same
//     (repo_inst, name) superseded (send SIGTERM / TerminateProcess, write a
//     VerifyRecord outcome=superseded), create JobStatus{running}, spawn
//     `<self> verify --run-job <job_id>` detached (new session/process
//     group, stdin=/dev/null, stdout/stderr → job .log), print
//     "job=<id> name=<name>" per job and return immediately (exit 0).
//   - RunJob(job_id) (inside the detached child): S_start = TreeS (fallback
//     allowed, no deadline) → run argv in repo root (+spec.Cwd) with
//     timeout_s (default 1800; kill process group on timeout) → S_end →
//     VerifyRecord{tier T0, outcome pass|fail|timeout|signal|infra,
//     argv_hash = sha256(json(argv)), project_json_hash = sha256(file
//     bytes)} written with fsync → audit "verify" line → JobStatus done.
//     The runner never holds the hot-path state lock.
//   - Foreground(name): same as RunJob but synchronous in the current
//     process, output streamed; exit code per `verify --status` mapping.
//   - Job ids: UTC "20060102T150405" + "-" + IDGen.NewID(3).
//   - Session/agent attribution: BALLAST_SESSION_ID / BALLAST_AGENT_ID
//     environment variables when set (unverified whether clients export
//     a session id to tool subprocesses; see open questions).
//
// Implementation notes (U8b; additive):
//
//   - The detached child only receives the job id, so Start stores the
//     resolved entry (repo root, argv, env, cwd, exempt, project hash,
//     session/agent) in a sidecar next to the job file
//     (evidence.Store.SpecPath). RunJob runs exactly that entry.
//   - The child records its own PID in the job file when it starts; Start does
//     not rewrite the file after spawning (it would race with a fast job).
//   - Start returns the started jobs; the CLI prints Started.String()
//     ("job=<id> name=<name>"). StatusExitCode maps a record to the
//     `verify --status` code. Options.Output (default stdout) receives the
//     command output in Foreground.
//   - Supersede order: job file done/superseded, then the superseded record
//     (same file name the old runner would use), then SIGTERM to the old
//     runner (TerminateProcess on Windows). A running job also polls its job
//     file once a second and stops itself when it sees the supersede mark; it
//     then writes nothing. A job superseded before its child started never
//     runs its command.
//   - Runner identity: before recording its pid the runner opens
//     evidence.Store.LockPath(job) and holds an exclusive lock on it (flock;
//     LockFileEx on Windows) until it exits; the kernel drops the lock when the
//     runner dies. A pid in a job file may have been recycled by an unrelated
//     process after a runner crash, so a superseding Start signals the pid
//     only when it finds that lock held. Missing lock file, a lock it can take
//     or any doubt means the runner is gone: the job is only marked
//     superseded. A runner that cannot get its lock (lock file unwritable)
//     runs unlocked and is stopped through the supersede marker alone.
//   - JobStatus.Deadline is started_at + timeout_s + evidence.JobDeadlineSlack
//     (10 minutes), not started_at + timeout_s: the command's timer starts only
//     after the start snapshot and the kill grace and the end snapshot follow
//     it, and until the Deadline passes the job counts as running (verify_fresh
//     job_running, supersede, GC).
//   - Timeout / stop: SIGTERM to the command's process group, SIGKILL after 3s
//     (a Job Object on Windows). The command runs with the child
//     environment (Options.Env plus the entry's env) and argv[0] is resolved
//     against that environment's PATH. A command that cannot be started is
//     outcome infra.
//   - A failed audit append is noted in the job log and is not an error (the
//     record is already durable).
//   - The command output goes to <job_id>.log between a "# ballast verify"
//     header and a "# ballast outcome=" footer.
//
// Acceptance (U8): matrix.sh stub mode (9 stacks × pass/fail) records the
// right outcome; supersede marks the old record; timeout kills the whole
// process group; Spawner children survive the parent's exit (unix setsid,
// windows DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP).
package runner
