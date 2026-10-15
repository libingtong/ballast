// Package paths (unit U1) normalizes file paths across POSIX, native
// Windows, UNC and Git-Bash forms and compiles glob lists (doublestar
// dialect with list-level negation). Plan B11, B1 (`path` matcher),
// docs/dsl-semantics.md §5.2.
//
// Hot path: yes. Imports allowed: model, ports, sys, doublestar, stdlib
// (no net/*, no os/exec).
//
// Internal form ("norm"): absolute, '/' separators, cleaned, lower-case
// drive letter ("c:/Users/x"), UNC as "//server/share/...", Git-Bash
// "/c/Users" → "c:/Users" (only when GOOS=windows or the path came from a
// Windows client), "~" and "~/..." expanded against PathContext.Home. Case
// is PRESERVED in the string; comparisons fold case when GOOS is windows or
// darwin (IsCaseInsensitive).
//
// Candidates on POSIX (contract v1.3): besides the lexically folded form,
// a path containing ".." also yields its PHYSICAL form, where ".." applies
// to the directory reached so far (symlinks resolved), as the kernel and
// shells do: "link/../x" is "<target of link>/../x". Windows (Win32 folds
// ".." lexically) and Windows-client drive paths ("c:/x/../y" seen on a
// POSIX host) get the lexical form only.
//
// Glob lists: patterns OR-combined; a pattern starting with "!" is a
// negation; GlobSet.Match(candidates...) is true iff SOME candidate matches
// ≥1 positive pattern AND NO candidate matches any negative pattern (order
// irrelevant; candidates are the abs/rel/realpath forms of ONE target). A list with only negations is
// invalid (lint error "glob-only-negation"). Patterns without '/' match the
// basename of any candidate ("*.md" ≡ "**/*.md"). Patterns beginning with
// "~/" are expanded with Home before matching. Case folding: both pattern
// and candidate lower-cased when caseInsensitive. Service.CompileGlobs
// (ports.PathService, the only entry point other units use) expands "~/"
// against BOTH the literal home and its realpath; the package-level
// CompileGlobs(GlobOptions) knows only the literal home and is an
// implementation helper (v1.3).
//
// Acceptance (U1):
//   - testdata/normalize.json table passes on every GOOS (table rows carry
//     their own "goos" so they run everywhere).
//   - Candidates includes the EvalSymlinks form: a symlink/junction pointing
//     into ~/.ballast matches "~/.ballast/**" (plan B11 double check); a
//     non-existent file resolves through its nearest existing parent.
//   - Equal("C:\\Users\\X\\a.go", "c:/users/x/A.GO", windows) == true;
//     Equal on linux is case-sensitive.
//   - GlobSet: {"**/*.go","!vendor/**"} matches "a/b.go", not "vendor/x.go";
//     {"!x"} fails to compile.
//   - Service implements ports.PathService (compile-time assertion).
//   - Fuzz: Normalize never panics and is idempotent (Normalize(Normalize(p)) == Normalize(p)).
package paths
