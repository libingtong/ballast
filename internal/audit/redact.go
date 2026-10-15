package audit

import (
	"regexp"
	"strings"
	"sync"
)

// redactRule is one masking pattern; repl may use $1-style references.
type redactRule struct {
	re   *regexp.Regexp
	repl string
}

var (
	redactOnce  sync.Once
	redactRules []redactRule
)

// redactPatterns are applied in order. Patterns are compiled lazily (only
// when the cheap prefilter fires) because every hook invocation links this
// package but few ever redact anything.
const secretValue = `(?:"[^"]*"|'[^']*'|[^\s,;&)}\]])+`

var redactPatterns = []struct{ pattern, repl string }{
	// PEM private key blocks; an unterminated block (clipped input) runs to the end.
	{`-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----|\z)`, `***`},
	// user:password@ in URLs.
	{`(://[^\s/:@]+:)[^\s/@]+@`, `${1}***@`},
	// Authorization headers (any scheme).
	{`(?i)(authorization["']?\s*[:=]\s*["']?)(?:(?:bearer|basic|token)\s+)?[^\s"',;]+`, `${1}***`},
	// Bearer tokens elsewhere.
	{`(?i)\b(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`, `${1}***`},
	// name=value / name: value / "name": "value" for secret-looking names.
	{`(?i)(\b[a-z0-9_.-]*(?:password|passwd|passphrase|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential)[a-z0-9_.-]*["']?\s*[=:]\s*)` + secretValue, `${1}***`},
	// --password value (space separated flag form).
	{`(?i)(--?[a-z0-9-]*(?:password|passwd|secret|token|api-?key)[a-z0-9-]*\s+)` + secretValue, `${1}***`},
	// Well-known token shapes.
	{`\b(?:AKIA|ASIA|AGPA|AIDA|AROA|AIPA|ANPA|ANVA|ABIA|ACCA)[A-Z0-9]{16}\b`, `***`},
	{`\b(?:gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{16,}|glpat-[A-Za-z0-9_-]{16,}|npm_[A-Za-z0-9]{30,})`, `***`},
	{`\bxox[abposr]-[A-Za-z0-9-]{8,}`, `***`},
	{`\b(?:sk-[A-Za-z0-9_-]{16,}|[sr]k_(?:live|test)_[A-Za-z0-9]{16,}|AIza[0-9A-Za-z_-]{30,})`, `***`},
	{`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}(?:\.[A-Za-z0-9_-]*)?`, `***`},
}

// redactTriggers are lower-case substrings; text containing none of them
// cannot match any pattern above (the URL rule is checked separately).
var redactTriggers = []string{
	"-----begin", "akia", "asia", "agpa", "aida", "aroa", "aipa", "anpa", "anva", "abia", "acca",
	"pass", "pwd", "secret", "token", "key", "credential", "bearer", "authorization",
	"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "glpat-", "npm_", "xox",
	"sk-", "sk_", "rk_", "aiza", "eyj",
}

func mayContainSecret(s string) bool {
	if strings.Contains(s, "://") && strings.Contains(s, "@") {
		return true
	}
	low := strings.ToLower(s)
	for _, t := range redactTriggers {
		if strings.Contains(low, t) {
			return true
		}
	}
	return false
}

// Redact masks secrets in s (AWS keys, private-key blocks, bearer tokens,
// "password=..."/"token=..." values, GitHub/Slack tokens) with "***".
// Minimal core redactor; the P2-a redactor replaces it behind the same signature.
// It is idempotent: Redact(Redact(s)) == Redact(s).
func Redact(s string) string {
	if s == "" || !mayContainSecret(s) {
		return s
	}
	redactOnce.Do(func() {
		redactRules = make([]redactRule, len(redactPatterns))
		for i, p := range redactPatterns {
			redactRules[i] = redactRule{re: regexp.MustCompile(p.pattern), repl: p.repl}
		}
	})
	for _, r := range redactRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}
