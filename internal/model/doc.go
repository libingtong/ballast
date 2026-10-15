// Package model holds the frozen, cross-unit data contract of Ballast.
//
// Every type here is shared by two or more development units (U1–U12, see
// docs/architecture.md §8). The package depends on the standard library only
// and contains no I/O. Small pure helpers (three-valued logic, action
// severity, comparison evaluation, the client capability tables) are
// implemented here because they ARE the contract; everything else lives in
// the owning unit's package.
//
// Change policy: fields may be added (append-only, with omitempty JSON tags)
// without a contract revision. Renaming/removing a field, changing a JSON
// tag, or changing the meaning of a constant requires a contract revision
// recorded in docs/architecture.md §12.
//
// Acceptance (owner: architect / U0):
//   - `go test ./internal/model` decodes every file in schema/examples/ with
//     DisallowUnknownFields into RuleFile (keeps Go types and
//     schema/rules.v1.json in lock-step).
//   - Tri truth tables (Kleene logic) and Cmp evaluation are table-tested.
package model
