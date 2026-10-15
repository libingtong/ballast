package bashparse

import (
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// StripWrappers removes leading wrapper commands from argv and returns the
// remaining argv, the stripped wrapper names, and whether sudo was stripped.
//
// A wrapper is only stripped when a command word remains after it; otherwise
// the wrapper itself is the command (`sudo`, `env`, `exec >file`, ...).
func StripWrappers(argv []model.Word) (rest []model.Word, wrappers []string, sudo bool) {
	rest = argv
	for len(rest) > 0 && !rest[0].Dynamic {
		name := baseName(rest[0].Lit)
		var n int
		switch name {
		case "env":
			n = skipEnv(rest)
		case "nice":
			n = skipNice(rest)
		case "nohup":
			n = 1
		case "timeout":
			n = skipTimeout(rest)
		case "time":
			n = skipTime(rest)
		case "command":
			n = skipCommand(rest)
		case "exec":
			n = skipExec(rest)
		case "sudo":
			n = skipSudo(rest)
		}
		if n <= 0 || n >= len(rest) {
			break
		}
		wrappers = append(wrappers, name)
		if name == "sudo" {
			sudo = true
		}
		rest = rest[n:]
	}
	return rest, wrappers, sudo
}

// isAssignWord reports whether w looks like NAME=value (also when the value
// is dynamic).
func isAssignWord(w model.Word) bool {
	s := w.Lit
	eq := strings.IndexByte(s, '=')
	if eq <= 0 {
		return false
	}
	for i := 0; i < eq; i++ {
		c := s[i]
		switch {
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// shortValue consumes one short-option cluster word at args[i] (starting with
// a single '-') whose letters in valueChars take a value (attached rest of the
// cluster, else the next word). It returns the index of the next unconsumed
// word.
func shortValue(args []model.Word, i int, valueChars string) int {
	s := args[i].Lit
	for j := 1; j < len(s); j++ {
		if strings.IndexByte(valueChars, s[j]) >= 0 {
			if j+1 < len(s) {
				return i + 1
			}
			return i + 2
		}
	}
	return i + 1
}

func isNumberWord(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// skipEnv returns the number of words env and its options/assignments span,
// or 0 when env cannot be stripped safely.
func skipEnv(a []model.Word) int {
	i := 1
	optsDone := false
	for i < len(a) {
		w := a[i]
		s := w.Lit
		if isAssignWord(w) {
			i++
			continue
		}
		if w.Dynamic {
			break
		}
		if optsDone || len(s) < 2 || s[0] != '-' {
			if s == "-" && !optsDone {
				i++
				continue
			}
			break
		}
		if s == "--" {
			optsDone = true
			i++
			continue
		}
		if strings.HasPrefix(s, "--") {
			name, _, attached := strings.Cut(s[2:], "=")
			switch name {
			case "split-string":
				return 0
			case "unset", "chdir", "argv0":
				if attached {
					i++
				} else {
					i += 2
				}
			default:
				i++
			}
			continue
		}
		if envSplitString(s) {
			return 0
		}
		i = shortValue(a, i, "uCa")
	}
	return i
}

func skipNice(a []model.Word) int {
	i := 1
	for i < len(a) {
		w := a[i]
		s := w.Lit
		if w.Dynamic {
			break
		}
		switch {
		case s == "--":
			return i + 1
		case s == "-n" || s == "--adjustment":
			i += 2
		case strings.HasPrefix(s, "-n") || strings.HasPrefix(s, "--adjustment="):
			i++
		case len(s) > 1 && s[0] == '-' && isNumberWord(strings.TrimLeft(s[1:], "-+")):
			i++
		default:
			return i
		}
	}
	return i
}

func skipTimeout(a []model.Word) int {
	i := 1
	for i < len(a) {
		w := a[i]
		s := w.Lit
		if w.Dynamic || len(s) < 2 || s[0] != '-' {
			break
		}
		if s == "--" {
			i++
			break
		}
		if strings.HasPrefix(s, "--") {
			name, _, attached := strings.Cut(s[2:], "=")
			if (name == "signal" || name == "kill-after") && !attached {
				i += 2
			} else {
				i++
			}
			continue
		}
		i = shortValue(a, i, "sk")
	}
	if i < len(a) {
		i++ // the DURATION operand
	}
	return i
}

func skipTime(a []model.Word) int {
	i := 1
	for i < len(a) {
		w := a[i]
		s := w.Lit
		if w.Dynamic || len(s) < 2 || s[0] != '-' {
			break
		}
		if s == "--" {
			i++
			break
		}
		if strings.HasPrefix(s, "--") {
			name, _, attached := strings.Cut(s[2:], "=")
			if (name == "format" || name == "output") && !attached {
				i += 2
			} else {
				i++
			}
			continue
		}
		i = shortValue(a, i, "fo")
	}
	return i
}

func skipCommand(a []model.Word) int {
	i := 1
	for i < len(a) {
		w := a[i]
		s := w.Lit
		if w.Dynamic || len(s) < 2 || s[0] != '-' {
			break
		}
		if s == "--" {
			i++
			break
		}
		if strings.ContainsAny(s[1:], "vV") {
			return 0 // command -v/-V only looks the name up
		}
		i++
	}
	return i
}

func skipExec(a []model.Word) int {
	i := 1
	for i < len(a) {
		w := a[i]
		s := w.Lit
		if w.Dynamic || len(s) < 2 || s[0] != '-' {
			break
		}
		if s == "--" {
			i++
			break
		}
		i = shortValue(a, i, "a")
	}
	return i
}

func skipSudo(a []model.Word) int {
	i := 1
	optsDone := false
	for i < len(a) {
		w := a[i]
		s := w.Lit
		if isAssignWord(w) {
			i++
			continue
		}
		if w.Dynamic || optsDone || len(s) < 2 || s[0] != '-' {
			break
		}
		if s == "--" {
			optsDone = true
			i++
			continue
		}
		if strings.HasPrefix(s, "--") {
			name, _, attached := strings.Cut(s[2:], "=")
			switch name {
			case "user", "group", "prompt", "role", "type", "command-timeout", "chdir",
				"chroot", "other-user", "close-from", "host", "login-class":
				if attached {
					i++
				} else {
					i += 2
				}
			default:
				i++
			}
			continue
		}
		i = shortValue(a, i, "CDRUcgprtTu")
	}
	return i
}

// envSplitString reports whether the short-option cluster s uses -S, whose
// value is a whole command line that cannot be split statically.
func envSplitString(s string) bool {
	for j := 1; j < len(s); j++ {
		switch s[j] {
		case 'S':
			return true
		case 'u', 'C', 'a':
			return false
		}
	}
	return false
}
