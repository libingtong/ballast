package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Line-level editing of the top-level `hooks:` block of Hermes' config.yaml.
// Nothing outside that block is parsed or touched. Only block-style YAML is
// understood inside the block; flow collections (other than the empty ones),
// anchors, aliases and tags make the block "unsupported", which the installer
// reports as model.ErrSettingsParse before writing anything.

type yline struct {
	text string
	eol  string
}

type yamlDoc struct {
	lines  []yline
	eol    string
	bom    string
	loneCR bool
}

var errYAMLUnsupported = errors.New("unsupported YAML in the hooks block")

func parseYAMLDoc(src []byte) *yamlDoc {
	d := &yamlDoc{eol: "\n"}
	if bytes.HasPrefix(src, []byte("\xef\xbb\xbf")) {
		d.bom = "\xef\xbb\xbf"
		src = src[3:]
	}
	if bytes.Contains(src, []byte("\r\n")) {
		d.eol = "\r\n"
	}
	d.loneCR = hasLoneCR(string(src))
	raw, eols := splitLines(string(src))
	for i := range raw {
		d.lines = append(d.lines, yline{raw[i], eols[i]})
	}
	return d
}

func (d *yamlDoc) bytes() []byte {
	var b bytes.Buffer
	b.WriteString(d.bom)
	for _, l := range d.lines {
		b.WriteString(l.text)
		b.WriteString(l.eol)
	}
	return b.Bytes()
}

func (d *yamlDoc) insert(at int, texts ...string) {
	ins := make([]yline, len(texts))
	for i, t := range texts {
		ins[i] = yline{t, d.eol}
	}
	d.lines = append(d.lines[:at], append(ins, d.lines[at:]...)...)
}

func (d *yamlDoc) remove(from, to int) { // [from,to)
	d.lines = append(d.lines[:from], d.lines[to:]...)
}

func (d *yamlDoc) blank(i int) bool { return strings.TrimSpace(d.lines[i].text) == "" }

func (d *yamlDoc) isBlankDoc() bool {
	for i := range d.lines {
		if !d.blank(i) {
			return false
		}
	}
	return true
}

// appendBlock adds lines at the end, after one separating blank line.
func (d *yamlDoc) appendBlock(texts ...string) {
	if n := len(d.lines); n > 0 {
		if d.lines[n-1].eol == "" {
			d.lines[n-1].eol = d.eol
		}
		d.insert(len(d.lines), "")
	}
	d.insert(len(d.lines), texts...)
}

type yItem struct {
	start, end int // inclusive line range
	opaque     bool
	obj        map[string]any
	command    string
}

type yEvent struct {
	name      string
	keyLine   int
	indent    int
	emptyFlow bool
	items     []yItem
	itemInd   int // indentation of "- ", -1 when no item
}

func (e *yEvent) insertAfter() int {
	if n := len(e.items); n > 0 {
		return e.items[n-1].end
	}
	return e.keyLine
}

type ySection struct {
	hdr         int // -1 when absent
	last        int // last indented line of the block (hdr when none)
	inlineEmpty bool
	keyIndent   int // indentation of event keys; -1 when none
	events      []*yEvent
	hasContent  bool // any non-comment content line in the block
	hasComment  bool // any comment line inside the block
}

func (s *ySection) event(name string) *yEvent {
	for _, e := range s.events {
		if e.name == name {
			return e
		}
	}
	return nil
}

func isHooksKeyLine(text string) (rest string, ok bool) {
	s := text
	switch {
	case strings.HasPrefix(s, `"hooks"`), strings.HasPrefix(s, `'hooks'`):
		s = s[7:]
	case strings.HasPrefix(s, "hooks"):
		s = s[5:]
	default:
		return "", false
	}
	s = strings.TrimLeft(s, " \t")
	if !strings.HasPrefix(s, ":") {
		return "", false
	}
	s = s[1:]
	if s != "" && s[0] != ' ' && s[0] != '\t' {
		return "", false
	}
	return stripYAMLComment(strings.TrimSpace(s)), true
}

// stripYAMLComment removes a trailing " # comment" outside quotes.
func stripYAMLComment(s string) string {
	if strings.HasPrefix(s, "#") {
		return ""
	}
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == '\\' && q == '"' {
				i++
			} else if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' || s[i-1] == ':' {
				q = c
			}
		case c == '#' && i > 0 && (s[i-1] == ' ' || s[i-1] == '\t'):
			return strings.TrimRight(s[:i], " \t")
		}
	}
	return strings.TrimSpace(s)
}

func leadingSpaces(s string) (n int, tab bool) {
	for n < len(s) && (s[n] == ' ' || s[n] == '\t') {
		if s[n] == '\t' {
			tab = true
		}
		n++
	}
	return n, tab
}

func isTopLevel(text string) bool {
	return text != "" && text[0] != ' ' && text[0] != '\t' && text[0] != '#'
}

func unsupported(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errYAMLUnsupported, fmt.Sprintf(format, a...))
}

func (d *yamlDoc) section() (*ySection, error) {
	s := &ySection{hdr: -1, last: -1, keyIndent: -1}
	if d.loneCR {
		return nil, unsupported("bare carriage return line breaks")
	}
	for i, l := range d.lines {
		if !isTopLevel(l.text) {
			continue
		}
		rest, ok := isHooksKeyLine(l.text)
		if !ok {
			continue
		}
		if s.hdr >= 0 {
			return nil, unsupported("duplicate top-level hooks key at line %d", i+1)
		}
		switch rest {
		case "":
		case "{}", "[]", "null", "~":
			s.inlineEmpty = true
		default:
			return nil, unsupported("hooks value %q at line %d is not a block mapping", rest, i+1)
		}
		s.hdr = i
	}
	if s.hdr < 0 {
		return s, nil
	}
	end := len(d.lines)
	for i := s.hdr + 1; i < len(d.lines); i++ {
		if isTopLevel(d.lines[i].text) {
			end = i
			break
		}
	}
	s.last = s.hdr
	for i := s.hdr + 1; i < end; i++ {
		if !d.blank(i) && (d.lines[i].text[0] == ' ' || d.lines[i].text[0] == '\t') {
			s.last = i
		}
	}
	if s.inlineEmpty && s.last > s.hdr {
		return nil, unsupported("hooks is an empty flow value but has indented content")
	}
	var ev *yEvent
	var it *yItem
	itemKeyCol := 0
	for i := s.hdr + 1; i <= s.last; i++ {
		text := d.lines[i].text
		if d.blank(i) {
			continue
		}
		ind, tab := leadingSpaces(text)
		body := text[ind:]
		if body[0] == '#' {
			s.hasComment = true
			continue
		}
		if tab {
			return nil, unsupported("tab indentation at line %d", i+1)
		}
		s.hasContent = true
		if s.keyIndent < 0 {
			if body == "-" || strings.HasPrefix(body, "- ") {
				return nil, unsupported("hooks is a sequence at line %d", i+1)
			}
			s.keyIndent = ind
		}
		isItem := body == "-" || strings.HasPrefix(body, "- ")
		switch {
		case ind < s.keyIndent:
			return nil, unsupported("inconsistent indentation at line %d", i+1)
		case ind == s.keyIndent && !isItem:
			name, rest, ok := parseYAMLKeyLine(body)
			if !ok {
				return nil, unsupported("cannot parse line %d", i+1)
			}
			e := &yEvent{name: name, keyLine: i, indent: ind, itemInd: -1}
			switch rest {
			case "":
			case "[]", "null", "~":
				e.emptyFlow = true
			default:
				return nil, unsupported("event %q at line %d is not a block sequence", name, i+1)
			}
			s.events = append(s.events, e)
			ev, it = e, nil
		case isItem && ev != nil && ev.itemInd >= 0 && ind > ev.itemInd && it != nil:
			it.end = i // a nested sequence inside the current item
			it.opaque = true
		case isItem:
			if ev == nil {
				return nil, unsupported("sequence item outside an event at line %d", i+1)
			}
			if ev.itemInd >= 0 && ev.itemInd != ind {
				return nil, unsupported("inconsistent sequence indentation at line %d", i+1)
			}
			ev.itemInd = ind
			ev.emptyFlow = false
			ev.items = append(ev.items, yItem{start: i, end: i, obj: map[string]any{}})
			it = &ev.items[len(ev.items)-1]
			after := body[1:]
			sp, _ := leadingSpaces(after)
			itemKeyCol = ind + 1 + sp
			first := strings.TrimSpace(after)
			if first == "" || first[0] == '#' {
				it.opaque = true
			} else {
				it.addField(first, false)
			}
		default: // continuation of the current item
			if it == nil || ind <= ev.itemInd {
				return nil, unsupported("unexpected content at line %d", i+1)
			}
			it.end = i
			if ind != itemKeyCol {
				it.opaque = true
			} else {
				it.addField(body, true)
			}
		}
	}
	for _, e := range s.events {
		for k := range e.items {
			it := &e.items[k]
			if c, ok := it.obj["command"].(string); ok && !it.opaque {
				it.command = c
			}
		}
	}
	return s, nil
}

// addField parses "key: scalar" into the item object.
func (it *yItem) addField(s string, _ bool) {
	name, rest, ok := parseYAMLKeyLine(s)
	if !ok {
		it.opaque = true
		return
	}
	if _, dup := it.obj[name]; dup {
		it.opaque = true
		return
	}
	v, ok := parseYAMLScalar(rest)
	if !ok {
		it.opaque = true
		return
	}
	it.obj[name] = v
}

// parseYAMLKeyLine splits "name: rest" (comments stripped from rest).
func parseYAMLKeyLine(s string) (name, rest string, ok bool) {
	if s == "" {
		return "", "", false
	}
	i := 0
	switch s[0] {
	case '"', '\'':
		q := s[0]
		j := 1
		for j < len(s) {
			if q == '"' && s[j] == '\\' {
				j += 2
				continue
			}
			if s[j] == q {
				if q == '\'' && j+1 < len(s) && s[j+1] == '\'' {
					j += 2
					continue
				}
				break
			}
			j++
		}
		if j >= len(s) {
			return "", "", false
		}
		v, ok := parseYAMLScalar(s[:j+1])
		str, isStr := v.(string)
		if !ok || !isStr {
			return "", "", false
		}
		name = str
		i = j + 1
	default:
		for i < len(s) {
			if s[i] == ':' && (i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\t') {
				break
			}
			if s[i] == '#' && i > 0 && (s[i-1] == ' ' || s[i-1] == '\t') {
				return "", "", false
			}
			i++
		}
		if i >= len(s) {
			return "", "", false
		}
		name = strings.TrimRight(s[:i], " \t")
		if name == "" || strings.ContainsAny(name[:1], "[{&*!|>%@`-?") {
			return "", "", false
		}
	}
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	if i >= len(s) || s[i] != ':' {
		return "", "", false
	}
	return name, stripYAMLComment(strings.TrimSpace(s[i+1:])), true
}

// parseYAMLScalar converts a one-line scalar to a JSON-comparable value.
func parseYAMLScalar(v string) (any, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	switch v[0] {
	case '"':
		end := -1
		for j := 1; j < len(v); j++ {
			if v[j] == '\\' {
				j++
				continue
			}
			if v[j] == '"' {
				end = j
				break
			}
		}
		if end < 0 || strings.TrimSpace(stripYAMLComment(strings.TrimSpace(v[end+1:]))) != "" {
			return nil, false
		}
		s, ok := unescapeYAMLDouble(v[1:end])
		return s, ok
	case '\'':
		var b strings.Builder
		j := 1
		for j < len(v) {
			if v[j] == '\'' {
				if j+1 < len(v) && v[j+1] == '\'' {
					b.WriteByte('\'')
					j += 2
					continue
				}
				break
			}
			b.WriteByte(v[j])
			j++
		}
		if j >= len(v) || stripYAMLComment(strings.TrimSpace(v[j+1:])) != "" {
			return nil, false
		}
		return b.String(), true
	case '|', '>', '[', '{', '&', '*', '!', '%', '@', '`', '-':
		if v[0] != '-' || len(v) == 1 || v[1] == ' ' {
			return nil, false
		}
	}
	v = stripYAMLComment(v)
	switch v {
	case "true", "True", "TRUE":
		return true, true
	case "false", "False", "FALSE":
		return false, true
	case "null", "Null", "NULL", "~":
		return nil, true
	}
	if _, err := strconv.ParseInt(v, 10, 64); err == nil && (v == "0" || !strings.HasPrefix(strings.TrimLeft(v, "+-"), "0")) {
		return json.Number(strings.TrimPrefix(v, "+")), true
	}
	return v, true
}

func unescapeYAMLDouble(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(s) {
			return "", false
		}
		switch s[i] {
		case '"', '\\', '/':
			b.WriteByte(s[i])
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case '0':
			b.WriteByte(0)
		case ' ':
			b.WriteByte(' ')
		case 'x', 'u', 'U':
			n := map[byte]int{'x': 2, 'u': 4, 'U': 8}[s[i]]
			if i+n >= len(s) {
				return "", false
			}
			cp, err := strconv.ParseUint(s[i+1:i+1+n], 16, 32)
			if err != nil {
				return "", false
			}
			b.WriteRune(rune(cp))
			i += n
		default:
			return "", false
		}
	}
	return b.String(), true
}

// itemHash is the EntryHash of the item as a JSON object.
func (it *yItem) hash() string {
	if it.opaque {
		return ""
	}
	raw, err := json.Marshal(it.obj)
	if err != nil {
		return ""
	}
	h, err := EntryHash(raw)
	if err != nil {
		return ""
	}
	return h
}
