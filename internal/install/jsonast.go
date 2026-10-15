package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// A small order- and format-preserving JSON model. Unmodified nodes are
// re-emitted byte for byte from the source; only containers that were
// changed are re-rendered (in the indentation style of the document), so an
// install followed by an uninstall reproduces the original file exactly.

type jkind uint8

const (
	jScalar jkind = iota
	jObject
	jArray
)

const jMaxDepth = 256

type jmember struct {
	key    string
	keyRaw []byte
	val    *jnode
}

type jnode struct {
	kind    jkind
	raw     []byte // source text; nil for nodes created by the installer
	members []jmember
	items   []*jnode
	dirty   bool

	// style of a parsed container
	inline     bool
	colonSpace bool
	commaSpace bool
}

type jdoc struct {
	lead  []byte // bytes before the root value (BOM, whitespace)
	trail []byte // bytes after the root value
	root  *jnode
	unit  string // one indentation level
	eol   string
}

var errJSONSyntax = errors.New("invalid JSON")

type jparser struct {
	src   []byte
	pos   int
	depth int
}

// parseJSONDoc parses a strict JSON document. Blank input yields an empty
// object (the caller decides whether that counts as "file absent").
func parseJSONDoc(src []byte) (*jdoc, error) {
	d := &jdoc{unit: detectIndent(src), eol: detectEOL(src)}
	if len(bytes.TrimSpace(bytes.TrimPrefix(src, []byte("\xef\xbb\xbf")))) == 0 {
		d.root = newObject()
		d.trail = []byte(d.eol)
		return d, nil
	}
	p := &jparser{src: src}
	if bytes.HasPrefix(src, []byte("\xef\xbb\xbf")) {
		p.pos = 3
	}
	p.skipWS()
	d.lead = src[:p.pos]
	root, err := p.value()
	if err != nil {
		return nil, err
	}
	end := p.pos
	p.skipWS()
	if p.pos != len(src) {
		return nil, p.errf("unexpected data after the top-level value")
	}
	d.trail = src[end:]
	d.root = root
	return d, nil
}

func (p *jparser) errf(format string, a ...any) error {
	line, col := 1, 1
	for i := 0; i < p.pos && i < len(p.src); i++ {
		if p.src[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return fmt.Errorf("%w at line %d column %d: %s", errJSONSyntax, line, col, fmt.Sprintf(format, a...))
}

func (p *jparser) skipWS() {
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *jparser) value() (*jnode, error) {
	if p.pos >= len(p.src) {
		return nil, p.errf("unexpected end of input")
	}
	switch c := p.src[p.pos]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		start := p.pos
		if err := p.str(); err != nil {
			return nil, err
		}
		return &jnode{kind: jScalar, raw: p.src[start:p.pos]}, nil
	case c == '-' || (c >= '0' && c <= '9'):
		start := p.pos
		if err := p.number(); err != nil {
			return nil, err
		}
		return &jnode{kind: jScalar, raw: p.src[start:p.pos]}, nil
	case c == 't' || c == 'f' || c == 'n':
		for _, lit := range []string{"true", "false", "null"} {
			if bytes.HasPrefix(p.src[p.pos:], []byte(lit)) {
				start := p.pos
				p.pos += len(lit)
				return &jnode{kind: jScalar, raw: p.src[start:p.pos]}, nil
			}
		}
		return nil, p.errf("invalid literal")
	default:
		return nil, p.errf("unexpected character %q", c)
	}
}

func (p *jparser) str() error {
	p.pos++ // opening quote
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '"':
			p.pos++
			return nil
		case c < 0x20:
			return p.errf("control character in string")
		case c == '\\':
			p.pos++
			if p.pos >= len(p.src) {
				return p.errf("unterminated escape")
			}
			switch p.src[p.pos] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				p.pos++
			case 'u':
				if p.pos+4 >= len(p.src) {
					return p.errf("short \\u escape")
				}
				for i := 1; i <= 4; i++ {
					if !isHex(p.src[p.pos+i]) {
						return p.errf("bad \\u escape")
					}
				}
				p.pos += 5
			default:
				return p.errf("invalid escape")
			}
		default:
			p.pos++
		}
	}
	return p.errf("unterminated string")
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func (p *jparser) number() error {
	digits := func() int {
		n := 0
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
			n++
		}
		return n
	}
	if p.src[p.pos] == '-' {
		p.pos++
	}
	if p.pos >= len(p.src) {
		return p.errf("bad number")
	}
	if p.src[p.pos] == '0' {
		p.pos++
	} else if digits() == 0 {
		return p.errf("bad number")
	}
	if p.pos < len(p.src) && p.src[p.pos] == '.' {
		p.pos++
		if digits() == 0 {
			return p.errf("bad number")
		}
	}
	if p.pos < len(p.src) && (p.src[p.pos] == 'e' || p.src[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.src) && (p.src[p.pos] == '+' || p.src[p.pos] == '-') {
			p.pos++
		}
		if digits() == 0 {
			return p.errf("bad number")
		}
	}
	return nil
}

func (p *jparser) enter() error {
	p.depth++
	if p.depth > jMaxDepth {
		return p.errf("nesting too deep")
	}
	return nil
}

func (p *jparser) object() (*jnode, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	start := p.pos
	n := &jnode{kind: jObject}
	p.pos++
	p.skipWS()
	if p.pos < len(p.src) && p.src[p.pos] == '}' {
		p.pos++
		n.raw = p.src[start:p.pos]
		n.finishStyle()
		return n, nil
	}
	for {
		p.skipWS()
		if p.pos >= len(p.src) || p.src[p.pos] != '"' {
			return nil, p.errf("expected object key")
		}
		ks := p.pos
		if err := p.str(); err != nil {
			return nil, err
		}
		keyRaw := p.src[ks:p.pos]
		var key string
		if err := json.Unmarshal(keyRaw, &key); err != nil {
			return nil, p.errf("bad object key")
		}
		p.skipWS()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			return nil, p.errf("expected ':'")
		}
		p.pos++
		p.skipWS()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		n.members = append(n.members, jmember{key: key, keyRaw: keyRaw, val: v})
		p.skipWS()
		if p.pos >= len(p.src) {
			return nil, p.errf("unterminated object")
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			n.raw = p.src[start:p.pos]
			n.finishStyle()
			return n, nil
		default:
			return nil, p.errf("expected ',' or '}'")
		}
	}
}

func (p *jparser) array() (*jnode, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	start := p.pos
	n := &jnode{kind: jArray}
	p.pos++
	p.skipWS()
	if p.pos < len(p.src) && p.src[p.pos] == ']' {
		p.pos++
		n.raw = p.src[start:p.pos]
		n.finishStyle()
		return n, nil
	}
	for {
		p.skipWS()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		n.items = append(n.items, v)
		p.skipWS()
		if p.pos >= len(p.src) {
			return nil, p.errf("unterminated array")
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			n.raw = p.src[start:p.pos]
			n.finishStyle()
			return n, nil
		default:
			return nil, p.errf("expected ',' or ']'")
		}
	}
}

func (n *jnode) finishStyle() {
	if len(n.members) == 0 && len(n.items) == 0 {
		n.colonSpace, n.commaSpace = true, true // an empty container grows multi-line
		return
	}
	n.inline = !bytes.ContainsAny(n.raw, "\n\r")
	n.colonSpace = bytes.Contains(n.raw, []byte(`": `))
	n.commaSpace = bytes.Contains(n.raw, []byte(", "))
	if len(n.members)+len(n.items) == 1 {
		// no comma to learn from: guess from the other spacing
		n.commaSpace = n.colonSpace || bytes.HasPrefix(n.raw[1:], []byte(" "))
	}
}

func detectEOL(src []byte) string {
	if i := bytes.IndexByte(src, '\n'); i > 0 && src[i-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

// detectIndent returns the whitespace of the first indented line.
func detectIndent(src []byte) string {
	for len(src) > 0 {
		var line []byte
		if i := bytes.IndexByte(src, '\n'); i >= 0 {
			line, src = src[:i], src[i+1:]
		} else {
			line, src = src, nil
		}
		j := 0
		for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
			j++
		}
		if j > 0 && j < len(line) && line[j] != '\r' {
			return string(line[:j])
		}
	}
	return "  "
}

// ---- construction & mutation -------------------------------------------

func newObject() *jnode { return &jnode{kind: jObject} }
func newArray() *jnode  { return &jnode{kind: jArray} }

func newString(s string) *jnode {
	return &jnode{kind: jScalar, raw: marshalNoEscape(s)}
}

func newInt(i int) *jnode {
	return &jnode{kind: jScalar, raw: []byte(fmt.Sprintf("%d", i))}
}

func marshalNoEscape(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n")
}

func (n *jnode) get(key string) *jnode {
	for i := len(n.members) - 1; i >= 0; i-- {
		if n.members[i].key == key {
			return n.members[i].val
		}
	}
	return nil
}

func (n *jnode) count(key string) int {
	c := 0
	for _, m := range n.members {
		if m.key == key {
			c++
		}
	}
	return c
}

func (n *jnode) set(key string, v *jnode) {
	n.dirty = true
	for i := len(n.members) - 1; i >= 0; i-- {
		if n.members[i].key == key {
			n.members[i].val = v
			return
		}
	}
	n.members = append(n.members, jmember{key: key, keyRaw: marshalNoEscape(key), val: v})
}

// setFront inserts key at the start of the object when absent.
func (n *jnode) setFront(key string, v *jnode) {
	n.dirty = true
	n.members = append([]jmember{{key: key, keyRaw: marshalNoEscape(key), val: v}}, n.members...)
}

func (n *jnode) del(key string) {
	out := n.members[:0:0]
	for _, m := range n.members {
		if m.key != key {
			out = append(out, m)
		}
	}
	if len(out) != len(n.members) {
		n.dirty = true
	}
	n.members = out
}

func (n *jnode) appendItem(v *jnode) {
	n.dirty = true
	n.items = append(n.items, v)
}

func (n *jnode) removeItem(i int) {
	n.dirty = true
	n.items = append(n.items[:i:i], n.items[i+1:]...)
}

func (n *jnode) replaceItem(i int, v *jnode) {
	n.dirty = true
	n.items[i] = v
}

func (n *jnode) isEmptyContainer() bool {
	switch n.kind {
	case jObject:
		return len(n.members) == 0
	case jArray:
		return len(n.items) == 0
	}
	return false
}

// obj helpers for building nodes
func objOf(kv ...any) *jnode {
	o := newObject()
	for i := 0; i+1 < len(kv); i += 2 {
		o.members = append(o.members, jmember{key: kv[i].(string), keyRaw: marshalNoEscape(kv[i].(string)), val: kv[i+1].(*jnode)})
	}
	return o
}

func arrOf(items ...*jnode) *jnode {
	a := newArray()
	a.items = items
	return a
}

func stringArray(ss []string) *jnode {
	a := newArray()
	for _, s := range ss {
		a.items = append(a.items, newString(s))
	}
	return a
}

// ---- inspection ---------------------------------------------------------

// str returns the decoded string of a scalar string node.
func (n *jnode) str() (string, bool) {
	if n == nil || n.kind != jScalar || len(n.raw) == 0 || n.raw[0] != '"' {
		return "", false
	}
	var s string
	if json.Unmarshal(n.raw, &s) != nil {
		return "", false
	}
	return s, true
}

func (n *jnode) isInt(want int) bool {
	return n != nil && n.kind == jScalar && string(n.raw) == fmt.Sprintf("%d", want)
}

// strList returns the elements of an array of strings.
func (n *jnode) strList() ([]string, bool) {
	if n == nil || n.kind != jArray {
		return nil, false
	}
	out := make([]string, 0, len(n.items))
	for _, it := range n.items {
		s, ok := it.str()
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// hash is EntryHash of the node's current serialization.
func (n *jnode) hash(f jfmt) string {
	var b bytes.Buffer
	f.write(&b, n, 0, jstyle{colonSpace: true, commaSpace: true})
	h, err := EntryHash(b.Bytes())
	if err != nil {
		return ""
	}
	return h
}

// ---- serialization --------------------------------------------------------

type jfmt struct {
	unit string
	eol  string
}

// settle propagates dirtiness from descendants to ancestors.
func (n *jnode) settle() bool {
	d := n.dirty || n.raw == nil
	switch n.kind {
	case jObject:
		for _, m := range n.members {
			if m.val.settle() {
				d = true
			}
		}
	case jArray:
		for _, it := range n.items {
			if it.settle() {
				d = true
			}
		}
	}
	n.dirty = d
	return d
}

func (d *jdoc) bytes() []byte {
	d.root.settle()
	var b bytes.Buffer
	b.Write(d.lead)
	jfmt{unit: d.unit, eol: d.eol}.write(&b, d.root, 0, jstyle{colonSpace: true, commaSpace: true})
	b.Write(d.trail)
	return b.Bytes()
}

// jstyle is the layout of a container: inline (single line) or multi-line,
// and the spacing after ':' and ','.
type jstyle struct {
	inline     bool
	colonSpace bool
	commaSpace bool
}

// write renders n. Clean parsed nodes are copied verbatim; changed or new
// containers are laid out in their own parsed style, or in the parent's
// style (inherit) when they are new.
func (f jfmt) write(b *bytes.Buffer, n *jnode, depth int, inherit jstyle) {
	if n.raw != nil && !n.dirty {
		b.Write(n.raw)
		return
	}
	if n.kind == jScalar {
		b.Write(n.raw)
		return
	}
	st := inherit
	if n.raw != nil {
		st = jstyle{inline: n.inline, colonSpace: n.colonSpace, commaSpace: n.commaSpace}
	}
	open, closeCh := byte('{'), byte('}')
	count := len(n.members)
	if n.kind == jArray {
		open, closeCh = '[', ']'
		count = len(n.items)
	}
	b.WriteByte(open)
	if count == 0 {
		b.WriteByte(closeCh)
		return
	}
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		if st.inline {
			if i > 0 && st.commaSpace {
				b.WriteByte(' ')
			}
		} else {
			b.WriteString(f.eol)
			b.WriteString(strings.Repeat(f.unit, depth+1))
		}
		if n.kind == jObject {
			m := n.members[i]
			b.Write(m.keyRaw)
			b.WriteByte(':')
			if st.colonSpace || !st.inline {
				b.WriteByte(' ')
			}
			f.write(b, m.val, depth+1, st)
		} else {
			f.write(b, n.items[i], depth+1, st)
		}
	}
	if !st.inline {
		b.WriteString(f.eol)
		b.WriteString(strings.Repeat(f.unit, depth))
	}
	b.WriteByte(closeCh)
}
