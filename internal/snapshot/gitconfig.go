package snapshot

import (
	"bytes"
	"strconv"
	"strings"
)

// scanConfig walks the key lines of a git config file. section and key are
// lower-cased; sub (the subsection) keeps its case. raw is the value text
// after '=' trimmed of surrounding blanks and NOT unquoted (see cfgString);
// hasEq is false for a bare key (boolean true). It is a deliberately small
// scanner: includes are reported as keys of the "include"/"includeif"
// sections, never followed here.
func scanConfig(data []byte, visit func(section, sub, key, raw string, hasEq bool)) {
	var section, sub string
	for len(data) > 0 {
		var line []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			line, data = data, nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			var rest []byte
			var ok bool
			section, sub, rest, ok = parseSectionHeader(line)
			if !ok {
				section, sub = "", ""
				continue
			}
			line = bytes.TrimSpace(rest)
			if len(line) == 0 || line[0] == '#' || line[0] == ';' {
				continue
			}
		}
		if section == "" {
			continue
		}
		i := 0
		for i < len(line) && (line[i] == '-' || line[i] >= '0' && line[i] <= '9' || line[i] >= 'a' && line[i] <= 'z' || line[i] >= 'A' && line[i] <= 'Z') {
			i++
		}
		if i == 0 {
			continue
		}
		key := strings.ToLower(string(line[:i]))
		rest := bytes.TrimLeft(line[i:], " \t")
		if len(rest) == 0 || rest[0] == '#' || rest[0] == ';' {
			visit(section, sub, key, "", false)
			continue
		}
		if rest[0] != '=' {
			continue
		}
		val := bytes.TrimSpace(rest[1:])
		// Continuation lines: a trailing odd backslash joins the next line.
		for trailingBackslashes(val)%2 == 1 && len(data) > 0 {
			val = val[:len(val)-1]
			var next []byte
			if j := bytes.IndexByte(data, '\n'); j >= 0 {
				next, data = data[:j], data[j+1:]
			} else {
				next, data = data, nil
			}
			val = append(append([]byte{}, val...), bytes.TrimRight(next, "\r")...)
		}
		visit(section, sub, key, string(val), true)
	}
}

func trailingBackslashes(b []byte) int {
	n := 0
	for i := len(b) - 1; i >= 0 && b[i] == '\\'; i-- {
		n++
	}
	return n
}

// parseSectionHeader parses `[name]`, `[name "sub"]` and the legacy
// `[name.sub]`; rest is whatever follows the closing bracket.
func parseSectionHeader(line []byte) (section, sub string, rest []byte, ok bool) {
	i := 1
	for i < len(line) && line[i] != ']' && line[i] != ' ' && line[i] != '\t' && line[i] != '"' {
		i++
	}
	name := string(line[1:i])
	if i >= len(line) || name == "" {
		return "", "", nil, false
	}
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i < len(line) && line[i] == '"' {
		i++
		var b strings.Builder
		for i < len(line) && line[i] != '"' {
			if line[i] == '\\' && i+1 < len(line) {
				i++
			}
			b.WriteByte(line[i])
			i++
		}
		if i >= len(line) {
			return "", "", nil, false
		}
		i++ // closing quote
		sub = b.String()
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
	} else if dot := strings.IndexByte(name, '.'); dot >= 0 {
		sub = strings.ToLower(name[dot+1:])
		name = name[:dot]
	}
	if i >= len(line) || line[i] != ']' {
		return "", "", nil, false
	}
	return strings.ToLower(name), sub, line[i+1:], true
}

// cfgString interprets a raw value the way git does: surrounding blanks and
// unquoted trailing comments are dropped, double quotes are removed and the
// \n \t \b \\ \" escapes are decoded.
func cfgString(raw string) string {
	var b strings.Builder
	inQuote := false
	trimTo := 0
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '\\' && i+1 < len(raw):
			i++
			switch raw[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'b':
				b.WriteByte('\b')
			default:
				b.WriteByte(raw[i])
			}
			trimTo = b.Len()
		case c == '"':
			inQuote = !inQuote
			trimTo = b.Len()
		case !inQuote && (c == '#' || c == ';'):
			return strings.TrimLeft(b.String()[:trimTo], " \t")
		case !inQuote && (c == ' ' || c == '\t'):
			b.WriteByte(c)
		default:
			b.WriteByte(c)
			trimTo = b.Len()
		}
	}
	return strings.TrimLeft(b.String()[:trimTo], " \t")
}

// cfgBool parses a git boolean. ok is false for values git would reject.
func cfgBool(raw string, hasEq bool) (val, ok bool) {
	if !hasEq {
		return true, true
	}
	s := strings.ToLower(cfgString(raw))
	switch s {
	case "", "no", "off", "false":
		return false, true
	case "yes", "on", "true":
		return true, true
	}
	n, err := parseConfigInt(s)
	if err != nil {
		return false, false
	}
	return n != 0, true
}

// parseConfigInt parses an integer with git's optional k/m/g suffix.
func parseConfigInt(s string) (int64, error) {
	mult := int64(1)
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'k':
			mult, s = 1<<10, s[:n-1]
		case 'm':
			mult, s = 1<<20, s[:n-1]
		case 'g':
			mult, s = 1<<30, s[:n-1]
		}
	}
	v, err := strconv.ParseInt(s, 0, 64)
	return v * mult, err
}
