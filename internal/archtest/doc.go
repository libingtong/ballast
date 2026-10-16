// Package archtest enforces the dependency contract of docs/architecture.md
// §8–§9: allowed internal imports per package, no net/* anywhere in the
// hot path, and os/exec only in internal/snapshot/gitfallback.go among
// hot-path packages; and, for EVERY package linked into the binary
// (./internal/... and ./cmd/...), no func init() and no package-level
// regexp compilation (each hook invocation pays for all package inits).
// Since architecture v2.0 (P2) the whole cmd/ballast binary must not link
// net/* (TestBinaryNoNet), and the P2 packages redact and tier are hot
// path (no net, no os/exec even transitively); sandbox is not hot path and
// only the runner may import it.
// Owner: architect. Every unit runs it in CI.
//
// Acceptance: `go test ./internal/archtest` passes in every unit's worktree
// (it needs the go tool on PATH and no network: GOPROXY=off).
package archtest
