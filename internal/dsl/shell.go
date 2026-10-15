package dsl

import (
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// matchNode evaluates a shell matcher against Nodes[idx]: every present
// field must hold on this node (dsl-semantics §5.4).
func (c *evalCtx) matchNode(sp *shellProg, cl *model.CommandList, idx int, kind string) res {
	n := &cl.Nodes[idx]
	m := sp.m
	out := resTrue
	step := func(r res) bool {
		out = out.and(r)
		return out.t == model.TriFalse
	}
	if sp.cmd != nil && step(nodeCmd(sp, n, kind)) {
		return out
	}
	if len(sp.gitSub) > 0 && step(nodeGitSub(sp, n, kind)) {
		return out
	}
	if m.Async != nil && step(fromBool(n.Async == *m.Async)) {
		return out
	}
	if m.InLoop != nil && step(fromBool((n.Loop != nil) == *m.InLoop)) {
		return out
	}
	if m.LoopBounded != nil {
		v := model.TriFalse
		if n.Loop != nil {
			v = n.Loop.Bounded
		}
		if !*m.LoopBounded {
			v = v.Not()
		}
		if step(fromTri(v, kind+":loop_bounded")) {
			return out
		}
	}
	if m.InSubst != nil && step(fromBool(n.InSubst == *m.InSubst)) {
		return out
	}
	if m.Sudo != nil && step(fromBool(n.Sudo == *m.Sudo)) {
		return out
	}
	if m.Dynamic != nil && step(fromBool(n.HasDynamicArgv() == *m.Dynamic)) {
		return out
	}
	if m.SleepS != nil && step(nodeSleep(m.SleepS, n, kind)) {
		return out
	}
	if len(sp.flags) > 0 && step(fromBool(nodeHasAnyFlag(n, sp.flags, sp.kind == model.ShellPowerShell))) {
		return out
	}
	if len(sp.args) > 0 && step(nodeArgs(sp, n, kind)) {
		return out
	}
	if sp.writes != nil {
		r, _ := c.writesMatch(sp.writes, n.Writes, kind)
		if step(r) {
			return out
		}
	}
	if sp.pipeFrom != nil && step(pipeNeighbour(cl, idx, true, sp.pipeFrom, kind)) {
		return out
	}
	if sp.pipeTo != nil && step(pipeNeighbour(cl, idx, false, sp.pipeTo, kind)) {
		return out
	}
	return out
}

func nodeCmd(sp *shellProg, n *model.CommandNode, kind string) res {
	if n.Name == "" {
		if len(n.Argv) > 0 && n.Argv[0].Dynamic {
			return unk(kind + ":dynamic_command")
		}
		return resFalse
	}
	return fromBool(sp.cmd.match(n.Name))
}

func nodeGitSub(sp *shellProg, n *model.CommandNode, kind string) res {
	if n.Git == nil {
		return resFalse
	}
	if n.Git.SubDynamic {
		return unk(kind + ":dynamic_git_sub")
	}
	for _, s := range sp.gitSub {
		if n.Git.Sub == s {
			return resTrue
		}
	}
	return resFalse
}

func nodeSleep(cmp *model.Cmp, n *model.CommandNode, kind string) res {
	if n.Sleep == nil {
		return resFalse
	}
	if !n.Sleep.Known {
		return unk(kind + ":dynamic_sleep")
	}
	return fromBool(cmp.Eval(n.Sleep.Seconds))
}

func nodeArgs(sp *shellProg, n *model.CommandNode, kind string) res {
	var lits []string
	dynamic := false
	for i := 1; i < len(n.Argv); i++ {
		if n.Argv[i].Dynamic {
			dynamic = true
			continue
		}
		lits = append(lits, n.Argv[i].Lit)
	}
	joined := strings.Join(lits, " ")
	bad := false
	for _, l := range sp.args {
		re, err := l.get()
		if err != nil {
			bad = true
			continue
		}
		if re.MatchString(joined) {
			return resTrue
		}
	}
	switch {
	case bad:
		return unk(kind + ":bad_regex")
	case dynamic:
		return unk(kind + ":dynamic_args")
	}
	return resFalse
}

// nodeHasAnyFlag implements the flags matcher: "--long" matches exactly or
// as "--long=…"; "-x" matches the standalone word and all-letter combined
// short options ("-anm" contains "-n"). PowerShell parameters compare
// case-insensitively and are never combined.
func nodeHasAnyFlag(n *model.CommandNode, flags []string, ps bool) bool {
	for i := 1; i < len(n.Argv); i++ {
		w := n.Argv[i]
		if w.Dynamic {
			continue
		}
		for _, f := range flags {
			if flagMatches(w.Lit, f, ps) {
				return true
			}
		}
	}
	return false
}

func flagMatches(word, flag string, ps bool) bool {
	if ps {
		return strings.EqualFold(word, flag) || (strings.HasPrefix(flag, "-") && len(word) > len(flag) &&
			strings.EqualFold(word[:len(flag)], flag) && (word[len(flag)] == ':' || word[len(flag)] == '='))
	}
	if strings.HasPrefix(flag, "--") {
		return word == flag || strings.HasPrefix(word, flag+"=")
	}
	if len(flag) == 2 && flag[0] == '-' && isASCIILetter(flag[1]) {
		if word == flag {
			return true
		}
		if len(word) > 2 && word[0] == '-' && word[1] != '-' && allASCIILetters(word[1:]) {
			return strings.IndexByte(word[1:], flag[1]) >= 0
		}
		return false
	}
	return word == flag
}

func isASCIILetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

func allASCIILetters(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isASCIILetter(s[i]) {
			return false
		}
	}
	return len(s) > 0
}

// pipeNeighbour implements pipe_from (left=true: the node at the left of a
// pipe into idx) and pipe_to (left=false: the node idx pipes into).
func pipeNeighbour(cl *model.CommandList, idx int, left bool, g *nameGlobs, kind string) res {
	out := resFalse
	for _, e := range cl.Pipes {
		var other int
		switch {
		case left && e.To == idx:
			other = e.From
		case !left && e.From == idx:
			other = e.To
		default:
			continue
		}
		if other < 0 || other >= len(cl.Nodes) {
			continue
		}
		nb := &cl.Nodes[other]
		if nb.Name == "" {
			if len(nb.Argv) > 0 && nb.Argv[0].Dynamic {
				out = out.or(unk(kind + ":dynamic_pipe_command"))
			}
			continue
		}
		if g.match(nb.Name) {
			return resTrue
		}
	}
	return out
}
