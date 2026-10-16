package psparse

import (
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// paramDef describes one cmdlet parameter.
type paramDef struct {
	name  string   // canonical lower-case name with leading '-'
	value bool     // false for switch parameters
	alias []string // exact aliases (lower-case, leading '-')
}

// cmdSpec lists the parameters of a cmdlet whose targets are extracted, and
// which parameters positional arguments bind to, in order.
type cmdSpec struct {
	params []paramDef
	pos    []string
}

var commonParams = []paramDef{
	{"-verbose", false, nil},
	{"-debug", false, nil},
	{"-erroraction", true, []string{"-ea"}},
	{"-warningaction", true, []string{"-wa"}},
	{"-informationaction", true, []string{"-infa"}},
	{"-errorvariable", true, []string{"-ev"}},
	{"-warningvariable", true, []string{"-wv"}},
	{"-informationvariable", true, []string{"-iv"}},
	{"-outvariable", true, []string{"-ov"}},
	{"-outbuffer", true, []string{"-ob"}},
	{"-pipelinevariable", true, []string{"-pv"}},
	{"-whatif", false, []string{"-wi"}},
	{"-confirm", false, []string{"-cf"}},
}

var (
	specStartSleep = cmdSpec{
		params: []paramDef{{"-seconds", true, nil}, {"-milliseconds", true, []string{"-ms"}}, {"-duration", true, nil}},
		pos:    []string{"-seconds"},
	}
	specOutFile = cmdSpec{
		params: []paramDef{
			{"-filepath", true, []string{"-path"}}, {"-literalpath", true, []string{"-pspath", "-lp"}},
			{"-encoding", true, nil}, {"-append", false, nil}, {"-force", false, nil},
			{"-noclobber", false, []string{"-nc"}}, {"-width", true, nil}, {"-nonewline", false, nil},
			{"-inputobject", true, nil}},
		pos: []string{"-filepath"},
	}
	specSetContent = cmdSpec{
		params: []paramDef{
			{"-path", true, nil}, {"-value", true, nil}, {"-literalpath", true, []string{"-pspath", "-lp"}},
			{"-filter", true, nil}, {"-include", true, nil}, {"-exclude", true, nil},
			{"-encoding", true, nil}, {"-force", false, nil}, {"-nonewline", false, nil},
			{"-passthru", false, nil}, {"-stream", true, nil}, {"-credential", true, nil},
			{"-asbytestream", false, nil}, {"-usetransaction", false, []string{"-usetx"}}},
		pos: []string{"-path", "-value"},
	}
	specClearContent = cmdSpec{
		params: []paramDef{
			{"-path", true, nil}, {"-literalpath", true, []string{"-pspath", "-lp"}},
			{"-filter", true, nil}, {"-include", true, nil}, {"-exclude", true, nil},
			{"-force", false, nil}, {"-credential", true, nil}, {"-stream", true, nil},
			{"-usetransaction", false, []string{"-usetx"}}},
		pos: []string{"-path"},
	}
	specCopyMove = cmdSpec{
		params: []paramDef{
			{"-path", true, nil}, {"-destination", true, nil}, {"-literalpath", true, []string{"-pspath", "-lp"}},
			{"-filter", true, nil}, {"-include", true, nil}, {"-exclude", true, nil},
			{"-container", false, nil}, {"-force", false, nil}, {"-passthru", false, nil},
			{"-recurse", false, nil}, {"-credential", true, nil}, {"-fromsession", true, nil},
			{"-tosession", true, nil}, {"-usetransaction", false, []string{"-usetx"}}},
		pos: []string{"-path", "-destination"},
	}
	specRenameItem = cmdSpec{
		params: []paramDef{
			{"-path", true, nil}, {"-literalpath", true, []string{"-pspath", "-lp"}},
			{"-newname", true, nil}, {"-force", false, nil}, {"-passthru", false, nil},
			{"-credential", true, nil}, {"-usetransaction", false, []string{"-usetx"}}},
		pos: []string{"-path", "-newname"},
	}
	specNewItem = cmdSpec{
		params: []paramDef{
			{"-path", true, nil}, {"-name", true, nil}, {"-itemtype", true, []string{"-type"}},
			{"-value", true, nil}, {"-force", false, nil}, {"-credential", true, nil},
			{"-usetransaction", false, []string{"-usetx"}}},
		pos: []string{"-path"},
	}
	specRemoveItem = cmdSpec{
		params: []paramDef{
			{"-path", true, nil}, {"-literalpath", true, []string{"-pspath", "-lp"}},
			{"-filter", true, nil}, {"-include", true, nil}, {"-exclude", true, nil},
			{"-recurse", false, nil}, {"-force", false, nil}, {"-credential", true, nil},
			{"-stream", true, nil}, {"-usetransaction", false, []string{"-usetx"}}},
		pos: []string{"-path"},
	}
	specTeeObject = cmdSpec{
		params: []paramDef{
			{"-filepath", true, []string{"-path"}}, {"-literalpath", true, []string{"-pspath", "-lp"}},
			{"-variable", true, nil}, {"-append", false, nil}, {"-inputobject", true, nil}},
		pos: []string{"-filepath"},
	}
	specGetContent = cmdSpec{
		params: []paramDef{
			{"-path", true, nil}, {"-literalpath", true, []string{"-pspath", "-lp"}},
			{"-totalcount", true, []string{"-first", "-head"}}, {"-tail", true, []string{"-last"}},
			{"-readcount", true, nil}, {"-wait", false, nil}, {"-raw", false, nil},
			{"-encoding", true, nil}, {"-delimiter", true, nil}, {"-filter", true, nil},
			{"-include", true, nil}, {"-exclude", true, nil}, {"-force", false, nil},
			{"-credential", true, nil}, {"-stream", true, nil}, {"-asbytestream", false, nil},
			{"-usetransaction", false, []string{"-usetx"}}},
		pos: []string{"-path"},
	}
	specSelectString = cmdSpec{
		params: []paramDef{
			{"-pattern", true, nil}, {"-path", true, nil}, {"-literalpath", true, []string{"-pspath", "-lp"}},
			{"-inputobject", true, nil}, {"-simplematch", false, nil}, {"-casesensitive", false, nil},
			{"-quiet", false, nil}, {"-list", false, nil}, {"-notmatch", false, nil},
			{"-allmatches", false, nil}, {"-encoding", true, nil}, {"-context", true, nil},
			{"-include", true, nil}, {"-exclude", true, nil}, {"-raw", false, nil},
			{"-noemphasis", false, nil}, {"-culture", true, nil}},
		pos: []string{"-pattern", "-path"},
	}
)

// specFor returns the parameter spec of a canonical cmdlet name, or nil.
func specFor(canon string) *cmdSpec {
	switch canon {
	case "start-sleep":
		return &specStartSleep
	case "out-file":
		return &specOutFile
	case "set-content", "add-content":
		return &specSetContent
	case "clear-content":
		return &specClearContent
	case "copy-item", "move-item":
		return &specCopyMove
	case "rename-item":
		return &specRenameItem
	case "new-item":
		return &specNewItem
	case "remove-item":
		return &specRemoveItem
	case "tee-object":
		return &specTeeObject
	case "get-content":
		return &specGetContent
	case "select-string":
		return &specSelectString
	}
	return nil
}

// resolve maps a lower-cased "-name" (possibly abbreviated, as PowerShell
// allows for unambiguous prefixes) to its definition, or nil.
func (s *cmdSpec) resolve(p string) *paramDef {
	for _, list := range [2][]paramDef{s.params, commonParams} {
		for i := range list {
			d := &list[i]
			if d.name == p {
				return d
			}
			for _, a := range d.alias {
				if a == p {
					return d
				}
			}
		}
	}
	var match *paramDef
	for _, list := range [2][]paramDef{s.params, commonParams} {
		for i := range list {
			if strings.HasPrefix(list[i].name, p) {
				if match != nil {
					return nil // ambiguous
				}
				match = &list[i]
			}
		}
	}
	return match
}

// item is one argument of a command after parameter/value grouping.
type item struct {
	param    bool
	name     string
	attached *model.Word
	words    []model.Word // value item: the words of a comma-joined array
}

// buildItems groups the tokens following the command word into parameter and
// value items and renders the node's argv (head word first).
func buildItems(head model.Word, rest []token) ([]item, []model.Word) {
	argv := []model.Word{head}
	var items []item
	lastVal, lastArgv := -1, -1
	join := false
	// parts accumulates the pieces of the comma-joined array at argv[lastArgv]
	// so the rendering is built once instead of by repeated concatenation.
	var parts []string
	flush := func() {
		if len(parts) > 1 {
			argv[lastArgv].Lit = strings.Join(parts, "")
		}
		parts = parts[:0]
	}
	for _, t := range rest {
		switch {
		case t.kind == tkParam:
			flush()
			items = append(items, item{param: true, name: t.param, attached: t.pval})
			argv = append(argv, model.Word{Lit: t.raw, Dynamic: t.pval != nil && t.pval.Dynamic})
			lastVal, join = -1, false
		case t.kind == tkOp && t.word.Lit == "," && lastVal >= 0:
			join = true
			parts = append(parts, ",")
		case join && lastVal >= 0:
			items[lastVal].words = append(items[lastVal].words, t.word)
			parts = append(parts, t.word.Lit)
			argv[lastArgv].Dynamic = argv[lastArgv].Dynamic || t.word.Dynamic
			join = false
		default:
			flush()
			items = append(items, item{words: []model.Word{t.word}})
			argv = append(argv, t.word)
			lastVal, lastArgv = len(items)-1, len(argv)-1
			parts = append(parts[:0], t.word.Lit)
			join = false
		}
	}
	flush()
	return items, argv
}

// bind assigns the items to parameters. params is the exported parameter map
// (lower-cased "-name" as written -> following non-parameter words until the
// next parameter, per the contract, whatever the cmdlet); bound maps canonical
// parameter names to their values (named or positional, bound by the real
// parameter table) for spec'd cmdlets; extra holds positional values that
// matched no slot.
func bind(spec *cmdSpec, items []item) (params, bound map[string][]model.Word, extra []model.Word) {
	params = map[string][]model.Word{}
	for i := 0; i < len(items); i++ {
		if !items[i].param {
			continue
		}
		vals := []model.Word{}
		if items[i].attached != nil {
			vals = append(vals, *items[i].attached)
		} else {
			for j := i + 1; j < len(items) && !items[j].param; j++ {
				vals = append(vals, items[j].words...)
			}
		}
		params[items[i].name] = append(params[items[i].name], vals...)
	}
	if spec == nil {
		return params, nil, nil
	}
	bound = map[string][]model.Word{}
	var positional [][]model.Word
	for i := 0; i < len(items); i++ {
		it := items[i]
		if !it.param {
			positional = append(positional, it.words)
			continue
		}
		def := spec.resolve(it.name)
		var vals []model.Word
		switch {
		case it.attached != nil:
			vals = []model.Word{*it.attached}
		case def != nil && def.value:
			if i+1 < len(items) && !items[i+1].param {
				vals = items[i+1].words
				i++
			}
		}
		if def != nil {
			bound[def.name] = append(bound[def.name], vals...)
		}
	}
	slot := 0
	for _, vals := range positional {
		for slot < len(spec.pos) {
			if !slotTaken(bound, spec.pos[slot]) {
				break
			}
			slot++
		}
		if slot < len(spec.pos) {
			bound[spec.pos[slot]] = vals
			slot++
			continue
		}
		extra = append(extra, vals...)
	}
	return params, bound, extra
}

// slotTaken reports whether the positional slot name is already bound. The
// -Path / -FilePath parameter and -LiteralPath belong to different parameter
// sets, so a bound -LiteralPath leaves no positional slot for the path: the
// next positional word binds to the following slot (Set-Content
// -LiteralPath a x: x is the -Value; Copy-Item -LiteralPath a b: b is the
// -Destination).
func slotTaken(bound map[string][]model.Word, name string) bool {
	if _, ok := bound[name]; ok {
		return true
	}
	if name == "-path" || name == "-filepath" {
		_, ok := bound["-literalpath"]
		return ok
	}
	return false
}
