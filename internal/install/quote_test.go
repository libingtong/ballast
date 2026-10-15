package install

import (
	"reflect"
	"testing"
)

func TestQuoteCommand(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		goos string
		want string
	}{
		{"unix plain", []string{"/opt/ballast/bin/ballast", "hook", "--client", "codex", "--event", "Stop"}, "linux",
			"/opt/ballast/bin/ballast hook --client codex --event Stop"},
		{"unix space in binary (doc example)", []string{"/opt/x y/ballast", "hook", "--client", "codex", "--event", "Stop"}, "darwin",
			"'/opt/x y/ballast' hook --client codex --event Stop"},
		{"unix single quote", []string{"/o'p/b", "x"}, "linux", `'/o'\''p/b' x`},
		{"unix empty arg", []string{"a", "", "b"}, "linux", "a '' b"},
		{"unix shell metachars", []string{"/a/$HOME;b", "`c`", "d*e", "~"}, "linux", `'/a/$HOME;b' '` + "`c`" + `' 'd*e' '~'`},
		{"unix unicode", []string{"/opt/日本/b"}, "linux", "'/opt/日本/b'"},
		{"unix newline", []string{"a\nb"}, "linux", "'a\nb'"},
		{"windows plain", []string{`C:\bin\ballast.exe`, "hook", "--client", "codex"}, "windows",
			`C:\bin\ballast.exe hook --client codex`},
		{"windows program files", []string{`C:\Program Files\Ballast\ballast.exe`, "hook"}, "windows",
			`"C:\Program Files\Ballast\ballast.exe" hook`},
		{"windows trailing backslash quoted", []string{`C:\a b\`, "x"}, "windows", `"C:\a b\\" x`},
		{"windows embedded quote", []string{`a"b`, `c d"e`}, "windows", `a\"b "c d\"e"`},
		{"windows backslashes before quote", []string{`a\\"b`}, "windows", `a\\\\\"b`},
		{"windows empty", []string{""}, "windows", `""`},
	}
	for _, c := range cases {
		got := QuoteCommand(c.argv, c.goos)
		if got != c.want {
			t.Errorf("%s: QuoteCommand = %q, want %q", c.name, got, c.want)
		}
		back, err := SplitCommand(got, c.goos)
		if err != nil || !reflect.DeepEqual(back, c.argv) {
			t.Errorf("%s: SplitCommand(%q) = %q, %v; want %q", c.name, got, back, err, c.argv)
		}
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		in   string
		goos string
		want []string
		bad  bool
	}{
		{in: `a  b	c`, goos: "linux", want: []string{"a", "b", "c"}},
		{in: `a "b c" 'd e'`, goos: "linux", want: []string{"a", "b c", "d e"}},
		{in: `a\ b "x\"y" "p\qr"`, goos: "linux", want: []string{"a b", `x"y`, `p\qr`}},
		{in: `''`, goos: "linux", want: []string{""}},
		{in: `a'b'"c"`, goos: "linux", want: []string{"abc"}},
		{in: ``, goos: "linux", want: nil},
		{in: `a 'b`, goos: "linux", bad: true},
		{in: `a "b`, goos: "linux", bad: true},
		{in: `"C:\Program Files\x" a`, goos: "windows", want: []string{`C:\Program Files\x`, "a"}},
		{in: `a "" b`, goos: "windows", want: []string{"a", "", "b"}},
		{in: `a "b`, goos: "windows", bad: true},
		{in: `x\\\"y`, goos: "windows", want: []string{`x\"y`}},
	}
	for _, c := range cases {
		got, err := SplitCommand(c.in, c.goos)
		if c.bad {
			if err == nil {
				t.Errorf("SplitCommand(%q,%s) should fail, got %q", c.in, c.goos, got)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitCommand(%q,%s) = %q, %v; want %q", c.in, c.goos, got, err, c.want)
		}
	}
}

func FuzzQuoteSplit(f *testing.F) {
	seeds := []string{"", "a", "a b", "a'b", `a"b`, `a\b`, `a\`, `\"`, "a\tb", "$x", "日本", "-- x", " ", "''", `"`, `\\"`}
	for _, s := range seeds {
		f.Add(s, "x y", "/opt/ballast")
	}
	f.Fuzz(func(t *testing.T, a, b, c string) {
		for _, s := range []string{a, b, c} {
			for i := 0; i < len(s); i++ {
				if s[i] == 0 {
					t.Skip()
				}
			}
		}
		argv := []string{c, "hook", a, b}
		for _, goos := range []string{"linux", "windows"} {
			q := QuoteCommand(argv, goos)
			back, err := SplitCommand(q, goos)
			if err != nil || !reflect.DeepEqual(back, argv) {
				t.Fatalf("%s: %q -> %q -> %q (%v)", goos, argv, q, back, err)
			}
		}
	})
}
