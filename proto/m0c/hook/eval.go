package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

// 三值逻辑:unknown 不命中(fail-open)并累加诊断计数。
type tri int8

const (
	fls tri = iota
	tru
	unk
)

func triOf(b bool) tri {
	if b {
		return tru
	}
	return fls
}

type Input struct {
	SessionID            string          `json:"session_id"`
	Cwd                  string          `json:"cwd"`
	HookEventName        string          `json:"hook_event_name"`
	ToolName             string          `json:"tool_name"`
	ToolInput            json.RawMessage `json:"tool_input"`
	ToolResponse         json.RawMessage `json:"tool_response"`
	ToolUseID            string          `json:"tool_use_id"`
	AgentID              string          `json:"agent_id"`
	AgentType            string          `json:"agent_type"`
	Prompt               string          `json:"prompt"`
	LastAssistantMessage string          `json:"last_assistant_message"`
	Source               string          `json:"source"`
}

type Hit struct {
	Rule    *Rule
	Matched string
}

type evalCtx struct {
	in       *Input
	rs       *Ruleset
	st       *State
	home     string
	ti       map[string]any
	tiDone   bool
	paths    []string
	pathsOK  bool
	bash     *BashInfo
	bashErr  error
	bashDone bool
	bashUS   int64

	unknown  int
	snapMemo map[string]tri
	snapMS   int64
	snapTree string
	snapStep snapSteps
	snapRan  bool
}

func (c *evalCtx) toolInput() map[string]any {
	if !c.tiDone {
		c.tiDone = true
		if len(c.in.ToolInput) > 0 {
			_ = json.Unmarshal(c.in.ToolInput, &c.ti)
		}
	}
	return c.ti
}

func (c *evalCtx) str(field string) (string, bool) {
	v, ok := c.toolInput()[field]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func (c *evalCtx) targetPaths() []string {
	if c.pathsOK {
		return c.paths
	}
	c.pathsOK = true
	for _, f := range []string{"file_path", "notebook_path", "path"} {
		if s, ok := c.str(f); ok && s != "" {
			if !filepath.IsAbs(s) {
				s = filepath.Join(c.in.Cwd, s)
			}
			c.paths = append(c.paths, filepath.ToSlash(filepath.Clean(s)))
		}
	}
	return c.paths
}

func (c *evalCtx) bashInfo() (*BashInfo, error) {
	if !c.bashDone {
		c.bashDone = true
		cmd, _ := c.str("command")
		t := time.Now()
		c.bash, c.bashErr = parseBash(cmd)
		c.bashUS = time.Since(t).Microseconds()
	}
	return c.bash, c.bashErr
}

func globAny(globs []string, s string) bool {
	for _, g := range globs {
		if ok, _ := doublestar.Match(g, s); ok {
			return true
		}
	}
	return false
}

func (c *evalCtx) evaluate() []Hit {
	var hits []Hit
	for i := range c.rs.Rules {
		r := &c.rs.Rules[i]
		if r.Event != c.in.HookEventName {
			continue
		}
		m, matched := c.matchRule(r)
		switch m {
		case unk:
			c.unknown++
		case tru:
			hits = append(hits, Hit{Rule: r, Matched: matched})
		}
	}
	return hits
}

// and 合并:有 false 即 false;否则有 unknown 即 unknown。
func and(a, b tri) tri {
	if a == fls || b == fls {
		return fls
	}
	if a == unk || b == unk {
		return unk
	}
	return tru
}

func (c *evalCtx) matchRule(r *Rule) (tri, string) {
	if len(r.Tool) > 0 && !globAny(r.Tool, c.in.ToolName) {
		return fls, ""
	}
	if len(r.Path) > 0 {
		ps := c.targetPaths()
		hit := false
		for _, p := range ps {
			if globAny(r.Path, p) {
				hit = true
				break
			}
		}
		if !hit {
			return fls, ""
		}
	}
	for field, rx := range r.InputRegex {
		s, ok := c.str(field)
		if !ok {
			return fls, ""
		}
		re := getRe(rx)
		if re == nil {
			return unk, ""
		}
		if !re.MatchString(s) {
			return fls, ""
		}
	}
	if r.RespRegex != "" {
		re := getRe(r.RespRegex)
		if re == nil {
			return unk, ""
		}
		if !re.Match(c.in.ToolResponse) {
			return fls, ""
		}
	}
	matched := ""
	if r.Text != nil {
		src := c.in.LastAssistantMessage
		if r.Text.Source == "prompt" {
			src = c.in.Prompt
		}
		if r.Text.TailChars > 0 {
			src = tail(src, r.Text.TailChars)
		}
		re := getRe(r.Text.Regex)
		if re == nil {
			return unk, ""
		}
		loc := re.FindStringIndex(src)
		if loc == nil {
			return fls, ""
		}
		matched = src[loc[0]:loc[1]]
		for _, nx := range r.Text.NotRegex {
			nre := getRe(nx)
			if nre == nil {
				return unk, ""
			}
			if nre.MatchString(src) {
				return fls, ""
			}
		}
	}
	res := tru
	if r.Bash != nil {
		m, mm := c.matchBash(r.Bash)
		if m == fls {
			return fls, ""
		}
		res = and(res, m)
		matched = mm
	}
	for _, cd := range r.When {
		v := c.pred(cd)
		if cd.Not && v != unk {
			v = triOf(v == fls)
		}
		if v == fls {
			return fls, ""
		}
		res = and(res, v)
	}
	if matched == "" {
		matched = c.in.ToolName
	}
	if len(matched) > 200 {
		matched = matched[:200]
	}
	return res, matched
}

func (c *evalCtx) matchBash(b *BashMatch) (tri, string) {
	bi, err := c.bashInfo()
	if err != nil || bi == nil {
		return unk, ""
	}
	if len(b.PipeFrom) > 0 || len(b.PipeTo) > 0 {
		for _, p := range bi.Pipes {
			if contains(b.PipeFrom, p[0]) && contains(b.PipeTo, p[1]) {
				return tru, p[0] + "|" + p[1]
			}
		}
		return fls, ""
	}
	bg, _ := c.toolInput()["run_in_background"].(bool)
	for i := range bi.Nodes {
		n := &bi.Nodes[i]
		name := baseName(n.Argv[0])
		if len(b.Cmd) > 0 && !contains(b.Cmd, name) {
			continue
		}
		if b.MinSleepS > 0 {
			if name != "sleep" {
				continue
			}
			secs, ok := sleepSeconds(n.Argv[1:])
			if !ok || secs < b.MinSleepS {
				continue
			}
		}
		if b.NotAsync && (n.Async || bg) {
			continue
		}
		args := n.Argv[1:]
		if b.GitSub != "" {
			sub, rest, _ := gitSub(n.Argv)
			if sub != b.GitSub {
				continue
			}
			args = rest
		}
		if len(b.Flags) > 0 {
			ok := false
			for _, f := range b.Flags {
				if hasFlag(args, f) {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		if len(b.WritesGlob) > 0 {
			ok := false
			for _, w := range n.Writes {
				if globAny(b.WritesGlob, filepath.ToSlash(filepath.Clean(w))) {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		if b.ArgRegex != "" {
			re := getRe(b.ArgRegex)
			if re == nil {
				return unk, ""
			}
			if !re.MatchString(strings.Join(n.Argv, " ")) {
				continue
			}
		}
		return tru, strings.Join(n.Argv, " ")
	}
	return fls, ""
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func (c *evalCtx) pred(cd Cond) tri {
	switch cd.Pred {
	case "read_this_session":
		ps := c.targetPaths()
		if len(ps) == 0 {
			return unk
		}
		for _, p := range ps {
			if !contains(c.st.Read, p) {
				return fls
			}
		}
		return tru
	case "path_exists":
		ps := c.targetPaths()
		if len(ps) == 0 {
			return unk
		}
		for _, p := range ps {
			if _, err := os.Stat(p); err != nil {
				return fls
			}
		}
		return tru
	case "is_subagent":
		return triOf(c.in.AgentID != "")
	case "prior_hit":
		return triOf(c.st.PriorHit[cd.Rule] >= cd.Gte)
	case "verify_fresh":
		return c.verifyFresh()
	}
	return unk
}

// verifyFresh:算出 C(或 Stop 时的 S)并与 evidence 记录里的 s_end 比较。
// 没有 evidence 记录 => 不新鲜(false);不在 git 仓库 => unknown。
func (c *evalCtx) verifyFresh() tri {
	if c.snapMemo == nil {
		c.snapMemo = map[string]tri{}
	}
	addU := c.in.HookEventName != "PreToolUse"
	if c.in.HookEventName == "PreToolUse" {
		if bi, err := c.bashInfo(); err == nil && bi != nil {
			for i := range bi.Nodes {
				if sub, rest, _ := gitSub(bi.Nodes[i].Argv); sub == "commit" && (hasFlag(rest, "-a") || hasFlag(rest, "--all")) {
					addU = true
				}
			}
		}
	}
	key := "C"
	if addU {
		key = "S"
	}
	if v, ok := c.snapMemo[key]; ok {
		return v
	}
	if os.Getenv("BALLAST_SNAPSHOT") == "0" {
		c.snapMemo[key] = unk
		return unk
	}
	t := time.Now()
	tree, steps, err := snapshot(c.in.Cwd, c.rs.Exempt, addU)
	c.snapMS += time.Since(t).Microseconds()
	c.snapStep = steps
	c.snapRan = true
	if err != nil {
		c.snapMemo[key] = unk
		return unk
	}
	c.snapTree = tree
	b, err := os.ReadFile(filepath.Join(c.home, "evidence", repoInst(c.in.Cwd)+".json"))
	res := fls
	if err == nil {
		var ev struct {
			SEnd string `json:"s_end"`
		}
		if json.Unmarshal(b, &ev) == nil && ev.SEnd == tree {
			res = tru
		}
	}
	c.snapMemo[key] = res
	return res
}
