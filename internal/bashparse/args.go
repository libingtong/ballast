package bashparse

import (
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// optSpec describes which options of a command take a value.
type optSpec struct {
	// short lists short option letters that take a value (attached rest of
	// the cluster, else the next word).
	short string
	// optShort lists short letters whose value can only be attached (the
	// rest of the cluster); they never consume the next word (sed -i).
	optShort string
	// long lists long option names (without "--") that take the next word
	// when no "=value" is attached.
	long []string
}

func (sp *optSpec) takesLong(name string) bool {
	for _, l := range sp.long {
		if l == name {
			return true
		}
	}
	return false
}

// scanArgs walks args like a GNU getopt with permutation. For every option it
// calls on(short, long, val): short is the option letter (0 for long options),
// long the long name, val the option value (nil when it has none). Words that
// are not options are returned as operands, in order; everything after "--" is
// an operand. A lone "-" is an operand. Dynamic words are always operands.
func scanArgs(args []model.Word, sp *optSpec, on func(short byte, long string, val *model.Word)) []model.Word {
	var operands []model.Word
	for i := 0; i < len(args); i++ {
		a := args[i]
		s := a.Lit
		if a.Dynamic || len(s) < 2 || s[0] != '-' {
			operands = append(operands, a)
			continue
		}
		if s == "--" {
			operands = append(operands, args[i+1:]...)
			return operands
		}
		if s[1] == '-' {
			name, v, attached := strings.Cut(s[2:], "=")
			switch {
			case attached:
				on(0, name, &model.Word{Lit: v})
			case sp != nil && sp.takesLong(name) && i+1 < len(args):
				i++
				on(0, name, &args[i])
			default:
				on(0, name, nil)
			}
			continue
		}
		for j := 1; j < len(s); j++ {
			ch := s[j]
			if sp != nil && strings.IndexByte(sp.short, ch) >= 0 {
				switch {
				case j+1 < len(s):
					on(ch, "", &model.Word{Lit: s[j+1:]})
				case i+1 < len(args):
					i++
					on(ch, "", &args[i])
				default:
					on(ch, "", nil)
				}
				break
			}
			if sp != nil && strings.IndexByte(sp.optShort, ch) >= 0 {
				if j+1 < len(s) {
					on(ch, "", &model.Word{Lit: s[j+1:]})
				} else {
					on(ch, "", nil)
				}
				break
			}
			on(ch, "", nil)
		}
	}
	return operands
}

// target builds a path target from a word; ok is false for words that name no
// file (empty, or /dev/null).
func target(w model.Word, via string) (model.WriteTarget, bool) {
	if w.Dynamic {
		return model.WriteTarget{Dynamic: true, Via: via}, true
	}
	if w.Lit == "" || w.Lit == "/dev/null" {
		return model.WriteTarget{}, false
	}
	return model.WriteTarget{Path: w.Lit, Via: via}, true
}

// targets maps operands to targets, skipping "-" (stdin/stdout) and any word
// for which skip returns true.
func targets(ops []model.Word, via string, skip func(string) bool) []model.WriteTarget {
	var out []model.WriteTarget
	for _, o := range ops {
		if !o.Dynamic && (o.Lit == "-" || skip != nil && skip(o.Lit)) {
			continue
		}
		if t, ok := target(o, via); ok {
			out = append(out, t)
		}
	}
	return out
}
