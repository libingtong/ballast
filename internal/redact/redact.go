// Package redact (P2 unit P2-REDACT; architecture v2.0, docs/p2-design.md §4)
// is the single secret redactor shared by the hook (state ring summaries),
// the audit writer (every string it persists) and `ballast report`.
//
// Layer L0: imports the standard library only (no internal package), so the
// hook — which may import only contract packages — can use it directly.
// Hot path: yes. No package-level regexp compilation and no func init():
// patterns compile lazily behind a cheap substring prefilter (archtest
// TestNoHeavyPackageInit).
//
// Frozen contract:
//   - String(s) replaces every detected secret VALUE with Mask and keeps the
//     surrounding text (key names, "Bearer ", URL scheme/host) so audit lines
//     stay readable. It never panics, is idempotent
//     (String(String(s)) == String(s)) and never grows a secret-free input.
//   - Detection classes (Kind) are a superset of the union of the two pre-P2
//     redactors (internal/audit/redact.go regexps and internal/hook/redact.go
//     word scanner); see docs/p2-design.md §4.2 for the full table.
//   - Find reports the spans String would mask (for tests and for the
//     aggregated per-kind counters in `ballast report`); it never returns
//     the secret text itself.
//
// Acceptance (P2-REDACT): testdata/samples.json has ≥ 50 positive samples
// (each with the planted secret and the expected output) and ≥ 20 negative
// samples (must stay byte-identical); zero leaks: no planted secret occurs
// in String(sample); `go test ./internal/redact -run '^$' -fuzz FuzzString
// -fuzztime 60s` finds no panic, no idempotency violation and no leak of a
// planted secret; BenchmarkClean4K (4 KiB secret-free text) ≤ 10 µs/op on
// the CI runners.
package redact

// Mask replaces a secret value.
const Mask = "***"

// Kind classifies a detected secret.
type Kind string

// Detection classes (docs/p2-design.md §4.2).
const (
	KindPrivateKey  Kind = "private_key"  // PEM private key block (unterminated block runs to the end)
	KindURLPassword Kind = "url_password" // scheme://user:PASSWORD@host
	KindAuthHeader  Kind = "auth_header"  // Authorization: <scheme> VALUE (any scheme)
	KindBearer      Kind = "bearer"       // Bearer/Basic VALUE outside headers
	KindKeyValue    Kind = "key_value"    // password=/token:/"api_key": VALUE for secret-looking key names
	KindFlag        Kind = "flag"         // --password VALUE / -p? (only secret-looking flag names)
	KindAWS         Kind = "aws"          // AKIA/ASIA/... access key ids
	KindVendorToken Kind = "vendor_token" // ghp_/github_pat_/glpat-/npm_/xox?-/sk-/sk_live_/rk_/AIza...
	KindJWT         Kind = "jwt"          // eyJ....eyJ....sig
	KindHighEntropy Kind = "high_entropy" // reserved: off by default (false positives on hashes/ids)
)

// Match is one span String would mask (byte offsets into the input).
type Match struct {
	Kind  Kind
	Start int
	End   int
}

// String masks secrets in s with Mask. See the package contract.
func String(s string) string {
	panic("unimplemented: redact.String (unit P2-REDACT)")
}

// Find returns the spans String(s) would mask, in input order,
// non-overlapping.
func Find(s string) []Match {
	panic("unimplemented: redact.Find (unit P2-REDACT)")
}

// MayContainSecret is the cheap prefilter (lower-cased substring triggers
// plus "://…@"); false ⇒ String(s) == s without compiling any pattern.
func MayContainSecret(s string) bool {
	panic("unimplemented: redact.MayContainSecret (unit P2-REDACT)")
}
