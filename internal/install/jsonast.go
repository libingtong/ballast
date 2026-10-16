package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// A small order- and format-preserving JSON model. Every byte of the source
// belongs to exactly one place in the tree: the root's lead/trail, a node's
// raw text, or the gap (whitespace before/after) a child keeps inside its
// parent, plus the colon text of an object member and the inner whitespace
// of an empty container. Unmodified nodes are re-emitted from raw; a changed
// container is re-assembled from those pieces, so only the inserted or
// removed member differs. New members take their separator from the
// adjacent member (blank lines are not copied), and removing the member an
// install inserted gives the original bytes back.

type jkind uint8

const (
	jScalar jkind = iota
	jObject
	jArray
)

const jMaxDepth = 256

// jgap is the whitespace around a child inside its container: pre sits
// between the previous separator (or the opening bracket) and the child
// (for an object member: before the key); post between the child and the
// next comma (or the closing bracket).
type jgap struct{ pre, post string }

type jmember struct {
	key    string
	keyRaw []byte
	colon  string // source text between the key and the value, ':' included; "" for a new member
	val    *jnode
}

type jnode struct {
	kind    jkind
	raw     []byte // source text; nil for nodes created by the installer
	members []jmember
	items   []*jnode
	dirty   bool

	gap *jgap // nil for a node without a source position (laid out when rendered)

	// parsed containers
	inner  string     // whitespace between the brackets of an empty container
	indent string     // indentation of the line the container starts on
	multi  bool       // an empty container grows multi-line (its parent is)
	st     *jdocStyle // document-wide defaults
}

// jdocStyle holds the formatting defaults of one document.
type jdocStyle struct {
	unit  string // one indentation level
	eol   string
	colon string // text between key and value of the first member
	sep   string // whitespace after the first inline comma
}

type jdoc struct {
	lead  []byte // bytes before the root value (BOM, whitespace)
	trail []byte // bytes after the root value
	root  *jnode
	st    *jdocStyle
}

var errJSONSyntax = errors.New("invalid JSON")

type jparser struct {
	src   []byte
	pos   int
	depth int
	st    *jdocStyle

	colonSet, sepSet bool
	lnPos, lnStart   int // incremental line tracking for lineIndent
}

// parseJSONDoc parses a strict JSON document. Blank input yields an empty
// object (the caller decides whether that counts as "file absent").
func parseJSONDoc(src []byte) (*jdoc, error) {
	st := &jdocStyle{unit: detectIndent(src), eol: detectEOL(src)}
	d := &jdoc{st: st}
	body := bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(body)) == 0 {
		st.colon, st.sep = ": ", " "
		d.root = newObject()
		d.lead = src[:len(src)-len(body)]
		d.trail = []byte(st.eol)
		return d, nil
	}
	p := &jparser{src: src, st: st}
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
	if root.kind != jScalar && root.size() == 0 {
		root.multi = true // an empty document root grows multi-line
	}
	if !p.colonSet {
		st.colon = ": "
	}
	if !p.sepSet {
		st.sep = ""
		if strings.HasSuffix(st.colon, " ") {
			st.sep = " "
		}
	}
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

// ws consumes whitespace and returns it.
func (p *jparser) ws() string {
	a := p.pos
	p.skipWS()
	return string(p.src[a:p.pos])
}

// lineIndent returns the indentation of the line that contains pos. Calls
// must come in increasing pos order (the parser only moves forward).
func (p *jparser) lineIndent(pos int) string {
	for ; p.lnPos < pos; p.lnPos++ {
		if p.src[p.lnPos] == '\n' {
			p.lnStart = p.lnPos + 1
		}
	}
	j := p.lnStart
	for j < pos && (p.src[j] == ' ' || p.src[j] == '\t') {
		j++
	}
	return string(p.src[p.lnStart:j])
}

// noteSep remembers the first inline whitespace after a comma.
func (p *jparser) noteSep(pre string) {
	if !p.sepSet && !strings.Contains(pre, "\n") {
		p.st.sep, p.sepSet = pre, true
	}
}

func (p *jparser) object() (*jnode, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	start := p.pos
	n := &jnode{kind: jObject, st: p.st, indent: p.lineIndent(start)}
	p.pos++
	pre := p.ws()
	if p.pos < len(p.src) && p.src[p.pos] == '}' {
		n.inner = pre
		p.pos++
		n.raw = p.src[start:p.pos]
		return n, nil
	}
	for {
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
		colon := string(p.src[ks+len(keyRaw) : p.pos])
		if !p.colonSet {
			p.st.colon, p.colonSet = colon, true
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		post := p.ws()
		v.gap = &jgap{pre: pre, post: post}
		n.members = append(n.members, jmember{key: key, keyRaw: keyRaw, colon: colon, val: v})
		if p.pos >= len(p.src) {
			return nil, p.errf("unterminated object")
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
			pre = p.ws()
			p.noteSep(pre)
		case '}':
			p.pos++
			n.raw = p.src[start:p.pos]
			n.finishChildren()
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
	n := &jnode{kind: jArray, st: p.st, indent: p.lineIndent(start)}
	p.pos++
	pre := p.ws()
	if p.pos < len(p.src) && p.src[p.pos] == ']' {
		n.inner = pre
		p.pos++
		n.raw = p.src[start:p.pos]
		return n, nil
	}
	for {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		post := p.ws()
		v.gap = &jgap{pre: pre, post: post}
		n.items = append(n.items, v)
		if p.pos >= len(p.src) {
			return nil, p.errf("unterminated array")
		}
		switch p.src[p.pos] {
		case ',':
			p.pos++
			pre = p.ws()
			p.noteSep(pre)
		case ']':
			p.pos++
			n.raw = p.src[start:p.pos]
			n.finishChildren()
			return n, nil
		default:
			return nil, p.errf("expected ',' or ']'")
		}
	}
}

// finishChildren tells empty child containers whether their parent lays
// its members out on separate lines.
func (n *jnode) finishChildren() {
	multi := false
	for i, c := 0, n.size(); i < c; i++ {
		g := n.child(i).gap
		if strings.Contains(g.pre, "\n") || strings.Contains(g.post, "\n") {
			multi = true
		}
	}
	for i, c := 0, n.size(); i < c; i++ {
		if ch := n.child(i); ch.kind != jScalar && ch.size() == 0 {
			ch.multi = multi
		}
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

// size is the number of members or items.
func (n *jnode) size() int {
	if n.kind == jObject {
		return len(n.members)
	}
	return len(n.items)
}

func (n *jnode) child(i int) *jnode {
	if n.kind == jObject {
		return n.members[i].val
	}
	return n.items[i]
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

// normSep reduces the whitespace before a sibling to one separator: the
// last line break and what follows it, so blank lines are not copied.
func normSep(pre string) string {
	i := strings.LastIndexByte(pre, '\n')
	if i < 0 {
		return pre
	}
	if i > 0 && pre[i-1] == '\r' {
		i--
	}
	return pre[i:]
}

// sepFor is the separator a new sibling of a member whose own pre is
// refPre gets.
func (n *jnode) sepFor(refPre string) string {
	s := normSep(refPre)
	if s == "" && n.kind == jObject && len(n.members) == 1 && strings.HasSuffix(n.members[0].colon, " ") {
		return " " // {"a": 1} grows to {"a": 1, "b": 2}
	}
	return s
}

// placeNew gives v, about to be inserted at the front or the end of the
// parsed container n, the whitespace around it. The neighbour hands over
// the gap side that faces the insertion point, so removing v later puts
// every byte back. A container without a source position lays its members
// out when it is rendered.
func (n *jnode) placeNew(v *jnode, front bool) {
	if n.raw == nil || n.st == nil || v.gap != nil {
		return
	}
	cnt := n.size()
	if cnt == 0 {
		if !strings.Contains(n.inner, "\n") && !n.multi {
			v.gap = &jgap{}
			return
		}
		eol, closeIndent := n.st.eol, n.indent
		if i := strings.IndexByte(n.inner, '\n'); i >= 0 {
			eol = "\n"
			if i > 0 && n.inner[i-1] == '\r' {
				eol = "\r\n"
			}
		}
		if i := strings.LastIndexByte(n.inner, '\n'); i >= 0 {
			closeIndent = n.inner[i+1:]
		}
		v.gap = &jgap{pre: eol + closeIndent + n.st.unit, post: eol + closeIndent}
		return
	}
	if front {
		ref := n.child(0)
		if ref.gap == nil {
			return
		}
		v.gap = &jgap{pre: ref.gap.pre}
		ref.gap = &jgap{pre: n.sepFor(ref.gap.pre), post: ref.gap.post}
		return
	}
	ref := n.child(cnt - 1)
	if ref.gap == nil {
		return
	}
	v.gap = &jgap{pre: n.sepFor(ref.gap.pre), post: ref.gap.post}
	ref.gap = &jgap{pre: ref.gap.pre}
}

// newColon is the colon text a new member of n gets ("" ⇒ the renderer's).
func (n *jnode) newColon() string {
	if n.raw == nil || n.st == nil {
		return ""
	}
	if l := len(n.members); l > 0 && n.members[l-1].colon != "" {
		return n.members[l-1].colon
	}
	return n.st.colon
}

func (n *jnode) set(key string, v *jnode) {
	n.dirty = true
	for i := len(n.members) - 1; i >= 0; i-- {
		if n.members[i].key == key {
			if v.gap == nil {
				v.gap = n.members[i].val.gap
			}
			n.members[i].val = v
			return
		}
	}
	colon := n.newColon()
	n.placeNew(v, false)
	n.members = append(n.members, jmember{key: key, keyRaw: marshalNoEscape(key), colon: colon, val: v})
}

// setFront inserts key at the start of the object when absent.
func (n *jnode) setFront(key string, v *jnode) {
	n.dirty = true
	colon := n.newColon()
	n.placeNew(v, true)
	n.members = append([]jmember{{key: key, keyRaw: marshalNoEscape(key), colon: colon, val: v}}, n.members...)
}

// removeAt drops child i. The removed child's whitespace is handed to its
// neighbour the way placeNew took it: the last child's post goes to the
// new last one, any other child's pre to its successor.
func (n *jnode) removeAt(i int) {
	n.dirty = true
	if cnt := n.size(); n.raw != nil && cnt > 1 {
		rem := n.child(i)
		if rem.gap != nil {
			if i == cnt-1 {
				if prev := n.child(i - 1); prev.gap != nil {
					prev.gap = &jgap{pre: prev.gap.pre, post: rem.gap.post}
				}
			} else if next := n.child(i + 1); next.gap != nil {
				next.gap = &jgap{pre: rem.gap.pre, post: next.gap.post}
			}
		}
	}
	if n.kind == jObject {
		n.members = append(n.members[:i:i], n.members[i+1:]...)
	} else {
		n.items = append(n.items[:i:i], n.items[i+1:]...)
	}
}

func (n *jnode) del(key string) {
	for i := len(n.members) - 1; i >= 0; i-- {
		if n.members[i].key == key {
			n.removeAt(i)
		}
	}
}

func (n *jnode) appendItem(v *jnode) {
	n.dirty = true
	n.placeNew(v, false)
	n.items = append(n.items, v)
}

func (n *jnode) removeItem(i int) { n.removeAt(i) }

func (n *jnode) replaceItem(i int, v *jnode) {
	n.dirty = true
	if v.gap == nil {
		v.gap = n.items[i].gap
	}
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

// emptyRaw returns the source text of an empty parsed container ("" when n
// is nil, new or not empty).
func emptyRaw(n *jnode) string {
	if n == nil || n.raw == nil || !n.isEmptyContainer() {
		return ""
	}
	return string(n.raw)
}

// restoreEmpty makes the (empty) container render as raw, the source text
// it had before install filled it.
func (n *jnode) restoreEmpty(raw string) {
	if len(raw) < 2 || !n.isEmptyContainer() {
		return
	}
	n.inner = raw[1 : len(raw)-1]
	n.dirty = true
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
func (n *jnode) hash() string {
	n.settle()
	var b bytes.Buffer
	n.compact(&b)
	h, err := EntryHash(b.Bytes())
	if err != nil {
		return ""
	}
	return h
}

// compact writes the node without any optional whitespace.
func (n *jnode) compact(b *bytes.Buffer) {
	if n.kind == jScalar || (n.raw != nil && !n.dirty) {
		b.Write(n.raw)
		return
	}
	open, closeCh := byte('{'), byte('}')
	if n.kind == jArray {
		open, closeCh = '[', ']'
	}
	b.WriteByte(open)
	for i, c := 0, n.size(); i < c; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		if n.kind == jObject {
			b.Write(n.members[i].keyRaw)
			b.WriteByte(':')
		}
		n.child(i).compact(b)
	}
	b.WriteByte(closeCh)
}

// ---- serialization --------------------------------------------------------

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
	d.st.render(&b, d.root, jctx{multi: d.root.raw == nil, colon: d.st.colon, sep: d.st.sep})
	b.Write(d.trail)
	return b.Bytes()
}

// jctx is the layout context of the node being rendered: whether its
// members go on separate lines, the indentation of the line it starts on,
// and the spacing used for members that have no source position.
type jctx struct {
	multi  bool
	indent string
	colon  string
	sep    string
}

// ctxFor is the context of a child that sits behind pre.
func ctxFor(pre string, c jctx) jctx {
	i := strings.LastIndexByte(pre, '\n')
	if i < 0 {
		return jctx{colon: c.colon, sep: c.sep}
	}
	return jctx{multi: true, indent: pre[i+1:], colon: c.colon, sep: c.sep}
}

// render writes n. Clean parsed nodes are copied verbatim; a changed parsed
// container is rebuilt from the whitespace it was parsed with; members
// without a source position are laid out in the style of the context.
func (st *jdocStyle) render(b *bytes.Buffer, n *jnode, c jctx) {
	if n.kind == jScalar || (n.raw != nil && !n.dirty) {
		b.Write(n.raw)
		return
	}
	open, closeCh := byte('{'), byte('}')
	if n.kind == jArray {
		open, closeCh = '[', ']'
	}
	b.WriteByte(open)
	count := n.size()
	if count == 0 {
		b.WriteString(n.inner)
		b.WriteByte(closeCh)
		return
	}
	// spacing for generated descendants follows the nearest parsed members
	if n.kind == jObject {
		if m := n.members[count-1]; m.colon != "" {
			c.colon = m.colon
		}
	}
	if n.raw != nil {
		if g := n.child(count - 1).gap; g != nil && !strings.Contains(g.pre, "\n") && (g.pre != "" || count > 1) {
			c.sep = g.pre
		}
	}
	for i := 0; i < count; i++ {
		ch := n.child(i)
		var pre, post string
		var cc jctx
		switch {
		case ch.gap != nil:
			pre, post = ch.gap.pre, ch.gap.post
			cc = ctxFor(pre, c)
		case c.multi:
			pre = st.eol + c.indent + st.unit
			if i == count-1 {
				post = st.eol + c.indent
			}
			cc = jctx{multi: true, indent: c.indent + st.unit, colon: c.colon, sep: c.sep}
		default:
			if i > 0 {
				pre = c.sep
			}
			cc = jctx{colon: c.colon, sep: c.sep}
		}
		b.WriteString(pre)
		if n.kind == jObject {
			m := n.members[i]
			b.Write(m.keyRaw)
			if m.colon != "" {
				b.WriteString(m.colon)
			} else {
				b.WriteString(c.colon)
			}
		}
		st.render(b, ch, cc)
		b.WriteString(post)
		if i < count-1 {
			b.WriteByte(',')
		}
	}
	b.WriteByte(closeCh)
}
