// Package ports declares the frozen service interfaces between development
// units (docs/architecture.md §8 "跨单元只能依赖的接口").
//
// Rules:
//   - A unit may import another unit's package ONLY for the functions and
//     types listed in architecture §8; for everything that participates in
//     the hook pipeline it depends on these interfaces and receives the
//     implementation by injection (wired in internal/cli by U12).
//   - Each implementing package carries a compile-time assertion such as
//     `var _ ports.StateStore = (*Store)(nil)`, so signature drift breaks the build.
//   - The package depends on model and the standard library only.
//
// Owner: architect (contract).
package ports
