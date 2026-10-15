//go:build !windows

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"github.com/bmatcuk/doublestar/v4"
)

// cacheVersion 变化会使旧的 gob 缓存失效。
const cacheVersion = "m0c-1"

var (
	knownEvents  = map[string]bool{"PreToolUse": true, "UserPromptSubmit": true, "PostToolUse": true, "PostToolUseFailure": true, "Stop": true, "SubagentStop": true, "SessionStart": true}
	knownActions = map[string]bool{"deny": true, "ask": true, "warn": true, "log": true}
	ruleIDRe     = regexp.MustCompile(`^[a-z0-9-]{3,40}$`)
)

// Rule 是原型用的最小 DSL。字段之间 AND,列表内部 OR。
type Rule struct {
	ID         string
	Event      string
	Action     string
	Tool       []string
	Path       []string
	InputRegex map[string]string
	RespRegex  string
	Bash       *BashMatch
	Text       *TextMatch
	When       []Cond
	Message    string
	Inject     string
	Summary    string
}

type BashMatch struct {
	Cmd        []string
	MinSleepS  float64
	NotAsync   bool
	GitSub     string
	Flags      []string
	WritesGlob []string
	ArgRegex   string
	PipeFrom   []string
	PipeTo     []string
}

type TextMatch struct {
	Source    string // prompt | last_assistant_message
	Regex     string
	NotRegex  []string
	TailChars int
}

type Cond struct {
	Pred string
	Not  bool
	Gte  int
	Rule string
}

// Ruleset 是编译(校验 + 规范化)后的结果,可整体 gob 序列化。
// 注意:*regexp.Regexp 无法跨进程序列化(只有源码文本),因此正则在求值时按需惰性编译。
type Ruleset struct {
	Version string
	Hash    string
	Exempt  []string
	Rules   []Rule
}

type ruleFile struct {
	Project struct {
		Exempt []string `json:"exempt"`
	} `json:"project"`
	Rules []jsonRule `json:"rules"`
}

type jsonRule struct {
	ID         string            `json:"id"`
	Enabled    *bool             `json:"enabled"`
	Event      string            `json:"event"`
	Action     string            `json:"action"`
	Tool       []string          `json:"tool"`
	Path       []string          `json:"path"`
	InputRegex map[string]string `json:"input_regex"`
	RespRegex  string            `json:"response_regex"`
	Bash       *struct {
		Cmd        []string `json:"cmd"`
		MinSleepS  float64  `json:"min_sleep_s"`
		NotAsync   bool     `json:"not_async"`
		GitSub     string   `json:"git_sub"`
		Flags      []string `json:"flags"`
		WritesGlob []string `json:"writes_glob"`
		ArgRegex   string   `json:"arg_regex"`
		PipeFrom   []string `json:"pipe_from"`
		PipeTo     []string `json:"pipe_to"`
	} `json:"bash"`
	Text *struct {
		Source    string   `json:"source"`
		Regex     string   `json:"regex"`
		NotRegex  []string `json:"not_regex"`
		TailChars int      `json:"tail_chars"`
	} `json:"text"`
	When []struct {
		Pred string `json:"pred"`
		Not  bool   `json:"not"`
		Gte  int    `json:"gte"`
		Rule string `json:"rule"`
	} `json:"when"`
	Message string `json:"message"`
	Inject  string `json:"inject"`
	Summary string `json:"summary"`
}

// loadRules 以规则文件的 stat(路径、size、mtime_ns、inode)为键查 gob 缓存;
// 未命中则读取 JSON、全量校验并编译(所有正则与 glob 都会编译一遍,等价 lint),再原子写缓存。
func loadRules(home, rulesPath string) (rs *Ruleset, cacheState string, err error) {
	fi, err := os.Stat(rulesPath)
	if err != nil {
		return nil, "", err
	}
	var ino uint64
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		ino = uint64(st.Ino)
	}
	h := fnv.New64a()
	fmt.Fprintf(h, "%s|%s|%d|%d|%d", cacheVersion, rulesPath, fi.Size(), fi.ModTime().UnixNano(), ino)
	cachePath := filepath.Join(home, "cache", fmt.Sprintf("%016x.gob", h.Sum64()))

	if b, rerr := os.ReadFile(cachePath); rerr == nil {
		var c Ruleset
		if derr := gob.NewDecoder(bytes.NewReader(b)).Decode(&c); derr == nil && c.Version == cacheVersion {
			return &c, "hit", nil
		}
	}
	raw, err := os.ReadFile(rulesPath)
	if err != nil {
		return nil, "", err
	}
	rs, err = compileRules(raw)
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(rs); err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		return nil, "", err
	}
	if err := atomicWrite(cachePath, buf.Bytes(), false); err != nil {
		return nil, "", err
	}
	return rs, "cold", nil
}

func compileRules(raw []byte) (*Ruleset, error) {
	var f ruleFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("rules json: %w", err)
	}
	sum := sha256.Sum256(raw)
	rs := &Ruleset{Version: cacheVersion, Hash: hex.EncodeToString(sum[:8]), Exempt: f.Project.Exempt}
	seen := map[string]bool{}
	for _, jr := range f.Rules {
		if jr.Enabled != nil && !*jr.Enabled {
			continue
		}
		if !ruleIDRe.MatchString(jr.ID) || seen[jr.ID] {
			return nil, fmt.Errorf("rule id invalid or duplicated: %q", jr.ID)
		}
		seen[jr.ID] = true
		if !knownEvents[jr.Event] {
			return nil, fmt.Errorf("rule %s: unknown event %q", jr.ID, jr.Event)
		}
		r := Rule{ID: jr.ID, Event: jr.Event, Action: jr.Action, Tool: jr.Tool, Path: jr.Path,
			InputRegex: jr.InputRegex, RespRegex: jr.RespRegex, Message: jr.Message, Inject: jr.Inject, Summary: jr.Summary}
		if r.Action == "" {
			r.Action = "log"
		}
		if !knownActions[r.Action] {
			return nil, fmt.Errorf("rule %s: unknown action %q", jr.ID, r.Action)
		}
		for _, g := range append(append([]string{}, r.Tool...), r.Path...) {
			if !doublestar.ValidatePattern(g) {
				return nil, fmt.Errorf("rule %s: bad glob %q", jr.ID, g)
			}
		}
		regs := []string{r.RespRegex}
		for _, v := range r.InputRegex {
			regs = append(regs, v)
		}
		if jr.Bash != nil {
			b := BashMatch(*jr.Bash)
			r.Bash = &b
			regs = append(regs, b.ArgRegex)
			for _, g := range b.WritesGlob {
				if !doublestar.ValidatePattern(g) {
					return nil, fmt.Errorf("rule %s: bad glob %q", jr.ID, g)
				}
			}
		}
		if jr.Text != nil {
			t := TextMatch(*jr.Text)
			if t.Source == "" {
				t.Source = "last_assistant_message"
			}
			r.Text = &t
			regs = append(regs, t.Regex)
			regs = append(regs, t.NotRegex...)
		}
		for _, c := range jr.When {
			r.When = append(r.When, Cond(c))
		}
		for _, s := range regs {
			if s == "" {
				continue
			}
			if _, err := regexp.Compile(s); err != nil {
				return nil, fmt.Errorf("rule %s: bad regex %q: %w", jr.ID, s, err)
			}
		}
		rs.Rules = append(rs.Rules, r)
	}
	for _, g := range rs.Exempt {
		if !doublestar.ValidatePattern(g) {
			return nil, fmt.Errorf("bad exempt glob %q", g)
		}
	}
	return rs, nil
}

// 惰性正则缓存:只编译本次求值真正用到的正则。
var reMemo = map[string]*regexp.Regexp{}

func getRe(s string) *regexp.Regexp {
	if re, ok := reMemo[s]; ok {
		return re
	}
	re, err := regexp.Compile(s)
	if err != nil {
		re = nil
	}
	reMemo[s] = re
	return re
}
