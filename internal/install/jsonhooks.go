package install

import (
	"fmt"
	"runtime"

	"github.com/libingtong/ballast/internal/model"
)

const (
	codexHookTimeout  = 30
	hermesHookTimeout = 30
	cursorLoopLimit   = 10
)

// jsonHooksTarget edits the JSON hook files of Claude Code (settings.json),
// Codex (hooks.json) and Cursor (hooks.json).
type jsonHooksTarget struct {
	client model.ClientID
	path   string
	goos   string
}

func (t *jsonHooksTarget) Path() string   { return t.path }
func (t *jsonHooksTarget) Format() Format { return FormatJSON }

// nested reports whether entries live in {"hooks":[...]} groups.
func (t *jsonHooksTarget) nested() bool { return t.client != model.ClientCursor }

func (t *jsonHooksTarget) parseErr(err error) error {
	return fmt.Errorf("%w: %s: %v", model.ErrSettingsParse, t.path, err)
}

func (t *jsonHooksTarget) parse(cur []byte) (*jdoc, error) {
	doc, err := parseJSONDoc(cur)
	if err != nil {
		return nil, t.parseErr(err)
	}
	if doc.root.kind != jObject {
		return nil, t.parseErr(fmt.Errorf("top-level value is not an object"))
	}
	return doc, nil
}

// handler builds the entry object that is hashed and recorded.
func (t *jsonHooksTarget) handler(binary string, ne model.NativeEvent, goos string) (obj *jnode, command string, args []string) {
	argv := HookArgv(binary, t.client, ne.Native)
	switch t.client {
	case model.ClientClaude:
		args = argv[1:]
		return objOf("type", newString("command"), "command", newString(binary), "args", stringArray(args)), binary, args
	case model.ClientCodex:
		command = QuoteCommand(argv, goos)
		return objOf("type", newString("command"), "command", newString(command), "timeout", newInt(codexHookTimeout)), command, nil
	default: // cursor
		command = QuoteCommand(argv, goos)
		o := objOf("command", newString(command))
		if ne.Native == "stop" || ne.Native == "subagentStop" {
			o.set("loop_limit", newInt(cursorLoopLimit))
		}
		return o, command, nil
	}
}

func (t *jsonHooksTarget) group(ne model.NativeEvent, h *jnode) *jnode {
	g := newObject()
	if t.client == model.ClientClaude && ne.Event.IsToolEvent() {
		g.set("matcher", newString("*"))
	}
	g.set("hooks", arrOf(h))
	return g
}

// isBallast applies the command-prefix half of the ownership rule.
func (t *jsonHooksTarget) isBallast(h *jnode, binaries map[string]bool, goos string) bool {
	if h == nil || h.kind != jObject {
		return false
	}
	cmd, ok := h.get("command").str()
	if !ok {
		return false
	}
	if t.client == model.ClientClaude {
		if !binaries[cmd] {
			return false
		}
		args, ok := h.get("args").strList()
		return ok && len(args) >= 3 && args[0] == "hook" && args[1] == "--client" && args[2] == string(t.client)
	}
	return commandHasPrefix(cmd, binaries, t.client, goos)
}

// commandHasPrefix reports whether the single command string starts with
// "<binary> hook --client <id>" for one of the known binaries.
func commandHasPrefix(cmd string, binaries map[string]bool, c model.ClientID, goos string) bool {
	parts, err := SplitCommand(cmd, goos)
	return err == nil && len(parts) >= 4 && binaries[parts[0]] &&
		parts[1] == "hook" && parts[2] == "--client" && parts[3] == string(c)
}

// ownerBinaries collects every binary path an owned entry may carry.
func ownerBinaries(c model.ClientID, goos, binary string, m *model.InstallManifest, recs []model.ManagedEntry) map[string]bool {
	set := map[string]bool{}
	if binary != "" {
		set[binary] = true
	}
	if m != nil && m.Binary != "" {
		set[m.Binary] = true
	}
	for _, r := range recs {
		if c == model.ClientClaude {
			set[r.Command] = true
		} else if parts, err := SplitCommand(r.Command, goos); err == nil && len(parts) > 0 {
			set[parts[0]] = true
		}
	}
	return set
}

type located struct {
	list      *jnode // event array
	group     *jnode // nested only
	handlers  *jnode // array holding the handler
	groupIdx  int
	handleIdx int
	node      *jnode
}

// eachHandler calls fn for every handler object of an event list.
func (t *jsonHooksTarget) eachHandler(list *jnode, fn func(l located) bool) {
	if list == nil || list.kind != jArray {
		return
	}
	for gi, g := range list.items {
		if !t.nested() {
			if g.kind == jObject && !fn(located{list: list, handlers: list, groupIdx: gi, handleIdx: gi, node: g}) {
				return
			}
			continue
		}
		if g.kind != jObject {
			continue
		}
		hs := g.get("hooks")
		if hs == nil || hs.kind != jArray {
			continue
		}
		for hi, h := range hs.items {
			if h.kind == jObject && !fn(located{list: list, group: g, handlers: hs, groupIdx: gi, handleIdx: hi, node: h}) {
				return
			}
		}
	}
}

func (t *jsonHooksTarget) key(native string, l located) string {
	if t.nested() {
		return fmt.Sprintf("hooks.%s[%d].hooks[%d]", native, l.groupIdx, l.handleIdx)
	}
	return fmt.Sprintf("hooks.%s[%d]", native, l.groupIdx)
}

// eventList returns the array of one event (nil when absent).
func (t *jsonHooksTarget) eventList(hooks *jnode, native string) (*jnode, error) {
	if hooks == nil {
		return nil, nil
	}
	if hooks.count(native) > 1 {
		return nil, t.parseErr(fmt.Errorf("duplicate key hooks.%s", native))
	}
	l := hooks.get(native)
	if l == nil {
		return nil, nil
	}
	if l.kind != jArray {
		return nil, t.parseErr(fmt.Errorf("hooks.%s is not an array", native))
	}
	return l, nil
}

func (t *jsonHooksTarget) hooksObject(root *jnode) (*jnode, error) {
	if root.count("hooks") > 1 {
		return nil, t.parseErr(fmt.Errorf("duplicate key hooks"))
	}
	h := root.get("hooks")
	if h == nil {
		return nil, nil
	}
	if h.kind != jObject {
		return nil, t.parseErr(fmt.Errorf("hooks is not an object"))
	}
	return h, nil
}

// Plan implements Target.
func (t *jsonHooksTarget) Plan(cur []byte, spec EntrySpec, m *model.InstallManifest) (*PlanResult, error) {
	m = nonNilManifest(m)
	goos := firstNonEmpty(spec.GOOS, t.goos, runtime.GOOS)
	doc, err := t.parse(cur)
	if err != nil {
		return nil, err
	}
	f := jfmt{unit: doc.unit, eol: doc.eol}
	root := doc.root
	events := spec.Events
	if len(events) == 0 {
		events = defaultEvents(t.client)
	}
	res := &PlanResult{Path: t.path, Format: FormatJSON, Before: cur, Created: cur == nil}
	if prior := fileRec(m, t.path); prior != nil && prior.Created {
		res.Created = true
	}

	recs := entriesFor(m, t.client, t.path)
	byHash := map[string]model.ManagedEntry{}
	for _, r := range recs {
		byHash[r.Hash] = r
	}
	binaries := ownerBinaries(t.client, goos, spec.Binary, m, recs)

	hooks, err := t.hooksObject(root)
	if err != nil {
		return nil, err
	}
	if t.client == model.ClientCursor && root.get("version") == nil {
		root.setFront("version", newInt(1))
	}
	ensureHooks := func() {
		if hooks == nil {
			hooks = newObject()
			root.set("hooks", hooks)
		}
	}

	processed := map[string]bool{}
	for _, ne := range events {
		processed[ne.Native] = true
		want, command, args := t.handler(spec.Binary, ne, goos)
		wantHash := want.hash(f)
		list, err := t.eventList(hooks, ne.Native)
		if err != nil {
			return nil, err
		}

		var owned *located
		foreign := 0
		t.eachHandler(list, func(l located) bool {
			if !t.isBallast(l.node, binaries, goos) {
				return true
			}
			h := l.node.hash(f)
			if r, ok := byHash[h]; ok && r.Native == ne.Native && owned == nil {
				ll := l
				owned = &ll
			} else {
				foreign++
			}
			return true
		})

		entry := model.ManagedEntry{
			Client: t.client, File: t.path, Native: ne.Native,
			Hash: wantHash, Command: command, Args: args,
		}
		switch {
		case owned != nil:
			if owned.node.hash(f) != wantHash {
				owned.handlers.replaceItem(owned.handleIdx, want)
			}
			entry.Key = t.key(ne.Native, *owned)
			res.Entries = append(res.Entries, entry)
		case foreign > 0:
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"%s: an existing %s entry for %q was modified by the user; left in place and no duplicate added",
				t.path, t.client, ne.Native))
			for _, r := range recs {
				if r.Native == ne.Native {
					res.Entries = append(res.Entries, r)
				}
			}
		default:
			ensureHooks()
			if list == nil {
				list = newArray()
				hooks.set(ne.Native, list)
			}
			var loc located
			if t.nested() {
				g := t.group(ne, want)
				list.appendItem(g)
				loc = located{groupIdx: len(list.items) - 1, handleIdx: 0}
			} else {
				list.appendItem(want)
				loc = located{groupIdx: len(list.items) - 1}
			}
			entry.Key = t.key(ne.Native, loc)
			res.Entries = append(res.Entries, entry)
		}
	}
	// Records of natives outside this install's event set stay recorded.
	for _, r := range recs {
		if !processed[r.Native] {
			res.Entries = append(res.Entries, r)
		}
	}

	res.After = doc.bytes()
	res.Files = []model.ManagedFile{{Client: t.client, Path: t.path, Hash: sha256Hex(res.After), Created: res.Created}}
	return res, nil
}

// Remove implements Target.
func (t *jsonHooksTarget) Remove(cur []byte, m *model.InstallManifest) (*PlanResult, error) {
	m = nonNilManifest(m)
	res := &PlanResult{Path: t.path, Format: FormatJSON, Before: cur}
	if cur == nil {
		return res, nil
	}
	goos := firstNonEmpty(t.goos, runtime.GOOS)
	doc, err := t.parse(cur)
	if err != nil {
		return nil, err
	}
	f := jfmt{unit: doc.unit, eol: doc.eol}
	root := doc.root
	recs := entriesFor(m, t.client, t.path)
	prior := fileRec(m, t.path)
	binaries := ownerBinaries(t.client, goos, "", m, recs)
	hooks, err := t.hooksObject(root)
	if err != nil {
		return nil, err
	}

	for _, r := range recs {
		list, err := t.eventList(hooks, r.Native)
		if err != nil {
			return nil, err
		}
		var hit *located
		foreign := false
		t.eachHandler(list, func(l located) bool {
			if l.node.hash(f) == r.Hash {
				ll := l
				hit = &ll
				return false
			}
			if t.isBallast(l.node, binaries, goos) {
				foreign = true
			}
			return true
		})
		switch {
		case hit != nil:
			t.removeHandler(hooks, r.Native, *hit)
		case foreign:
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"%s: %s entry for %q was modified by the user; kept", t.path, t.client, r.Native))
			res.Entries = append(res.Entries, r)
		}
	}
	if hooks != nil && hooks.isEmptyContainer() {
		root.del("hooks")
	}
	if len(res.Entries) > 0 && prior != nil {
		res.Files = []model.ManagedFile{*prior}
	}
	if prior != nil && prior.Created && t.rootIsEmpty(root) {
		res.After = nil
		return res, nil
	}
	res.After = doc.bytes()
	return res, nil
}

// removeHandler deletes a handler and any group/event/array left empty.
func (t *jsonHooksTarget) removeHandler(hooks *jnode, native string, l located) {
	l.handlers.removeItem(l.handleIdx)
	if t.nested() && l.handlers.isEmptyContainer() && onlyKeys(l.group, "matcher", "hooks") {
		l.list.removeItem(l.groupIdx)
	}
	if l.list.isEmptyContainer() {
		hooks.del(native)
	}
}

func onlyKeys(o *jnode, keys ...string) bool {
	for _, m := range o.members {
		ok := false
		for _, k := range keys {
			if m.key == k {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// rootIsEmpty: nothing but what install itself created remains.
func (t *jsonHooksTarget) rootIsEmpty(root *jnode) bool {
	switch len(root.members) {
	case 0:
		return true
	case 1:
		return t.client == model.ClientCursor && root.members[0].key == "version" && root.members[0].val.isInt(1)
	}
	return false
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
