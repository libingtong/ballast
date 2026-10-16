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
//
// Implementation notes (P2-REDACT):
//   - Prefilter: one pass over the input hashes every lower-cased trigram
//     into a small table of anchors (see scan.go); each anchor belongs to
//     one rule family, so a clean input costs a single tight loop and no
//     pattern is compiled or run. A "://" followed later by '@' enables the
//     URL rule. MayContainSecret is exactly "some anchor or URL hint hit";
//     every rule requires its anchor, which is what makes
//     !MayContainSecret(s) ⇒ String(s) == s hold.
//   - Rules produce VALUE spans only (rules.go). Spans of all rules are
//     merged (touching spans too) and replaced by one Mask each.
//   - key_value, flag and Authorization scanning are hand-written linear
//     scanners (no backtracking, no quadratic behaviour on long lines);
//     the remaining rules use lazily compiled RE2 patterns.
//   - Values that are at most 6 decimal digits, or true/false/null, are
//     left alone for key_value (max_tokens: 4096). Keys whose name contains
//     "tokeniz" (tokenizer) do not count as token keys.
//   - high_entropy is reserved and never produced: git hashes and uuids make
//     it too noisy to enable by default.
//
// Known gaps (documented, by design): mysql -pPASSWORD (a short flag glued
// to its value cannot be told from an ordinary -p); .netrc "password X"
// with space separation; environment variables with arbitrary names
// (FOO=<value>). Structured values: under a strong key the scalar elements
// of a [...] or (...) list ("password=[x]", {"password": ["x"]}, at most 64
// per list) are masked; a {...} object, a list under a weak key
// (pwd/pass/auth), and nested lists are masked only through the rules of
// their own elements (a vendor token, a nested "password": ...).
// Differences from the two pre-P2 implementations, all registered in
// docs/p2-design.md §4.2 / §11: a bare "Bearer abc123" shorter than 8
// characters outside an Authorization header is not masked (prose such as
// "bearer token" would be); "Authorization: Bearer" with the token on the
// NEXT line is not masked; an unquoted value is cut at ',' and ';'
// ("password=abc,def123" leaves "def123": the separators are ambiguous in
// connection strings and argument lists).
package redact

import "strings"

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

// maxPasses bounds the fixed-point iteration of String.
const maxPasses = 16

// String masks secrets in s with Mask. See the package contract.
//
// Masking is repeated until the text is stable (normally one pass finds
// something and the second finds nothing new): replacing a value by Mask can
// move a token boundary and expose a secret the first pass could not see
// (for example "ghp_x-password y" once the vendor token is gone), and the
// contract is that String is idempotent.
func String(s string) string {
	for pass := 0; pass < maxPasses; pass++ {
		ms := detect(s)
		if len(ms) == 0 {
			return s
		}
		next := mask(s, ms)
		if next == s {
			return s
		}
		s = next
	}
	return s
}

func mask(s string, ms []Match) string {
	var b strings.Builder
	b.Grow(len(s) + 2*len(ms))
	last := 0
	for _, m := range ms {
		b.WriteString(s[last:m.Start])
		b.WriteString(Mask)
		last = m.End
	}
	b.WriteString(s[last:])
	return b.String()
}

// Find returns the spans String(s) would mask, in input order,
// non-overlapping. They are the spans of String's first pass (String repeats
// until the text is stable, so a later pass can mask slightly more in
// pathological inputs). Values that are already Mask are reported as well,
// which lets `ballast report` count the redactions recorded in audit text.
func Find(s string) []Match {
	return detect(s)
}

// MayContainSecret is the cheap prefilter (lower-cased substring triggers
// plus "://…@"); false ⇒ String(s) == s without compiling any pattern.
func MayContainSecret(s string) bool {
	return scanFlags(s) != 0 || urlHint(s)
}
