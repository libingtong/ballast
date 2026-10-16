package redact

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// lazyRE is a regexp compiled on first use (never at package init).
type lazyRE struct {
	once sync.Once
	pat  string
	re   *regexp.Regexp
}

func (l *lazyRE) get() *regexp.Regexp {
	l.once.Do(func() { l.re = regexp.MustCompile(l.pat) })
	return l.re
}

// spans appends the last capture group (or the whole match when the pattern
// has no group) of every match as a Match of the given kind.
func (l *lazyRE) spans(s string, kind Kind, out []Match) []Match {
	for _, ix := range l.get().FindAllStringSubmatchIndex(s, -1) {
		a, b := ix[0], ix[1]
		if len(ix) > 2 {
			a, b = ix[len(ix)-2], ix[len(ix)-1]
		}
		if a >= 0 && b > a {
			out = append(out, Match{Kind: kind, Start: a, End: b})
		}
	}
	return out
}

var (
	// PEM private key; group 1 is the body. An unterminated block (clipped
	// input) runs to the end of the text.
	rePEM = lazyRE{pat: `-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----([\s\S]*?)(?:-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----|\z)`}
	// scheme://user:PASSWORD@host (user may be empty).
	reURL = lazyRE{pat: `://[^\s/:@]*:([^\s/@]+)@`}
	// Bearer VALUE outside Authorization headers.
	reBearer = lazyRE{pat: `(?i)\bbearer[ \t]+([A-Za-z0-9._~+/=-]{8,})`}
	// Basic VALUE (base64); see basicLooksSecret for the prose filter.
	reBasic = lazyRE{pat: `(?i)\bbasic[ \t]+([A-Za-z0-9+/]{8,}={0,2})`}
	reAWS   = lazyRE{pat: `\b(?:AKIA|ASIA|AGPA|AIDA|AROA|AIPA|ANPA|ANVA|ABIA|ACCA)[A-Z0-9]{16}\b`}
	reJWT   = lazyRE{pat: `\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}(?:\.[A-Za-z0-9_-]*)?`}
	// Slack webhook path: only the secret path after /services/ is masked.
	reSlackHook = lazyRE{pat: `hooks\.slack\.com/services/([A-Za-z0-9/]{8,})`}
	reVendor    = lazyRE{pat: `\b(?:` +
		`gh[pousr]_[A-Za-z0-9]{8,}` +
		`|github_pat_[A-Za-z0-9_-]{8,}` +
		`|glpat-[A-Za-z0-9_-]{16,}` +
		`|npm_[A-Za-z0-9]{30,}` +
		`|xox[abposr]-[A-Za-z0-9-]{8,}` +
		`|sk-[A-Za-z0-9_-]{8,}` +
		`|[sr]k_(?:live|test)_[A-Za-z0-9]{8,}` +
		`|whsec_[A-Za-z0-9]{16,}` +
		`|AIza[0-9A-Za-z_-]{30,}` +
		`|ya29\.[0-9A-Za-z._-]{8,}` +
		`|hf_[A-Za-z0-9]{30,}` +
		`|pypi-AgEI[A-Za-z0-9_-]{16,}` +
		`|dckr_pat_[A-Za-z0-9_-]{16,}` +
		`|do[por]_v1_[A-Za-z0-9]{16,}` +
		`|SG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}` +
		`)`}
)

var awsPrefixes = [...]string{"AKIA", "ASIA", "AGPA", "AIDA", "AROA", "AIPA", "ANPA", "ANVA", "ABIA", "ACCA"}

// detect returns the merged, ordered value spans of every rule.
func detect(s string) []Match {
	if len(s) < 3 {
		return nil
	}
	f := scanFlags(s)
	url := urlHint(s)
	if f == 0 && !url {
		return nil
	}
	var ms []Match
	if f&fPEM != 0 && strings.Contains(s, "-----BEGIN") {
		ms = findPEM(s, ms)
	}
	if url {
		ms = reURL.spans(s, KindURLPassword, ms)
	}
	if f&fAuth != 0 {
		ms = findAuthHeaders(s, ms)
	}
	if f&fBearer != 0 {
		ms = reBearer.spans(s, KindBearer, ms)
	}
	if f&fBasic != 0 {
		ms = findBasic(s, ms)
	}
	if f&fKey != 0 {
		ms = findKeyValues(s, ms)
		ms = findFlags(s, ms)
	}
	if f&fAWS != 0 {
		for _, p := range awsPrefixes {
			if strings.Contains(s, p) {
				ms = reAWS.spans(s, KindAWS, ms)
				break
			}
		}
	}
	if f&fVendor != 0 {
		ms = reVendor.spans(s, KindVendorToken, ms)
		if strings.Contains(s, "hooks.slack.com/services/") {
			ms = reSlackHook.spans(s, KindVendorToken, ms)
		}
	}
	if f&fJWT != 0 && strings.Contains(s, "eyJ") {
		ms = reJWT.spans(s, KindJWT, ms)
	}
	return mergeMatches(ms)
}

// mergeMatches sorts spans and merges overlapping or touching ones (two
// adjacent masks would otherwise read "******" and break idempotence).
func mergeMatches(ms []Match) []Match {
	if len(ms) < 2 {
		return ms
	}
	sort.SliceStable(ms, func(i, j int) bool {
		if ms[i].Start != ms[j].Start {
			return ms[i].Start < ms[j].Start
		}
		return ms[i].End > ms[j].End
	})
	out := ms[:1]
	for _, m := range ms[1:] {
		last := &out[len(out)-1]
		if m.Start <= last.End {
			if m.End > last.End {
				last.End = m.End
			}
			if kindPriority(m.Kind) > kindPriority(last.Kind) {
				last.Kind = m.Kind
			}
			continue
		}
		out = append(out, m)
	}
	return out
}

// kindPriority picks the most specific Kind when spans merge, so the
// per-kind counters of `ballast report` credit "ghp_…" in "TOKEN=ghp_…" to
// vendor_token rather than to the generic key_value.
func kindPriority(k Kind) int {
	switch k {
	case KindPrivateKey:
		return 9
	case KindURLPassword:
		return 8
	case KindAuthHeader:
		return 7
	case KindAWS, KindVendorToken, KindJWT:
		return 6
	case KindBearer:
		return 5
	case KindFlag:
		return 4
	case KindKeyValue:
		return 3
	}
	return 0
}

// --- PEM -------------------------------------------------------------------

func findPEM(s string, out []Match) []Match {
	for _, ix := range rePEM.get().FindAllStringSubmatchIndex(s, -1) {
		if a, b := trimBlank(s, ix[2], ix[3]); b > a {
			out = append(out, Match{Kind: KindPrivateKey, Start: a, End: b})
		}
	}
	return out
}

// trimBlank shrinks [a, b) past surrounding blanks, line breaks and
// JSON-escaped \n, \r, \t, so the masked body keeps the line structure.
func trimBlank(s string, a, b int) (int, int) {
	esc := func(c byte) bool { return c == 'n' || c == 'r' || c == 't' }
	for a < b {
		switch {
		case isBlank(s[a]) || s[a] == '\r' || s[a] == '\n':
			a++
		case s[a] == '\\' && a+1 < b && esc(s[a+1]):
			a += 2
		default:
			goto tail
		}
	}
tail:
	for b > a {
		switch {
		case isBlank(s[b-1]) || s[b-1] == '\r' || s[b-1] == '\n':
			b--
		case b-2 >= a && s[b-2] == '\\' && esc(s[b-1]):
			b -= 2
		default:
			return a, b
		}
	}
	return a, b
}

// --- Basic -----------------------------------------------------------------

func findBasic(s string, out []Match) []Match {
	for _, ix := range reBasic.get().FindAllStringSubmatchIndex(s, -1) {
		v := s[ix[2]:ix[3]]
		if basicLooksSecret(v) {
			out = append(out, Match{Kind: KindBearer, Start: ix[2], End: ix[3]})
		}
	}
	return out
}

// basicLooksSecret filters prose such as "Basic configuration": a value must
// carry a digit, base64 punctuation or an interior upper-case letter.
func basicLooksSecret(v string) bool {
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= '0' && c <= '9', c == '+', c == '/', c == '=':
			return true
		case c >= 'A' && c <= 'Z' && i > 0:
			return true
		}
	}
	return false
}

// --- Authorization header ----------------------------------------------------

func isBlank(c byte) bool { return c == ' ' || c == '\t' }

func isQuote(c byte) bool { return c == '"' || c == '\'' }

func lineEnd(s string, from int) int {
	for from < len(s) && s[from] != '\n' && s[from] != '\r' {
		from++
	}
	return from
}

// asciiLower lower-cases ASCII letters only, so byte offsets stay aligned
// with s (strings.ToLower may change the length of non-ASCII text).
func asciiLower(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if c := b[j]; c >= 'A' && c <= 'Z' {
					b[j] = c + 32
				}
			}
			return string(b)
		}
	}
	return s
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isAlnum(c byte) bool { return isAlpha(c) || c >= '0' && c <= '9' }

// findAuthHeaders masks the credential of every "Authorization" /
// "Proxy-Authorization" header (name[:=]value), whatever the scheme. The
// scheme word ("Bearer", "Basic", "Digest", ...) is kept. Quoted headers
// (JSON values, curl -H "...") are masked up to the closing quote; bare ones
// up to the end of the first token (with its comma-separated continuation).
func findAuthHeaders(s string, out []Match) []Match {
	const name = "authorization"
	low := asciiLower(s)
	n := len(s)
	from := 0
	// Memos that keep the scan linear: the end of the current line, and per
	// quote character the line end up to which no closing quote exists.
	leMemo, leFrom := -1, 0
	budget := 16*n + 1024
	noClose := [2]int{-1, -1}
	for from < n {
		k := strings.Index(low[from:], name)
		if k < 0 {
			break
		}
		start := from + k
		q := start + len(name)
		from = q
		keyQuoted := false
		if q < n && s[q] == '\\' && q+1 < n && isQuote(s[q+1]) {
			q += 2
			keyQuoted = true
		} else if q < n && isQuote(s[q]) {
			q++
			keyQuoted = true
		}
		for q < n && isBlank(s[q]) {
			q++
		}
		if q >= n || (s[q] != ':' && s[q] != '=') {
			continue
		}
		isEq := s[q] == '='
		q++
		if isEq && q < n && s[q] == '>' {
			q++
		}
		for q < n && isBlank(s[q]) {
			q++
		}
		var quote byte
		if q < n {
			if s[q] == '\\' && q+1 < n && isQuote(s[q+1]) {
				quote = s[q+1]
				q += 2
			} else if isQuote(s[q]) {
				quote = s[q]
				q++
			}
		}
		if quote == 0 && !keyQuoted && start > 0 && isQuote(s[start-1]) {
			quote = s[start-1] // header inside a quoted argument
		}
		if leMemo < q || leFrom > q {
			leMemo, leFrom = lineEnd(s, q), q
		}
		le := leMemo
		regionEnd := le
		if quote != 0 {
			e := q
			qi := 0
			if quote == '\'' {
				qi = 1
			}
			if q < noClose[qi] && noClose[qi] == le {
				e = le
			} else {
				for e < le && s[e] != quote {
					e++
				}
				if e == le {
					noClose[qi] = le
				}
			}
			if e < le && e > q && s[e-1] == '\\' {
				e-- // escaped closing quote (\")
			}
			regionEnd = e
			for regionEnd > q && isBlank(s[regionEnd-1]) {
				regionEnd--
			}
		}
		vs, ve := authValueSpan(s, q, regionEnd, le, quote != 0)
		if ve > vs {
			out = append(out, Match{Kind: KindAuthHeader, Start: vs, End: ve})
			// A credential may swallow the name of the next header
			// ("Authorization: Authorization: Bearer X"), so scanning
			// resumes after the name; the work is bounded.
			budget -= ve - vs
			if budget < 0 && ve > from {
				from = ve
			}
		}
	}
	return out
}

// knownSchemes are the authentication schemes kept readable in front of the
// masked credential. Anything else is not trusted to be a scheme word (it
// may be the credential itself), so it is masked too.
var knownSchemes = [...]string{
	"bearer", "basic", "digest", "token", "negotiate", "ntlm", "oauth",
	"hawk", "mac", "aws4-hmac-sha256", "apikey", "jwt", "bot",
}

func isKnownScheme(w string) bool {
	for _, k := range knownSchemes {
		if len(w) == len(k) && strings.EqualFold(w, k) {
			return true
		}
	}
	return false
}

// authValueSpan returns the credential span of a header whose value starts
// at q. regionEnd bounds a quoted value; le is the end of the line.
//
//   - known scheme followed by a credential: the span is the credential
//     (quoted: the rest of the region; bare: one token plus its
//     comma-separated continuation);
//   - anything else: the first word is the credential (or part of it), so
//     the span starts there and, outside quotes, extends over one more word
//     ("Authorization: <word> <word>"); a first word that is already Mask
//     is left alone, which keeps the function idempotent.
func authValueSpan(s string, q, regionEnd, le int, quoted bool) (int, int) {
	limit := le
	if quoted {
		limit = regionEnd
	}
	if q >= limit {
		return q, q
	}
	e := q
	for e < limit && !isBlank(s[e]) && !isQuote(s[e]) {
		e++
	}
	word := s[q:e]
	p := e
	for p < limit && isBlank(s[p]) {
		p++
	}
	hasNext := p > e && p < limit && !isQuote(s[p])
	switch {
	case isKnownScheme(word):
		if p > e && p < limit && isQuote(s[p]) && !quoted {
			// Bearer "value": mask inside the quotes.
			if c := closingQuote(s, p); c > p+1 && c <= limit {
				return p + 1, c
			}
			return p + 1, limit
		}
		if !hasNext {
			return q, q
		}
		if quoted {
			return p, regionEnd
		}
		return p, bareHeaderValueEnd(s, p, le)
	case quoted:
		return q, regionEnd
	case strings.TrimRight(word, ",;") == Mask:
		return q, q + len(Mask)
	case hasNext:
		if ve := bareHeaderValueEnd(s, p, le); ve > p {
			return q, ve
		}
	}
	return q, bareHeaderValueEnd(s, q, le)
}

// bareHeaderValueEnd scans an unquoted header value: one token, extended
// across ", token" continuations (Digest, AWS4-HMAC-SHA256).
func bareHeaderValueEnd(s string, vs, le int) int {
	e := vs
	for e < le {
		for e < le && !isBlank(s[e]) && !isQuote(s[e]) && s[e] != ';' && s[e] != ',' {
			e++
		}
		if e < le && s[e] == ',' {
			p := e + 1
			for p < le && isBlank(s[p]) {
				p++
			}
			if p < le && !isQuote(s[p]) && s[p] != ';' && s[p] != ',' {
				e = p
				continue
			}
		}
		break
	}
	return e
}

// --- key_value and flags -------------------------------------------------------

func isKeyChar(c byte) bool {
	return isAlnum(c) || c == '_' || c == '-' || c == '.'
}

// secretWords are matched against the lower-cased key with '_', '-' and '.'
// removed (api_key, api-key, apiKey and apikey are one spelling).
var secretWords = [...]string{
	"password", "passwd", "passphrase", "secret", "token", "apikey",
	"accesskey", "privatekey", "credential", "accountkey", "sharedaccesskey",
}

func normKey(k string) string {
	b := make([]byte, 0, len(k))
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c == '_' || c == '-' || c == '.':
		case c >= 'A' && c <= 'Z':
			b = append(b, c+32)
		default:
			b = append(b, c)
		}
	}
	return string(b)
}

func containsSecretWord(norm string) bool {
	if strings.Contains(norm, "tokeniz") {
		norm = strings.ReplaceAll(norm, "tokeniz", "")
	}
	for _, w := range secretWords {
		if strings.Contains(norm, w) {
			return true
		}
	}
	return false
}

// keyClass says how much a key name is trusted to name a secret.
type keyClass uint8

const (
	keyNone   keyClass = iota
	keyStrong          // contains password, token, secret, ... : always masked
	keyWeak            // pwd, pass, auth (whole key or last segment): ordinary words too
)

// classifyKey classifies a key name (a run of [A-Za-z0-9_.-]).
func classifyKey(k string) keyClass {
	k = strings.TrimLeft(k, "-.")
	if k == "" {
		return keyNone
	}
	norm := normKey(k)
	if containsSecretWord(norm) {
		return keyStrong
	}
	switch norm {
	case "pwd", "pass", "auth":
		return keyWeak
	}
	if len(norm) > len("pwd") && strings.HasSuffix(norm, "pwd") {
		return keyWeak // mypwd=, userPwd:, db_pwd
	}
	seg := k
	if i := strings.LastIndexAny(k, "_-."); i >= 0 {
		seg = k[i+1:]
	}
	if seg = asciiLower(seg); seg == "pwd" || seg == "pass" {
		return keyWeak
	}
	return keyNone
}

// valueEnd parses a value starting at p the way the old audit pattern did:
// a run of bare characters and quoted segments, ended by whitespace or one
// of , ; & ) } ]. noQuote carries "no closing quote exists before this
// offset" (a failed search is not repeated on the same line, which keeps the
// scan linear). A quote that opens the value but never closes is an ordinary
// character; an unmatched quote in the middle of a value is the closing
// quote of an enclosing string ("password=x") and ends the value.
func valueEnd(s string, p int, noQuote *int) int {
	n := len(s)
	e := p
	for e < n {
		switch c := s[e]; c {
		case '"', '\'':
			if e >= *noQuote {
				if k := closingQuote(s, e); k >= 0 {
					e = k + 1
					continue
				}
				*noQuote = lineEnd(s, e)
			}
			if e != p {
				return e
			}
			e++
		case '\\':
			if e != p && e+1 < n && isQuote(s[e+1]) {
				return e // \" closes an enclosing JSON-escaped string
			}
			e++
		case ' ', '\t', '\r', '\n', ',', ';', '&', ')', '}', ']':
			return e
		default:
			e++
		}
	}
	return e
}

// valueSpan returns the secret part [a, b) of the value starting at p and
// the end ve of the whole value. Surrounding quotes are not part of the
// secret; a JSON-escaped opening quote (\"value\") is honoured too.
func valueSpan(s string, p int, noQuote *int) (a, b, ve int) {
	if p+1 < len(s) && s[p] == '\\' && isQuote(s[p+1]) && p+2 > *noQuote {
		q := s[p+1]
		for j := p + 2; j < len(s) && s[j] != '\n' && s[j] != '\r'; j++ {
			if s[j] == q {
				b = j
				if j > p+2 && s[j-1] == '\\' {
					b = j - 1
				}
				return p + 2, b, j + 1
			}
		}
		*noQuote = lineEnd(s, p)
	}
	ve = valueEnd(s, p, noQuote)
	a, b = innerValue(s, p, ve)
	return a, b, ve
}

// closingQuote returns the index of the quote closing the one at s[i] on the
// same line, or -1. Double quotes honour backslash escapes.
func closingQuote(s string, i int) int {
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		switch c := s[j]; {
		case c == '\n' || c == '\r':
			return -1
		case c == '\\' && q == '"':
			j++
		case c == q:
			return j
		}
	}
	return -1
}

// innerValue strips one pair of surrounding quotes.
func innerValue(s string, a, b int) (int, int) {
	if b-a >= 2 && isQuote(s[a]) && s[b-1] == s[a] {
		return a + 1, b - 1
	}
	return a, b
}

// keepValue reports values key_value leaves alone: ≤ 6 decimal digits
// (max_tokens: 4096) and true/false/null.
func keepValue(v string) bool {
	if len(v) <= 6 {
		digits := len(v) > 0
		for i := 0; i < len(v); i++ {
			if v[i] < '0' || v[i] > '9' {
				digits = false
				break
			}
		}
		if digits {
			return true
		}
	}
	switch len(v) {
	case 4:
		return strings.EqualFold(v, "true") || strings.EqualFold(v, "null")
	case 5:
		return strings.EqualFold(v, "false")
	}
	return false
}

func findKeyValues(s string, out []Match) []Match {
	n := len(s)
	noQuote := 0
	// A value may swallow the delimiter of a following secret key
	// ("token: api_key: VALUE"), so scanning resumes right after the
	// delimiter instead of after the value. The work spent on nested values
	// is bounded; past the budget the scanner jumps over values, which keeps
	// pathological input linear.
	budget := 16*n + 1024
	encEnd := 0 // end of the value (inside the quotes) of the previous match
	for i := 0; i < n; i++ {
		c := s[i]
		if c != '=' && c != ':' {
			continue
		}
		j := i
		for j > 0 && isBlank(s[j-1]) {
			j--
		}
		blanks := i - j
		for j > 0 && (isQuote(s[j-1]) || s[j-1] == '\\') {
			j--
		}
		ke := j
		quotedKey := ke < i-blanks
		for j > 0 && isKeyChar(s[j-1]) {
			j--
		}
		if ke == j {
			continue
		}
		class := classifyKey(s[j:ke])
		if class == keyNone {
			continue
		}
		if c == ':' && j >= 3 && s[j-3:j] == "://" {
			continue // scheme://user:PASSWORD@host belongs to the URL rule
		}
		p := i + 1
		if p < n && (s[p] == '=' || (c == '=' && s[p] == '>')) {
			p++
		}
		afterBlank := p < n && isBlank(s[p])
		for p < n && isBlank(s[p]) {
			p++
		}
		if class == keyWeak && !quotedKey && (c == '=' && (blanks > 0 || afterBlank)) {
			continue // "x.Pass = y" is code, "pass=y" is an argument
		}
		lim := n
		if p <= encEnd {
			lim = encEnd // starts inside the previous value, cannot outlive it
		}
		if p < lim && (s[p] == '[' || s[p] == '(') {
			// A list of scalars under a strong key ("password=[x]",
			// {"password": ["x"]}): the scalars are the secret. Nested
			// structures inside are scanned on their own.
			if class == keyStrong {
				var end int
				out, end = listValues(s[:lim], p+1, &noQuote, out)
				if end > encEnd {
					encEnd = end
				}
			}
			continue
		}
		if p < lim && s[p] == '{' {
			continue // an object, not a value; keys inside are scanned on their own
		}
		a, b, ve := valueSpan(s[:lim], p, &noQuote)
		if ve == p {
			continue
		}
		budget -= ve - p
		if budget < 0 {
			i = ve - 1
		}
		if b > encEnd {
			encEnd = b
		}
		if b <= a || keepValue(s[a:b]) {
			continue
		}
		if class == keyWeak && !quotedKey && c == ':' && !hasDigit(s[a:b]) {
			continue // "must pass: ok" is prose; "pass: hunter2" carries a digit
		}
		out = append(out, Match{Kind: KindKeyValue, Start: a, End: b})
	}
	return out
}

// maxListValues bounds how many elements of one bracketed list are masked,
// which keeps the scan linear on "password=[a,a,a,...".
const maxListValues = 64

// listValues masks the scalar elements of the bracketed list whose first
// element starts at p (just after "[" or "("): a run of values separated by
// commas, ended by a closing bracket, a nested structure or anything else.
// It returns the extended matches and the end offset of the last element.
func listValues(s string, p int, noQuote *int, out []Match) ([]Match, int) {
	last := 0
	for k := 0; k < maxListValues; k++ {
		for p < len(s) && isBlank(s[p]) {
			p++
		}
		if p >= len(s) || strings.IndexByte("]){}[(", s[p]) >= 0 {
			break
		}
		a, b, ve := valueSpan(s, p, noQuote)
		if ve == p {
			break
		}
		if b > a && !keepValue(s[a:b]) {
			out = append(out, Match{Kind: KindKeyValue, Start: a, End: b})
		}
		if b > last {
			last = b
		}
		p = ve
		for p < len(s) && isBlank(s[p]) {
			p++
		}
		if p >= len(s) || s[p] != ',' {
			break
		}
		p++
	}
	return out, last
}

func hasDigit(v string) bool {
	for i := 0; i < len(v); i++ {
		if v[i] >= '0' && v[i] <= '9' {
			return true
		}
	}
	return false
}

func flagSecret(name string) bool {
	norm := normKey(name)
	if strings.Contains(norm, "tokeniz") {
		norm = strings.ReplaceAll(norm, "tokeniz", "")
	}
	for _, w := range [...]string{"password", "passwd", "secret", "token", "apikey"} {
		if strings.Contains(norm, w) {
			return true
		}
	}
	return false
}

// findFlags masks the value of "--password VALUE" style options (space
// separated; the "=" form is covered by findKeyValues).
func findFlags(s string, out []Match) []Match {
	n := len(s)
	noQuote := 0
	for i := 0; i < n; i++ {
		if s[i] != '-' || (i > 0 && (isAlnum(s[i-1]) || s[i-1] == '-' || s[i-1] == '_')) {
			continue
		}
		j := i
		for j < n && s[j] == '-' {
			j++
		}
		if j-i > 2 {
			i = j - 1
			continue
		}
		k := j
		for k < n && (isAlnum(s[k]) || s[k] == '-' || s[k] == '_') {
			k++
		}
		i = k - 1
		if k == j || (k < n && s[k] == '=') || !flagSecret(s[j:k]) {
			continue
		}
		p := k
		for p < n && isBlank(s[p]) {
			p++
		}
		if p == k || p >= n || s[p] == '\n' || s[p] == '\r' {
			continue
		}
		if s[p] == '-' && p+1 < n && (s[p+1] == '-' || isAlpha(s[p+1])) {
			continue // the next option, not a value
		}
		a, b, ve := valueSpan(s, p, &noQuote)
		if b > a {
			out = append(out, Match{Kind: KindFlag, Start: a, End: b})
		}
		if ve > i {
			i = ve - 1
		}
	}
	return out
}
