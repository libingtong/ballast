package audit

import "github.com/libingtong/ballast/internal/redact"

// Redact masks secrets in s (private-key blocks, URL passwords,
// Authorization headers, bearer/basic values, password=/token= style
// values, --password flags, AWS keys, vendor tokens, JWTs) with "***",
// keeping key names and surrounding text. Since P2 it is a one-line wrapper
// around the unified redactor (internal/redact, docs/p2-design.md §4.1);
// the audit writer applies it to Matched, every Context text and every
// string value inside Detail. It is idempotent:
// Redact(Redact(s)) == Redact(s).
func Redact(s string) string {
	return redact.String(s)
}
