package redact

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// checkInvariants asserts the properties every input must satisfy.
func checkInvariants(t *testing.T, s string) string {
	t.Helper()
	out := String(s)
	if utf8.ValidString(s) && !utf8.ValidString(out) {
		t.Fatalf("String(%q) produced invalid UTF-8: %q", s, out)
	}
	if again := String(out); again != out {
		t.Fatalf("not idempotent on %q:\n  once: %q\n twice: %q", s, out, again)
	}
	if !MayContainSecret(s) && out != s {
		t.Fatalf("MayContainSecret(%q) is false but String changed it to %q", s, out)
	}
	ms := Find(s)
	last := 0
	for _, m := range ms {
		if m.Start < last || m.End <= m.Start || m.End > len(s) {
			t.Fatalf("Find(%q) returned bad span %+v after %d", s, m, last)
		}
		last = m.End
	}
	if len(ms) == 0 && out != s {
		t.Fatalf("String changed %q without any Find span: %q", s, out)
	}
	if len(out) > len(s)+2*strings.Count(out, Mask) {
		t.Fatalf("output grew beyond the mask allowance: %q -> %q", s, out)
	}
	return out
}

func TestEmptyAndShort(t *testing.T) {
	for _, s := range []string{"", "a", "ab", "=", ":", "--", "\xff", "\xff\xfe\xfd"} {
		if got := checkInvariants(t, s); got != s {
			t.Errorf("String(%q) = %q", s, got)
		}
	}
}

func TestKeepsContext(t *testing.T) {
	tests := []struct{ in, want string }{
		{"password=hunter2hunter2 next", "password=*** next"},
		{"Authorization: Bearer abcdef1234567890", "Authorization: Bearer ***"},
		{"got bearer tok_abcdefghijkl back", "got bearer *** back"},
		{"https://user:p4ssw0rd@example.com/x", "https://user:***@example.com/x"},
		{"cli --token=abc123 run", "cli --token=*** run"},
		{"tool --password hunter2xyz --verbose", "tool --password *** --verbose"},
		{"line\nsecret=abc\nend", "line\nsecret=***\nend"},
		{"max_tokens: 4096", "max_tokens: 4096"},
		{"max_tokens: 4096000", "max_tokens: ***"},
		{`{"stream": true, "token": null, "secret": false}`, `{"stream": true, "token": null, "secret": false}`},
	}
	for _, tc := range tests {
		if got := String(tc.in); got != tc.want {
			t.Errorf("String(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFindDoesNotReturnSecretText(t *testing.T) {
	in := "password=hunter2hunter2 and Authorization: Bearer abcdef1234567890"
	ms := Find(in)
	if len(ms) != 2 || ms[0].Kind != KindKeyValue || ms[1].Kind != KindAuthHeader {
		t.Fatalf("Find = %+v", ms)
	}
	if in[ms[0].Start:ms[0].End] != "hunter2hunter2" || in[ms[1].Start:ms[1].End] != "abcdef1234567890" {
		t.Errorf("spans do not cover exactly the values: %+v", ms)
	}
}

func TestMayContainSecretIsPrefilter(t *testing.T) {
	for _, s := range []string{
		"", "git status", "go test ./... -race", "Edit: internal/state/store.go",
		"这是一个中文描述,没有密钥", "https://example.com/a?b=c",
	} {
		if MayContainSecret(s) {
			t.Errorf("MayContainSecret(%q) = true on plain text", s)
		}
	}
	for _, s := range []string{"password=x", "://u:p@h", "Bearer abcdefgh", "ghp_xxxxxxxx", "eyJabc"} {
		if !MayContainSecret(s) {
			t.Errorf("MayContainSecret(%q) = false", s)
		}
	}
}

func TestAnchorTable(t *testing.T) {
	anchorOnce.Do(buildAnchors) // panics on a slot collision
	seen := map[uint32]bool{}
	for _, a := range anchors {
		if len(a.tri) != 3 {
			t.Errorf("anchor %q is not a trigram", a.tri)
		}
		k := triKey(a.tri)
		if seen[k] {
			t.Errorf("duplicate anchor %q", a.tri)
		}
		seen[k] = true
		if anchorTab[triHash(k)].fam&a.f == 0 {
			t.Errorf("anchor %q lost its family", a.tri)
		}
	}
}

// TestScanFindsAnchorAtEveryOffset exercises both the 8-byte block loop and
// the scalar tail of scanFlags: every anchor, in either case, at every
// offset and with every tail length.
func TestScanFindsAnchorAtEveryOffset(t *testing.T) {
	for _, a := range anchors {
		for _, tri := range []string{a.tri, strings.ToUpper(a.tri)} {
			for pre := 0; pre < 20; pre++ {
				for post := 0; post < 12; post++ {
					in := strings.Repeat("~", pre) + tri + strings.Repeat("~", post)
					if got := scanFlags(in); got&a.f == 0 {
						t.Fatalf("anchor %q at offset %d (+%d tail) not found: flags %b", tri, pre, post, got)
					}
				}
			}
		}
	}
	if got := scanFlags(strings.Repeat("~", 64)); got != 0 {
		t.Errorf("flags %b on filler", got)
	}
}

// TestAnchorsCoverEveryRule feeds each positive sample through the prefilter
// individually: a rule that fires without its anchor would break the
// "!MayContainSecret ⇒ unchanged" contract (checkInvariants asserts it).
func TestAnchorsCoverEveryRule(t *testing.T) {
	f := loadSamples(t)
	for _, p := range f.Positive {
		checkInvariants(t, f.expand(p.In))
	}
	for _, n := range f.Negative {
		checkInvariants(t, f.expand(n.In))
	}
}

// TestLinearOnPathologicalInput guards against quadratic scans (the audit
// writer redacts whole commands, which can be large).
func TestLinearOnPathologicalInput(t *testing.T) {
	big := 1 << 18
	inputs := map[string]string{
		"quotes":      strings.Repeat(`token="\"`, big/10),
		"single":      strings.Repeat(`secret='`, big/8),
		"delims":      strings.Repeat("a=b:c ", big/6),
		"keys":        strings.Repeat("password=", big/9),
		"flags":       strings.Repeat("--token ", big/8),
		"dashes":      strings.Repeat("-", big),
		"auth":        strings.Repeat("Authorization: ", big/15),
		"auth quoted": strings.Repeat(`"Authorization: x `, big/17),
		"pem":         strings.Repeat("-----BEGIN PRIVATE KEY-----\n", big/28),
		"bearer":      strings.Repeat("bearer ", big/7),
		"url":         strings.Repeat("://a:", big/5) + "@",
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			out := String(in)
			if again := String(out); again != out {
				t.Error("not idempotent")
			}
		})
	}
}

// shrink greedily deletes chunks of in while bad stays true.
func shrink(in string, bad func(string) bool) string {
	for chunk := len(in) / 2; chunk >= 1; chunk /= 2 {
		for i := 0; i+chunk <= len(in); {
			if cand := in[:i] + in[i+chunk:]; bad(cand) {
				in = cand
			} else {
				i += chunk
			}
		}
	}
	return in
}

// TestSyntaxSoup builds random strings out of the rules' own syntax
// (keys, quotes, delimiters, headers, token prefixes, masks) and checks the
// invariants, shrinking any failure to a readable case.
func TestSyntaxSoup(t *testing.T) {
	toks := []string{
		"password", "token", "=", ":", " ", "\"", "'", "\\", "-", "--", "Authorization",
		"Bearer", "Basic", "://", "@", "-----BEGIN PRIVATE KEY-----", "\n", "a", "eyJ",
		".", ",", ";", "key", "x", "ghp_", "*", "***", "sk-", "AKIA", "q7", "\t",
	}
	r := rand.New(rand.NewPCG(1, 2))
	bad := func(s string) bool {
		out := String(s)
		return String(out) != out || (!MayContainSecret(s) && out != s)
	}
	seen := map[string]bool{}
	for it := 0; it < 30000; it++ {
		var b strings.Builder
		for i, n := 0, 3+r.IntN(50); i < n; i++ {
			b.WriteString(toks[r.IntN(len(toks))])
		}
		if in := b.String(); bad(in) {
			m := shrink(in, bad)
			if !seen[m] {
				seen[m] = true
				t.Errorf("invariant broken for %q (once %q)", m, String(m))
			}
		}
	}
}

// --- fuzzing -------------------------------------------------------------------

const (
	alnumChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	upperDigit = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b64urlSet  = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"
	pwChars    = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!$%^*-_+"
	bearerSet  = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._~+/-"
)

func randStr(r *rand.Rand, alphabet string, min, max int) string {
	n := min + r.IntN(max-min+1)
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[r.IntN(len(alphabet))]
	}
	return string(b)
}

// withLetter forces at least one letter and one digit so keepValue (≤ 6
// digits) can never apply to a planted value and the text is not a word.
func withLetter(v string) string { return "q7" + v }

// plantSecret returns a text embedding a secret of the given kind and the
// secret value that must not survive.
func plantSecret(kind uint8, seed uint64) (text, secret string) {
	r := rand.New(rand.NewPCG(seed, uint64(kind)+1))
	switch kind % 9 {
	case 0: // private_key
		body := randStr(r, alnumChars+"+/", 40, 120)
		hdr := []string{"", "RSA ", "EC ", "OPENSSH ", "ENCRYPTED "}[r.IntN(5)]
		text = "-----BEGIN " + hdr + "PRIVATE KEY-----\n" + body + "\n-----END " + hdr + "PRIVATE KEY-----"
		if r.IntN(3) == 0 {
			text = "-----BEGIN " + hdr + "PRIVATE KEY-----\r\n" + body // clipped
		}
		return text, body
	case 1: // url_password
		pw := withLetter(randStr(r, pwChars, 6, 20))
		return "https://user:" + pw + "@host.example/p", pw
	case 2: // auth_header
		v := withLetter(randStr(r, alnumChars, 10, 30))
		scheme := []string{"Bearer ", "Basic ", "Token ", "Digest ", ""}[r.IntN(5)]
		name := []string{"Authorization", "authorization", "Proxy-Authorization"}[r.IntN(3)]
		switch r.IntN(3) {
		case 0:
			return name + ": " + scheme + v, v
		case 1:
			return `"` + name + `": "` + scheme + v + `"`, v
		default:
			return `curl -H '` + name + `: ` + scheme + v + `' u`, v
		}
	case 3: // bearer
		v := withLetter(randStr(r, bearerSet, 8, 30))
		return "Bearer " + v, v
	case 4: // key_value
		key := []string{"password", "token", "api_key", "SECRET", "DB_PASSWORD", "client_secret", "accessToken", "x-api-key", "passwd", "private_key", "pass", "pwd", "auth", "DB_PASS"}[r.IntN(14)]
		v := withLetter(randStr(r, alnumChars, 8, 20))
		switch r.IntN(4) {
		case 0:
			return key + "=" + v, v
		case 1:
			return key + ": " + v, v
		case 2:
			return `{"` + key + `": "` + v + `"}`, v
		default:
			return "export " + strings.ToUpper(key) + "=" + v, v
		}
	case 5: // flag
		flag := []string{"--password", "--token", "--api-key", "--secret"}[r.IntN(4)]
		v := withLetter(randStr(r, alnumChars, 8, 20))
		return "tool " + flag + " " + v + " run", v
	case 6: // aws
		v := []string{"AKIA", "ASIA"}[r.IntN(2)] + randStr(r, upperDigit, 16, 16)
		return v, v
	case 7: // vendor_token
		var v string
		switch r.IntN(10) {
		case 0:
			v = "ghp_" + randStr(r, alnumChars, 30, 40)
		case 1:
			v = "github_pat_" + randStr(r, b64urlSet, 30, 60)
		case 2:
			v = "glpat-" + randStr(r, b64urlSet, 20, 30)
		case 3:
			v = "npm_" + randStr(r, alnumChars, 36, 40)
		case 4:
			v = "xoxb-" + randStr(r, alnumChars+"-", 20, 40)
		case 5:
			v = "sk-" + randStr(r, b64urlSet, 30, 60)
		case 6:
			v = "sk_live_" + randStr(r, alnumChars, 24, 30)
		case 7:
			v = "AIza" + randStr(r, b64urlSet, 35, 35)
		case 8:
			v = "hf_" + randStr(r, alnumChars, 34, 40)
		default:
			v = "SG." + randStr(r, b64urlSet, 22, 22) + "." + randStr(r, b64urlSet, 43, 43)
		}
		return v, v
	default: // jwt
		v := "eyJ" + randStr(r, b64urlSet, 10, 30) + "." + randStr(r, b64urlSet, 10, 30) + "." + randStr(r, b64urlSet, 10, 40)
		return v, v
	}
}

var seps = [...]string{" ", "\n", "\t", "\r\n", "  "}

func FuzzString(f *testing.F) {
	samples := loadSamples(f)
	for i, p := range samples.Positive {
		f.Add(samples.expand(p.In), uint8(i), uint64(i), uint8(i))
	}
	for i, n := range samples.Negative {
		f.Add(samples.expand(n.In), uint8(i), uint64(i), uint8(i))
	}
	f.Add("", uint8(0), uint64(0), uint8(0))
	f.Add("password=", uint8(4), uint64(1), uint8(1))
	f.Add("-----BEGIN PRIVATE KEY-----", uint8(0), uint64(2), uint8(2))
	f.Add("://:@", uint8(1), uint64(3), uint8(3))
	f.Add("Authorization:", uint8(2), uint64(4), uint8(4))
	f.Add("\"\\\"'", uint8(4), uint64(5), uint8(0))
	f.Fuzz(func(t *testing.T, s string, kind uint8, seed uint64, sep uint8) {
		checkInvariants(t, s)

		// Plant a secret of the chosen kind in the middle of s, delimited on
		// both sides; none of it may survive and the result must still obey
		// the invariants.
		text, secret := plantSecret(kind, seed)
		mid := len(s) / 2
		for mid < len(s) && mid > 0 && !utf8.RuneStart(s[mid]) {
			mid--
		}
		l, r := seps[int(sep)%len(seps)], seps[int(sep>>3)%len(seps)]
		in := s[:mid] + l + text + r + s[mid:]
		out := checkInvariants(t, in)
		if strings.Contains(out, secret) {
			t.Fatalf("planted %s secret leaked\n  in: %q\n out: %q\n secret: %q", []string{"private_key", "url_password", "auth_header", "bearer", "key_value", "flag", "aws", "vendor_token", "jwt"}[kind%9], in, out, secret)
		}
	})
}

// TestPlantedSecretsWithoutContext runs the generators over many seeds
// without any random surrounding text (the fuzz corpus covers the rest).
func TestPlantedSecretsWithoutContext(t *testing.T) {
	for kind := uint8(0); kind < 9; kind++ {
		for seed := uint64(0); seed < 400; seed++ {
			text, secret := plantSecret(kind, seed)
			for _, l := range []string{"", " ", "\n"} {
				in := "ctx" + l + text + l + "tail"
				if l == "" {
					in = text
				}
				out := checkInvariants(t, in)
				if strings.Contains(out, secret) {
					t.Fatalf("kind %d seed %d leaked %q in %q -> %q", kind, seed, secret, in, out)
				}
			}
		}
	}
}

// --- benchmarks ----------------------------------------------------------------

// cleanText4K is 4 KiB of secret-free, hook-shaped text (commands, paths,
// log lines, code).
func cleanText4K() string {
	const unit = "Bash: go test ./internal/state ./internal/audit -race -count=1 && git status --short\n" +
		"Edit: internal/hook/pipeline.go (added 12 lines, removed 3)\n" +
		"2026-10-16T12:00:00Z INFO listening on 127.0.0.1:8080 request=GET /health took=3ms\n" +
		"func (s *Store) Load(ctx context.Context, id string) (*Record, error) { return s.cache[id], nil }\n" +
		"当前任务:整理文档并运行单元测试,没有涉及任何敏感内容。\n"
	var b strings.Builder
	for b.Len() < 4096 {
		b.WriteString(unit)
	}
	return b.String()[:4096]
}

func BenchmarkClean4K(b *testing.B) {
	s := cleanText4K()
	if MayContainSecret(s) {
		b.Fatal("benchmark text must be secret-free and pass the prefilter")
	}
	b.SetBytes(int64(len(s)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if String(s) != s {
			b.Fatal("changed")
		}
	}
}

// TestClean4KBudget enforces the docs/p2-design.md §4.3 budget (4 KiB of
// secret-free text <= 10µs per String call) as the best of several rounds,
// so one scheduler hiccup on a shared runner does not fail it while a
// genuinely slower implementation still does. Skipped under -race.
func TestClean4KBudget(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("timing budget is not meaningful under -race / -short")
	}
	s := cleanText4K()
	if MayContainSecret(s) {
		t.Fatal("budget text must be secret-free and pass the prefilter")
	}
	for i := 0; i < 50; i++ { // warm up
		String(s)
	}
	const rounds, iters = 15, 500
	best := time.Hour
	for r := 0; r < rounds; r++ {
		start := time.Now()
		for i := 0; i < iters; i++ {
			String(s)
		}
		if per := time.Since(start) / iters; per < best {
			best = per
		}
	}
	t.Logf("String(4 KiB clean) best of %d rounds: %v", rounds, best)
	if best > 10*time.Microsecond {
		t.Errorf("String(4 KiB clean) took %v per call, budget 10µs", best)
	}
}

func BenchmarkShortClean(b *testing.B) {
	s := "Bash: go test ./internal/... -race -count=1"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = String(s)
	}
}

func BenchmarkWithSecrets(b *testing.B) {
	s := "Bash: curl -H 'Authorization: Bearer abcdef1234567890abcdef' https://u:pw1234@h/x && export API_KEY=abcdef0123456789"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = String(s)
	}
}
