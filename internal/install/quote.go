package install

import (
	"bytes"
	"errors"
	"strings"
)

// QuoteCommand renders argv as ONE command string for clients whose hook
// entries have no args field (Codex, Cursor, Hermes): POSIX single-quote
// quoting on unix ("'/opt/x y/ballast' hook --client codex --event Stop"),
// CommandLineToArgvW-compatible quoting on Windows (list2cmdline rules).
func QuoteCommand(argv []string, goos string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if goos == "windows" {
			parts[i] = quoteWindowsArg(a)
		} else {
			parts[i] = quotePOSIXArg(a)
		}
	}
	return strings.Join(parts, " ")
}

func posixSafe(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		strings.IndexByte("_@%+=:,./-", c) >= 0
}

func quotePOSIXArg(a string) string {
	if a == "" {
		return "''"
	}
	safe := true
	for i := 0; i < len(a); i++ {
		if !posixSafe(a[i]) {
			safe = false
			break
		}
	}
	if safe {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// quoteWindowsArg follows subprocess.list2cmdline: quote when the argument is
// empty or contains a space or tab; backslashes are doubled only in front of
// a quote (or the closing quote).
func quoteWindowsArg(a string) string {
	needQuote := a == "" || strings.ContainsAny(a, " \t")
	var b strings.Builder
	if needQuote {
		b.WriteByte('"')
	}
	bs := 0
	for i := 0; i < len(a); i++ {
		c := a[i]
		switch c {
		case '\\':
			bs++
		case '"':
			b.WriteString(strings.Repeat(`\`, bs*2+1))
			b.WriteByte('"')
			bs = 0
		default:
			b.WriteString(strings.Repeat(`\`, bs))
			bs = 0
			b.WriteByte(c)
		}
	}
	if bs > 0 {
		if needQuote {
			bs *= 2
		}
		b.WriteString(strings.Repeat(`\`, bs))
	}
	if needQuote {
		b.WriteByte('"')
	}
	return b.String()
}

// SplitCommand is the inverse of QuoteCommand (for ownership checks).
func SplitCommand(cmd, goos string) ([]string, error) {
	if goos == "windows" {
		return splitWindows(cmd)
	}
	return splitPOSIX(cmd)
}

var errUnterminatedQuote = errors.New("unterminated quote in command")

func isShellSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func splitPOSIX(s string) ([]string, error) {
	var out []string
	var cur bytes.Buffer
	have := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case isShellSpace(c):
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, errUnterminatedQuote
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
			have = true
		case c == '"':
			have = true
			i++
			closed := false
			for ; i < len(s); i++ {
				d := s[i]
				if d == '"' {
					closed = true
					break
				}
				if d == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`\n", s[i+1]) >= 0 {
					i++
					if s[i] == '\n' {
						continue
					}
					cur.WriteByte(s[i])
					continue
				}
				cur.WriteByte(d)
			}
			if !closed {
				return nil, errUnterminatedQuote
			}
		case c == '\\':
			if i+1 >= len(s) {
				cur.WriteByte('\\')
				have = true
				break
			}
			i++
			if s[i] != '\n' {
				cur.WriteByte(s[i])
				have = true
			}
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out, nil
}

// splitWindows implements the CommandLineToArgvW / MSVCRT argument rules.
func splitWindows(s string) ([]string, error) {
	var out []string
	var cur bytes.Buffer
	have, inQuote := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\':
			n := 0
			for i < len(s) && s[i] == '\\' {
				n++
				i++
			}
			if i < len(s) && s[i] == '"' {
				cur.WriteString(strings.Repeat(`\`, n/2))
				if n%2 == 1 {
					cur.WriteByte('"')
				} else {
					inQuote = !inQuote
				}
			} else {
				cur.WriteString(strings.Repeat(`\`, n))
				i--
			}
			have = true
		case c == '"':
			inQuote = !inQuote
			have = true
		case (c == ' ' || c == '\t') && !inQuote:
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	if inQuote {
		return nil, errUnterminatedQuote
	}
	if have {
		out = append(out, cur.String())
	}
	return out, nil
}
