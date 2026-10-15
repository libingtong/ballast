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
// Acceptance (U8): matrix.sh stub mode (9 stacks × pass/fail) records the
// right outcome; supersede marks the old record; timeout kills the whole
// process group; Spawner children survive the parent's exit (unix setsid,
// windows DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP).
package runner
