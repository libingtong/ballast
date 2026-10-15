// Command ballast is the single Ballast binary (hook, CLI, verify runner).
// All logic lives in internal/cli (unit U12); keep this file trivial so the
// hook's startup cost stays at Go runtime init.
//
// Owner: U12. Acceptance: see internal/cli/doc.go.
package main
