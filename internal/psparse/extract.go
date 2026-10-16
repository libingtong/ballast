package psparse

import (
	"math"
	"strconv"
	"strings"

	"github.com/libingtong/ballast/internal/bashparse"
	"github.com/libingtong/ballast/internal/model"
)

// finishCommand fills the node reserved for a command element.
func (p *parser) finishCommand(c ctx, st *elemState, toks []token, redirs []redir) {
	head := toks[st.cmdTok].word
	items, argv := buildItems(head, toks[st.cmdTok+1:])
	n := model.CommandNode{Argv: argv, Name: st.canon, Loop: c.loop, Async: c.async, InSubst: c.inSubst}
	if head.Dynamic {
		n.Name = ""
	}
	spec := specFor(n.Name)
	params, bound, extra := bind(spec, items)
	if len(params) > 0 {
		n.Params = params
	}
	switch n.Name {
	case "start-job", "start-threadjob":
		n.Async = true
	case "start-sleep":
		si := sleepInfo(bound)
		n.Sleep = &si
	case "sleep":
		si := bashparse.ParseSleep(argv[1:])
		n.Sleep = &si
	case "git":
		n.Git = bashparse.ParseGit(argv)
	case ".":
		if len(argv) > 1 && argv[1].Dynamic {
			p.unknown("source")
		}
	}
	n.Writes, n.Reads = targetsOf(n.Name, argv, bound, extra)
	applyRedirs(&n, redirs)
	p.cl.Nodes[st.cmdIdx] = n
}

// words concatenates the values of the given canonical parameters.
func words(bound map[string][]model.Word, names ...string) []model.Word {
	var out []model.Word
	for _, n := range names {
		out = append(out, bound[n]...)
	}
	return out
}

func psTargets(ws []model.Word, via string) []model.WriteTarget {
	var out []model.WriteTarget
	for _, w := range ws {
		if w.Dynamic {
			out = append(out, model.WriteTarget{Dynamic: true, Via: via})
			continue
		}
		switch strings.ToLower(w.Lit) {
		case "", "$null", "/dev/null", "nul":
			continue
		}
		out = append(out, model.WriteTarget{Path: w.Lit, Via: via})
	}
	return out
}

// targetsOf derives the write and read targets of a command by its canonical
// name; external executables use the bashparse tables.
func targetsOf(canon string, argv []model.Word, bound map[string][]model.Word, extra []model.Word) (writes, reads []model.WriteTarget) {
	switch canon {
	case "out-file":
		writes = psTargets(words(bound, "-filepath", "-literalpath"), canon)
	case "set-content", "add-content", "clear-content":
		writes = psTargets(words(bound, "-path", "-literalpath"), canon)
	case "remove-item":
		ws := append(words(bound, "-path", "-literalpath"), extra...)
		writes = psTargets(ws, canon)
	case "copy-item", "move-item":
		writes = psTargets(words(bound, "-destination"), canon)
	case "rename-item":
		writes = renameTargets(bound)
	case "tee-object":
		writes = psTargets(words(bound, "-filepath", "-literalpath"), canon)
	case "new-item":
		writes = newItemTargets(bound)
	case "get-content":
		reads = psTargets(words(bound, "-path", "-literalpath"), canon)
	case "select-string":
		reads = psTargets(words(bound, "-path", "-literalpath"), canon)
	default:
		if canon != "" && !strings.Contains(canon, "-") {
			writes = bashparse.WriteTargets(canon, argv)
			reads = bashparse.ReadTargets(canon, argv)
		}
	}
	return writes, reads
}

// renameTargets lists both the source and the new name of Rename-Item. The
// new name is resolved against the directory of a single literal source
// path (-NewName is a leaf name, so the result is the renamed file's path).
func renameTargets(bound map[string][]model.Word) []model.WriteTarget {
	src := words(bound, "-path", "-literalpath")
	out := psTargets(src, "rename-item")
	var dir string
	if len(src) == 1 && !src[0].Dynamic {
		// a source ending in a separator names a directory: drop the
		// trailing separators first so the new name replaces that leaf.
		base := strings.TrimRight(src[0].Lit, `\/`)
		if i := strings.LastIndexAny(base, `\/`); i >= 0 {
			dir = base[:i+1]
		}
	}
	for _, nm := range bound["-newname"] {
		if dir != "" && !nm.Dynamic && !strings.ContainsAny(nm.Lit, `\/`) {
			nm.Lit = dir + nm.Lit
		}
		out = append(out, psTargets([]model.Word{nm}, "rename-item")...)
	}
	return out
}

// newItemTargets resolves New-Item's -Path and -Name into created paths.
func newItemTargets(bound map[string][]model.Word) []model.WriteTarget {
	paths, names := bound["-path"], bound["-name"]
	if len(names) == 0 {
		return psTargets(paths, "new-item")
	}
	if len(paths) == 0 {
		return psTargets(names, "new-item")
	}
	var joined []model.Word
	for _, p := range paths {
		for _, nm := range names {
			w := model.Word{Dynamic: p.Dynamic || nm.Dynamic}
			if !w.Dynamic {
				sep := `\`
				if !strings.Contains(p.Lit, `\`) {
					sep = "/"
				}
				w.Lit = p.Lit
				if !strings.HasSuffix(p.Lit, `\`) && !strings.HasSuffix(p.Lit, "/") {
					w.Lit += sep
				}
				w.Lit += nm.Lit
			}
			joined = append(joined, w)
		}
	}
	return psTargets(joined, "new-item")
}

// sleepInfo derives the duration of Start-Sleep from its bound parameters.
func sleepInfo(bound map[string][]model.Word) model.SleepInfo {
	total := 0.0
	have := false
	for _, spec := range [...]struct {
		param string
		mult  float64
		span  bool
	}{{"-seconds", 1, false}, {"-milliseconds", 0.001, false}, {"-duration", 1, true}} {
		vals, ok := bound[spec.param]
		if !ok {
			continue
		}
		if len(vals) != 1 || vals[0].Dynamic {
			return model.SleepInfo{}
		}
		var v float64
		var good bool
		if spec.span {
			v, good = parseTimeSpan(vals[0].Lit)
		} else {
			v, good = parsePSNumber(vals[0].Lit)
			v *= spec.mult
		}
		if !good {
			return model.SleepInfo{}
		}
		total += v
		have = true
	}
	if !have {
		return model.SleepInfo{}
	}
	return model.SleepInfo{Seconds: total, Known: true}
}

// parsePSNumber parses a non-negative decimal or 0x hex literal.
func parsePSNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if len(s) > 2 && (s[:2] == "0x" || s[:2] == "0X") {
		u, err := strconv.ParseUint(s[2:], 16, 53)
		return float64(u), err == nil
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case isDigit(c), c == '.', c == 'e', c == 'E', c == '+', c == '-':
		default:
			return 0, false
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// parseTimeSpan parses [d.]hh:mm[:ss[.fff]].
func parseTimeSpan(s string) (float64, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	days := 0.0
	first := parts[0]
	if i := strings.IndexByte(first, '.'); i >= 0 {
		d, ok := parsePSNumber(first[:i])
		if !ok {
			return 0, false
		}
		days, first = d, first[i+1:]
	}
	h, ok1 := parsePSNumber(first)
	m, ok2 := parsePSNumber(parts[1])
	sec, ok3 := 0.0, true
	if len(parts) == 3 {
		sec, ok3 = parsePSNumber(parts[2])
	}
	if !ok1 || !ok2 || !ok3 {
		return 0, false
	}
	return days*86400 + h*3600 + m*60 + sec, true
}
