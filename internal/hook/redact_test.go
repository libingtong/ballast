package hook

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/libingtong/ballast/internal/redact"
)

// TestRedact pins the state-ring summary redaction to the shared
// internal/redact (docs/p2-design.md §4.1): it masks secret VALUES only, so the
// quote/user parts that the pre-P2 word scanner swallowed now stay readable.
func TestRedact(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"git status", "git status"},
		{"ls -la /tmp", "ls -la /tmp"},
		{"export PASSWORD=hunter2", "export PASSWORD=***"},
		{"curl --password=hunter2 host", "curl --password=*** host"},
		{"mysql --user=root --passwd=pw", "mysql --user=root --passwd=***"},
		{"API_KEY=abc123 make", "API_KEY=*** make"},
		{"x --api-key=zzz", "x --api-key=***"},
		{"TOKEN:abc", "TOKEN:***"},
		{"curl -H 'Authorization: Bearer abc.def' u", "curl -H 'Authorization: Bearer ***' u"},
		{"curl -H authorization:Basic dXNlcjpwdw==", "curl -H authorization:Basic ***"},
		{"Basic dXNlcjpwdw==", "Basic ***"},
		{"git clone https://user:pw@github.com/x/y.git", "git clone https://user:***@github.com/x/y.git"},
		{"git clone https://github.com/x/y.git", "git clone https://github.com/x/y.git"},
		{"echo ghp_abcdefghijklmnop1234", "echo ***"},
		{"echo 'github_pat_11ABCDEFGH0abcdefgh'", "echo '***'"},
		{"echo xoxb-1234-5678-abcdefgh", "echo ***"},
		{"echo sk-abcdefghijkl", "echo ***"},
		{"echo AKIAABCDEFGHIJKLMNOP", "echo ***"},
		{"echo AKIAshort", "echo AKIAshort"},
		{"echo sk-x", "echo sk-x"},
		{"echo eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnop", "echo ***"},
		{"echo a.b.c", "echo a.b.c"},
		{"key=value other=thing", "key=value other=thing"},
		{"a  \t b\nc", "a  \t b\nc"},
		{"author=bob pwd=x auth=y", "author=bob pwd=*** auth=***"},
		{"token=", "token="},
		{"=token", "=token"},
		{"naïve=ünï password=päss", "naïve=ünï password=***"},
	}
	for _, c := range cases {
		if got := redact.String(c.in); got != c.want {
			t.Errorf("redact.String(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func FuzzRedact(f *testing.F) {
	for _, s := range []string{"", "password=x", "Bearer", "Bearer a b", "https://a:b@c", "sk-aaaaaaaaaaaa", "\xff\xfe", "a=b=c:d", " \t\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := redact.String(s)
		if utf8.ValidString(s) && !utf8.ValidString(out) {
			t.Fatalf("redact produced invalid UTF-8 from %q: %q", s, out)
		}
		// Redaction only masks; it can never grow the text by more than a
		// small constant per word.
		if len(out) > len(s)+3*(strings.Count(s, " ")+strings.Count(s, "\n")+strings.Count(s, "\t")+1) {
			t.Fatalf("output grew: %q → %q", s, out)
		}
		if redact.String(out) != out {
			// Idempotence is not required but a second pass must not panic.
			_ = redact.String(redact.String(out))
		}
	})
}

func TestClipAndTailRunes(t *testing.T) {
	if got := clipRunes("héllo", 5); got != "héllo" {
		t.Errorf("clip no-op: %q", got)
	}
	if got := clipRunes("héllo wörld", 5); got != "héll…" || utf8.RuneCountInString(got) != 5 {
		t.Errorf("clip: %q", got)
	}
	if got := tailRunes("héllo wörld", 5); got != "wörld" {
		t.Errorf("tail: %q", got)
	}
	if got := tailRunes("abc", 5); got != "abc" {
		t.Errorf("tail no-op: %q", got)
	}
}
