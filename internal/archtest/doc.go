// Package archtest enforces the dependency contract of docs/architecture.md
// §8–§9: allowed internal imports per package, no net/* anywhere in the
// hot path, and os/exec only in internal/snapshot/gitfallback.go among
// hot-path packages; and, for EVERY package linked into the binary
// (./internal/... and ./cmd/...), no func init() and no package-level
// regexp compilation (each hook invocation pays for all package inits).
// Owner: architect. Every unit runs it in CI.
//
// Acceptance: `go test ./internal/archtest` passes in every unit's worktree
// (it needs the go tool on PATH and no network: GOPROXY=off).
package archtest
