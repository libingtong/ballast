package audit

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Secrets are assembled from parts so the source file itself carries no
// token-shaped literal that a repository scanner would flag.
var (
	ghTail   = strings.Repeat("a1B2", 9) // 36 chars
	awsKey   = "AKIA" + "IOSFODNN7EXAMPLE"
	awsTemp  = "ASIA" + "IOSFODNN7EXAMPLE"
	jwt      = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" + "." + "eyJzdWIiOiIxMjM0NTY3ODkwIn0" + "." + "dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"
	pemBody  = "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7\nabcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ\n"
	pemBlock = "-----BEGIN " + "PRIVATE KEY-----\n" + pemBody + "-----END " + "PRIVATE KEY-----"
)

// secretCases: after Redact, the secret substring must be gone.
var secretCases = []struct{ name, in, secret string }{
	{"aws access key", awsKey, awsKey},
	{"aws env export", "export AWS_ACCESS_KEY_ID=" + awsKey, awsKey},
	{"aws temp key", "creds " + awsTemp + " done", awsTemp},
	{"aws secret key", "aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
	{"aws secret json", `{"secretAccessKey": "abc/def+ghi0123"}`, "abc/def+ghi0123"},
	{"pem block", "before\n" + pemBlock + "\nafter", pemBody},
	{"pem rsa", "-----BEGIN RSA " + "PRIVATE KEY-----\nMIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu\n-----END RSA " + "PRIVATE KEY-----", "MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu"},
	{"pem unterminated", "-----BEGIN OPENSSH " + "PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW", "b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQ"},
	{"bearer header", "Authorization: Bearer abcdef1234567890abcdef", "abcdef1234567890abcdef"},
	{"bearer curl", `curl -H "Authorization: Bearer ` + jwt + `" https://api.example.com`, jwt},
	{"basic header", "Authorization: Basic dXNlcjpwYXNzd29yZA==", "dXNlcjpwYXNzd29yZA=="},
	{"bearer bare", "got bearer tok_abcdefghijkl back", "tok_abcdefghijkl"},
	{"bearer lowercase header", `-H 'authorization: bearer xyzxyzxyzxyz'`, "xyzxyzxyzxyz"},
	{"password eq", "password=hunter2hunter2", "hunter2hunter2"},
	{"password quoted", `PASSWORD="my secret pass"`, "my secret pass"},
	{"password single", `password='it''s'`, "it"},
	{"passwd colon", "passwd: s3cr3tvalue", "s3cr3tvalue"},
	{"password json", `{"password": "p@ssw0rd!x"}`, "p@ssw0rd!x"},
	{"password mixed case", "Password=Abc12345", "Abc12345"},
	{"db password", "db_password: abc123def", "abc123def"},
	{"pgpassword", "PGPASSWORD=pgpw123 psql -h db", "pgpw123"},
	{"flag password space", "tool --password hunter2xyz --verbose", "hunter2xyz"},
	{"flag password eq", "mysql -u root --password=rootpw123", "rootpw123"},
	{"flag token eq", "cli --token=abcDEF123456", "abcDEF123456"},
	{"flag api-key", "cli --api-key kk-12345678 run", "kk-12345678"},
	{"api key env", "API_KEY=abcdef0123456789", "abcdef0123456789"},
	{"apikey json", `{"apikey": "k-1234abcd"}`, "k-1234abcd"},
	{"x-api-key header", "X-Api-Key: abcdef123456", "abcdef123456"},
	{"client secret", "client_secret=Zm9vYmFyYmF6", "Zm9vYmFyYmF6"},
	{"secret colon", "secret: topsecretvalue", "topsecretvalue"},
	{"secret key django", "SECRET_KEY=django-insecure-abc123", "django-insecure-abc123"},
	{"access token", "access_token=ya29.a0AfH6SMBxQ", "ya29.a0AfH6SMBxQ"},
	{"token single quoted", "token = 'single quoted tok'", "single quoted tok"},
	{"token json", `{"token":"abc.def.ghi"}`, "abc.def.ghi"},
	{"private key field", "private_key: MIIEvQIBADANBg", "MIIEvQIBADANBg"},
	{"credentials", "credentials=AbCdEf123", "AbCdEf123"},
	{"multiline second line", "line one\nsecret=second-line-secret\nline three", "second-line-secret"},
	{"github pat classic", "token " + "gh" + "p_" + ghTail, "gh" + "p_" + ghTail},
	{"github env", "GITHUB_TOKEN=" + "gh" + "p_" + ghTail, "gh" + "p_" + ghTail},
	{"github oauth", "x gh" + "o_" + ghTail + " y", "gh" + "o_" + ghTail},
	{"github server", "gh" + "s_" + ghTail, "gh" + "s_" + ghTail},
	{"github user", "gh" + "u_" + ghTail, "gh" + "u_" + ghTail},
	{"github refresh", "gh" + "r_" + ghTail, "gh" + "r_" + ghTail},
	{"github fine grained", "github_pat_" + "11ABCDEFG0abcdefghij_klmnopqrstuvwxyz0123456789", "11ABCDEFG0abcdefghij_klmnopqrstuvwxyz0123456789"},
	{"github in clone url", "git clone https://x-access-token:" + "gh" + "s_" + ghTail + "@github.com/o/r", ghTail},
	{"gitlab pat", "glpat-" + "abcdefghij0123456789", "abcdefghij0123456789"},
	{"npm token", "npm_" + strings.Repeat("aB3", 12), strings.Repeat("aB3", 12)},
	{"slack bot", "xox" + "b-123456789012-abcdefghijklmnop", "123456789012-abcdefghijklmnop"},
	{"slack user", "xox" + "p-123456789012-123456789012-abcdef0123", "123456789012-123456789012-abcdef0123"},
	{"slack app", "xox" + "a-2-abcdefghijkl", "abcdefghijkl"},
	{"slack session", "xox" + "s-123456789012-abcdef", "123456789012-abcdef"},
	{"openai key", "OPENAI_API_KEY=\"sk-" + "proj-abcdefghijklmnop\"", "abcdefghijklmnop"},
	{"anthropic key", "ANTHROPIC_API_KEY=sk-" + "ant-api03-" + strings.Repeat("A", 20), strings.Repeat("A", 20)},
	{"sk bare", "use sk-" + "abcdefghijklmnopqrstuv now", "abcdefghijklmnopqrstuv"},
	{"stripe live", "rk_" + "live_" + "abcdefghijklmnop1234", "abcdefghijklmnop1234"},
	{"stripe secret test", "sk_" + "test_" + "abcdefghijklmnop1234", "abcdefghijklmnop1234"},
	{"google api key", "key AIza" + "SyA-abcdefghijklmnopqrstuvwxyz01234", "SyA-abcdefghijklmnopqrstuvwxyz01234"},
	{"jwt", "session=" + jwt, jwt},
	{"jwt bare", "got " + jwt + " ok", "dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"},
	{"url creds https", "https://user:p4ssw0rd@example.com/x", "p4ssw0rd"},
	{"url creds postgres", "postgres://admin:s3cretpw@db:5432/app", "s3cretpw"},
	{"npm npmrc", "//registry.npmjs.org/:_authToken=abcdefghijklmnop", "abcdefghijklmnop"},
}

func TestRedactSecrets(t *testing.T) {
	if len(secretCases) < 50 {
		t.Fatalf("only %d samples; the plan asks for 50+", len(secretCases))
	}
	for _, tc := range secretCases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.in)
			if strings.Contains(got, tc.secret) {
				t.Errorf("secret leaked:\n in: %q\nout: %q", tc.in, got)
			}
			if !strings.Contains(got, "***") {
				t.Errorf("no mask in %q", got)
			}
			if again := Redact(got); again != got {
				t.Errorf("not idempotent:\n  once: %q\n twice: %q", got, again)
			}
			if !utf8.ValidString(got) {
				t.Error("invalid UTF-8 produced")
			}
		})
	}
}

func TestRedactKeepsContext(t *testing.T) {
	tests := []struct{ in, want string }{
		{"password=hunter2hunter2 next", "password=*** next"},
		{"Authorization: Bearer abcdef1234567890", "Authorization: ***"},
		{"got bearer tok_abcdefghijkl back", "got bearer *** back"},
		{"https://user:p4ssw0rd@example.com/x", "https://user:***@example.com/x"},
		{"cli --token=abc123 run", "cli --token=*** run"},
		{"tool --password hunter2xyz --verbose", "tool --password *** --verbose"},
		{"line\nsecret=abc\nend", "line\nsecret=***\nend"},
	}
	for _, tc := range tests {
		if got := Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRedactLeavesOrdinaryTextAlone(t *testing.T) {
	for _, s := range []string{
		"",
		"git status",
		"ls -la /tmp && go test ./...",
		"Bash: git commit -m 'fix typo'",
		"Edit: internal/state/store.go",
		"commit a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
		"https://example.com/path?q=1&r=2",
		"ssh://git@github.com/o/r.git",
		"mail me@example.com",
		"the tokenizer is fast",
		"keyboard: us layout",
		"passenger list",
		"这是一个中文 描述,没有密钥",
		"max length 200 runes",
		"curl -s localhost:8080/health",
		"export PATH=/usr/bin:/bin",
		"sk-short",
		"AKIA-not-a-key",
	} {
		if got := Redact(s); got != s {
			t.Errorf("Redact(%q) = %q, want unchanged", s, got)
		}
	}
}

func FuzzRedact(f *testing.F) {
	for _, tc := range secretCases {
		f.Add(tc.in)
	}
	f.Add("")
	f.Add("password=")
	f.Add("-----BEGIN PRIVATE KEY-----")
	f.Add("://:@")
	f.Add("Authorization:")
	f.Fuzz(func(t *testing.T, s string) {
		got := Redact(s)
		if utf8.ValidString(s) && !utf8.ValidString(got) {
			t.Fatalf("Redact(%q) produced invalid UTF-8: %q", s, got)
		}
		if again := Redact(got); again != got {
			t.Fatalf("not idempotent on %q:\n  once: %q\n twice: %q", s, got, again)
		}
		if len(got) > 3*len(s)+16 {
			t.Fatalf("output grew from %d to %d bytes", len(s), len(got))
		}
	})
}
