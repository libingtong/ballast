package install

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// A line-level TOML scanner, just precise enough to locate table headers and
// simple key/value lines without being fooled by multi-line strings or
// arrays. It is NOT a validating parser: it rejects only what it cannot
// classify (so a corrupt file stops the installer instead of being edited).

type tlKind uint8

const (
	tlBlank tlKind = iota // blank or comment
	tlHeader
	tlKV
	tlCont // continuation of a multi-line value
)

type tline struct {
	text string
	eol  string
	kind tlKind
	path []string // header path (tlHeader) or key path (tlKV)
	arr  bool     // [[array]] header
	// tlKV: byte range of the value token on this line (comment excluded)
	valStart, valEnd int
}

type tomlDoc struct {
	lines []tline
	eol   string
	bom   string
}

var errTOMLSyntax = errors.New("invalid TOML")

// hasLoneCR reports a carriage return that is not part of a CRLF pair; such
// files have line breaks our line model cannot see.
func hasLoneCR(src string) bool {
	for i := 0; i < len(src); i++ {
		if src[i] == '\r' && (i+1 >= len(src) || src[i+1] != '\n') {
			return true
		}
	}
	return false
}

func splitLines(src string) (lines []string, eols []string) {
	for len(src) > 0 {
		i := strings.IndexByte(src, '\n')
		if i < 0 {
			lines = append(lines, src)
			eols = append(eols, "")
			break
		}
		text, eol := src[:i], "\n"
		if strings.HasSuffix(text, "\r") {
			text, eol = text[:len(text)-1], "\r\n"
		}
		lines = append(lines, text)
		eols = append(eols, eol)
		src = src[i+1:]
	}
	return lines, eols
}

// tlex tracks multi-line constructs across lines.
type tlex struct {
	depth int
	multi string // "" | `"""` | `'''`
}

func (lx *tlex) open() bool { return lx.depth > 0 || lx.multi != "" }

// scan consumes s (one physical line, or the value part of one) and returns
// the index where an unquoted comment starts (or len(s)).
func (lx *tlex) scan(s string) (end int, err error) {
	i := 0
	for i < len(s) {
		if lx.multi != "" {
			j := i
			for {
				k := strings.Index(s[j:], lx.multi)
				if k < 0 {
					return len(s), nil
				}
				k += j
				if lx.multi == `"""` {
					bs := 0
					for p := k - 1; p >= 0 && s[p] == '\\'; p-- {
						bs++
					}
					if bs%2 == 1 {
						j = k + 1
						continue
					}
				}
				// up to two extra quotes may close the string
				e := k + 3
				for e < len(s) && e < k+5 && s[e] == lx.multi[0] {
					e++
				}
				lx.multi = ""
				i = e
				break
			}
			continue
		}
		switch c := s[i]; c {
		case '#':
			return i, nil
		case '"', '\'':
			if strings.HasPrefix(s[i:], strings.Repeat(string(c), 3)) {
				lx.multi = strings.Repeat(string(c), 3)
				i += 3
				continue
			}
			j := i + 1
			for j < len(s) {
				if c == '"' && s[j] == '\\' {
					j += 2
					continue
				}
				if s[j] == c {
					break
				}
				j++
			}
			if j >= len(s) {
				return 0, fmt.Errorf("%w: unterminated string", errTOMLSyntax)
			}
			i = j + 1
		case '[', '{':
			lx.depth++
			i++
		case ']', '}':
			lx.depth--
			if lx.depth < 0 {
				return 0, fmt.Errorf("%w: unbalanced bracket", errTOMLSyntax)
			}
			i++
		default:
			i++
		}
	}
	return len(s), nil
}

// parseKeyPath parses a (possibly dotted, possibly quoted) key and returns
// the parts and the offset just after it.
func parseKeyPath(s string, from int) (parts []string, next int, err error) {
	i := from
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			return nil, 0, fmt.Errorf("%w: empty key", errTOMLSyntax)
		}
		switch c := s[i]; {
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(s) {
				if c == '"' && s[j] == '\\' {
					j += 2
					continue
				}
				if s[j] == c {
					break
				}
				j++
			}
			if j >= len(s) {
				return nil, 0, fmt.Errorf("%w: unterminated quoted key", errTOMLSyntax)
			}
			seg := s[i+1 : j]
			if c == '"' {
				if u, err := strconv.Unquote(s[i : j+1]); err == nil {
					seg = u
				}
			}
			parts = append(parts, seg)
			i = j + 1
		case isBareKeyChar(c):
			j := i
			for j < len(s) && isBareKeyChar(s[j]) {
				j++
			}
			parts = append(parts, s[i:j])
			i = j
		default:
			return nil, 0, fmt.Errorf("%w: unexpected %q in key", errTOMLSyntax, c)
		}
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i < len(s) && s[i] == '.' {
			i++
			continue
		}
		return parts, i, nil
	}
}

func isBareKeyChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func parseTOML(src []byte) (*tomlDoc, error) {
	text := string(bytes.TrimPrefix(src, []byte("\xef\xbb\xbf")))
	if hasLoneCR(text) {
		return nil, fmt.Errorf("%w: bare carriage return", errTOMLSyntax)
	}
	raw, eols := splitLines(text)
	d := &tomlDoc{eol: "\n"}
	if bytes.HasPrefix(src, []byte("\xef\xbb\xbf")) {
		d.bom = "\xef\xbb\xbf"
	}
	if bytes.Contains(src, []byte("\r\n")) {
		d.eol = "\r\n"
	}
	var lx tlex
	for n, l := range raw {
		tl := tline{text: l, eol: eols[n]}
		if lx.open() {
			tl.kind = tlCont
			if _, err := lx.scan(l); err != nil {
				return nil, fmt.Errorf("line %d: %w", n+1, err)
			}
			d.lines = append(d.lines, tl)
			continue
		}
		trim := strings.TrimLeft(l, " \t")
		off := len(l) - len(trim)
		switch {
		case trim == "" || trim[0] == '#':
			tl.kind = tlBlank
		case trim[0] == '[':
			tl.kind = tlHeader
			start := 1
			if strings.HasPrefix(trim, "[[") {
				tl.arr = true
				start = 2
			}
			parts, next, err := parseKeyPath(trim, start)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", n+1, err)
			}
			closer := "]"
			if tl.arr {
				closer = "]]"
			}
			if !strings.HasPrefix(trim[next:], closer) {
				return nil, fmt.Errorf("line %d: %w: bad table header", n+1, errTOMLSyntax)
			}
			rest := strings.TrimLeft(trim[next+len(closer):], " \t")
			if rest != "" && rest[0] != '#' {
				return nil, fmt.Errorf("line %d: %w: junk after table header", n+1, errTOMLSyntax)
			}
			tl.path = parts
		default:
			tl.kind = tlKV
			parts, next, err := parseKeyPath(trim, 0)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", n+1, err)
			}
			if next >= len(trim) || trim[next] != '=' {
				return nil, fmt.Errorf("line %d: %w: expected '='", n+1, errTOMLSyntax)
			}
			tl.path = parts
			vs := next + 1
			for vs < len(trim) && (trim[vs] == ' ' || trim[vs] == '\t') {
				vs++
			}
			end, err := lx.scan(trim[vs:])
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", n+1, err)
			}
			ve := vs + end
			for ve > vs && (trim[ve-1] == ' ' || trim[ve-1] == '\t') {
				ve--
			}
			if vs >= ve && !lx.open() {
				return nil, fmt.Errorf("line %d: %w: missing value", n+1, errTOMLSyntax)
			}
			tl.valStart, tl.valEnd = off+vs, off+ve
		}
		d.lines = append(d.lines, tl)
	}
	if lx.open() {
		return nil, fmt.Errorf("%w: unterminated multi-line value", errTOMLSyntax)
	}
	return d, nil
}

func (d *tomlDoc) bytes() []byte {
	var b bytes.Buffer
	b.WriteString(d.bom)
	for _, l := range d.lines {
		b.WriteString(l.text)
		b.WriteString(l.eol)
	}
	return b.Bytes()
}

func pathEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// tables returns the header indexes of non-array tables with the given path.
func (d *tomlDoc) tables(path []string) []int {
	var out []int
	for i, l := range d.lines {
		if l.kind == tlHeader && !l.arr && pathEq(l.path, path) {
			out = append(out, i)
		}
	}
	return out
}

// tableEnd returns the index of the first line after the table body.
func (d *tomlDoc) tableEnd(hdr int) int {
	for i := hdr + 1; i < len(d.lines); i++ {
		if d.lines[i].kind == tlHeader {
			return i
		}
	}
	return len(d.lines)
}

// rootEnd returns the index of the first header (the end of the root table).
func (d *tomlDoc) rootEnd() int {
	for i, l := range d.lines {
		if l.kind == tlHeader {
			return i
		}
	}
	return len(d.lines)
}

// kvIn finds a single-line key in lines [from,to).
func (d *tomlDoc) kvIn(from, to int, key []string) (idx int, ok bool) {
	for i := from; i < to; i++ {
		if d.lines[i].kind == tlKV && pathEq(d.lines[i].path, key) {
			return i, true
		}
	}
	return 0, false
}

func (d *tomlDoc) value(i int) string {
	l := d.lines[i]
	if l.valEnd > len(l.text) || l.valStart > l.valEnd {
		return ""
	}
	return l.text[l.valStart:l.valEnd]
}

// setValue replaces the value token of line i.
func (d *tomlDoc) setValue(i int, v string) {
	l := d.lines[i]
	l.text = l.text[:l.valStart] + v + l.text[l.valEnd:]
	l.valEnd = l.valStart + len(v)
	d.lines[i] = l
}

func (d *tomlDoc) insertLines(at int, texts ...string) {
	ins := make([]tline, len(texts))
	for i, t := range texts {
		ins[i] = tline{text: t, eol: d.eol}
	}
	d.lines = append(d.lines[:at], append(ins, d.lines[at:]...)...)
}

// appendBlock adds lines at the end of the document, separated from the
// previous content by one blank line.
func (d *tomlDoc) appendBlock(texts ...string) {
	if n := len(d.lines); n > 0 {
		if d.lines[n-1].eol == "" {
			d.lines[n-1].eol = d.eol
		}
		d.insertLines(len(d.lines), "")
	}
	d.insertLines(len(d.lines), texts...)
}

func (d *tomlDoc) removeRange(from, to int) {
	d.lines = append(d.lines[:from], d.lines[to:]...)
}

// removeTable deletes a table (header and body, trailing blank lines kept)
// together with the single blank line that precedes it.
func (d *tomlDoc) removeTable(hdr int) {
	end := d.tableEnd(hdr)
	for end > hdr+1 && d.lines[end-1].kind == tlBlank && strings.TrimSpace(d.lines[end-1].text) == "" {
		end--
	}
	from := hdr
	if from > 0 && d.lines[from-1].kind == tlBlank && strings.TrimSpace(d.lines[from-1].text) == "" {
		from--
	}
	d.removeRange(from, end)
}

func tomlString(s string) string {
	if !strings.ContainsAny(s, "'\n\r") {
		return "'" + s + "'"
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// unquoteTOMLValue returns the content of a simple one-line string value.
func unquoteTOMLValue(v string) (string, bool) {
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return v[1 : len(v)-1], true
	}
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		if u, err := strconv.Unquote(v); err == nil {
			return u, true
		}
	}
	return "", false
}
