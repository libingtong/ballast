package hook

import "strings"

// redact masks the obvious secrets in a ring-buffer summary before it is
// persisted in the state file: sensitive key=value words, bearer/basic
// credentials, well-known token prefixes and user:password@ URL parts. The
// audit writer applies its own (broader) redaction to everything it writes.
func redact(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	maskNext := false
	start := -1
	flush := func(end int) {
		w := s[start:end]
		start = -1
		switch {
		case maskNext:
			maskNext = false
			b.WriteString("***")
			return
		case strings.EqualFold(w, "bearer") || strings.EqualFold(w, "basic"):
			maskNext = true
			b.WriteString(w)
			return
		}
		b.WriteString(redactWord(w))
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		sp := c == ' ' || c == '\t' || c == '\n' || c == '\r'
		switch {
		case sp && start >= 0:
			flush(i)
			b.WriteByte(c)
		case sp:
			b.WriteByte(c)
		case start < 0:
			start = i
		}
	}
	if start >= 0 {
		flush(len(s))
	}
	return b.String()
}

// sensitiveKeys match as substrings of the key (DB_PASSWORD, x-api-key);
// exactKeys only when the whole key is equal ("author" is not "auth").
var (
	sensitiveKeys = []string{
		"password", "passwd", "secret", "token", "api_key", "apikey", "api-key",
		"access_key", "private_key", "authorization", "credential",
	}
	exactKeys = []string{"pwd", "auth", "pass"}
)

func sensitiveKey(key string) bool {
	for _, k := range sensitiveKeys {
		if strings.Contains(key, k) {
			return true
		}
	}
	for _, k := range exactKeys {
		if key == k {
			return true
		}
	}
	return false
}

func redactWord(w string) string {
	if i := strings.Index(w, "://"); i >= 0 {
		rest := w[i+3:]
		if at := strings.IndexByte(rest, '@'); at > 0 && strings.Contains(rest[:at], ":") {
			w = w[:i+3] + "***@" + rest[at+1:]
		}
	}
	if i := strings.IndexAny(w, "=:"); i > 0 && i < len(w)-1 {
		key := strings.ToLower(strings.TrimLeft(w[:i], "-"))
		if sensitiveKey(key) {
			return w[:i+1] + "***"
		}
	}
	core := strings.Trim(w, `"'`)
	if looksLikeToken(core) {
		return strings.Replace(w, core, "***", 1)
	}
	return w
}

func looksLikeToken(w string) bool {
	for _, p := range []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "xoxa-", "xoxb-", "xoxp-", "xoxr-", "xoxs-", "sk-", "sk_live_", "sk_test_"} {
		if strings.HasPrefix(w, p) && len(w) >= len(p)+8 {
			return true
		}
	}
	if (strings.HasPrefix(w, "AKIA") || strings.HasPrefix(w, "ASIA")) && len(w) == 20 && isUpperAlnum(w) {
		return true
	}
	return strings.HasPrefix(w, "eyJ") && len(w) >= 40 && strings.Count(w, ".") == 2
}

func isUpperAlnum(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
