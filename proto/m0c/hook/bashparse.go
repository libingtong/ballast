//go:build !windows

package main

import (
	"regexp"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// CmdNode 是从 Bash AST 展开出的命令节点(B1 "Bash 解析契约"的最小子集)。
type CmdNode struct {
	Argv    []string // 已剥离包装器;动态词(含变量/命令替换)记为 "\x00dyn"
	Wrapped []string
	Async   bool
	InSubst bool
	Writes  []string
}

type BashInfo struct {
	Nodes []CmdNode
	Pipes [][2]string // (左侧命令名, 右侧命令名)
}

const dyn = "\x00dyn"

func parseBash(cmd string) (*BashInfo, error) {
	bi := &BashInfo{}
	if err := parseInto(bi, cmd, 0); err != nil {
		return nil, err
	}
	return bi, nil
}

func parseInto(bi *BashInfo, src string, depth int) error {
	p := syntax.NewParser(syntax.Variant(syntax.LangBash))
	f, err := p.Parse(strings.NewReader(src), "")
	if err != nil {
		return err
	}
	async := map[*syntax.CallExpr]bool{}
	writes := map[*syntax.CallExpr][]string{}
	var stack []syntax.Node
	syntax.Walk(f, func(n syntax.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		switch x := n.(type) {
		case *syntax.Stmt:
			if ce, ok := x.Cmd.(*syntax.CallExpr); ok {
				if x.Background {
					async[ce] = true
				}
				for _, rd := range x.Redirs {
					switch rd.Op {
					case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.ClbOut:
						if s, ok := wordStr(rd.Word); ok {
							writes[ce] = append(writes[ce], s)
						}
					}
				}
			}
		case *syntax.BinaryCmd:
			if x.Op == syntax.Pipe {
				l, r := rightmostName(x.X), leftmostName(x.Y)
				if l != "" && r != "" {
					bi.Pipes = append(bi.Pipes, [2]string{l, r})
				}
			}
		case *syntax.CallExpr:
			inSubst := false
			for _, s := range stack {
				if _, ok := s.(*syntax.CmdSubst); ok {
					inSubst = true
					break
				}
			}
			argv := make([]string, 0, len(x.Args))
			for _, w := range x.Args {
				if s, ok := wordStr(w); ok {
					argv = append(argv, s)
				} else {
					argv = append(argv, dyn)
				}
			}
			argv, wrapped := stripWrappers(argv)
			if len(argv) == 0 {
				return true
			}
			node := CmdNode{Argv: argv, Wrapped: wrapped, Async: async[x], InSubst: inSubst, Writes: writes[x]}
			bi.Nodes = append(bi.Nodes, node)
			// sh/bash/zsh -c '<str>' 递归解析,最多 2 层。
			if depth < 2 && isShell(argv[0]) {
				for i := 1; i+1 < len(argv); i++ {
					if argv[i] == "-c" && argv[i+1] != dyn {
						_ = parseInto(bi, argv[i+1], depth+1) // 内层解析失败只丢弃内层节点
						break
					}
				}
			}
		}
		return true
	})
	return nil
}

func isShell(s string) bool {
	b := baseName(s)
	return b == "sh" || b == "bash" || b == "zsh"
}

func baseName(s string) string {
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func cmdName(c syntax.Command) string {
	if ce, ok := c.(*syntax.CallExpr); ok && len(ce.Args) > 0 {
		if s, ok := wordStr(ce.Args[0]); ok {
			return baseName(s)
		}
	}
	return ""
}

func rightmostName(s *syntax.Stmt) string {
	if b, ok := s.Cmd.(*syntax.BinaryCmd); ok && b.Op == syntax.Pipe {
		return rightmostName(b.Y)
	}
	return cmdName(s.Cmd)
}

func leftmostName(s *syntax.Stmt) string { return cmdName(s.Cmd) }

// wordStr 仅在词完全由字面量/单双引号字面量构成时返回其值。
func wordStr(w *syntax.Word) (string, bool) {
	if w == nil {
		return "", false
	}
	var sb strings.Builder
	for _, p := range w.Parts {
		switch x := p.(type) {
		case *syntax.Lit:
			sb.WriteString(x.Value)
		case *syntax.SglQuoted:
			sb.WriteString(x.Value)
		case *syntax.DblQuoted:
			for _, q := range x.Parts {
				l, ok := q.(*syntax.Lit)
				if !ok {
					return "", false
				}
				sb.WriteString(l.Value)
			}
		default:
			return "", false
		}
	}
	return sb.String(), true
}

// stripWrappers 剥离 env VAR=、nice、nohup、timeout N、time、command、exec、sudo。
func stripWrappers(argv []string) ([]string, []string) {
	var wrapped []string
	for len(argv) > 0 {
		name := baseName(argv[0])
		switch name {
		case "env":
			i := 1
			for i < len(argv) && (strings.HasPrefix(argv[i], "-") || strings.Contains(argv[i], "=")) {
				i++
			}
			argv = argv[i:]
		case "nohup", "command", "exec", "time":
			argv = argv[1:]
		case "nice":
			i := 1
			if i+1 < len(argv) && argv[i] == "-n" {
				i += 2
			} else if i < len(argv) && strings.HasPrefix(argv[i], "-") {
				i++
			}
			argv = argv[i:]
		case "timeout":
			i := 1
			for i < len(argv) && strings.HasPrefix(argv[i], "-") {
				i++
			}
			if i < len(argv) {
				i++ // 时长
			}
			argv = argv[i:]
		case "sudo":
			i := 1
			for i < len(argv) && strings.HasPrefix(argv[i], "-") {
				if argv[i] == "-u" || argv[i] == "-g" {
					i++
				}
				i++
			}
			if i > len(argv) {
				i = len(argv)
			}
			argv = argv[i:]
		default:
			return argv, wrapped
		}
		wrapped = append(wrapped, name)
	}
	return argv, wrapped
}

var durRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)([smhd]?)$`)

// sleepSeconds 支持 `300`、`5m`、`1h`,多个参数求和。动态词返回 ok=false。
func sleepSeconds(args []string) (float64, bool) {
	total := 0.0
	n := 0
	for _, a := range args {
		if a == dyn {
			return 0, false
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		m := durRe.FindStringSubmatch(a)
		if m == nil {
			return 0, false
		}
		v, _ := strconv.ParseFloat(m[1], 64)
		switch m[2] {
		case "m":
			v *= 60
		case "h":
			v *= 3600
		case "d":
			v *= 86400
		}
		total += v
		n++
	}
	return total, n > 0
}

// gitSub 解析 git 全局参数 -C/-c/--git-dir/--work-tree/--no-pager,返回子命令与其后参数。
func gitSub(argv []string) (sub string, rest []string, dir string) {
	if len(argv) == 0 || baseName(argv[0]) != "git" {
		return "", nil, ""
	}
	i := 1
	for i < len(argv) {
		a := argv[i]
		switch {
		case a == "-C" || a == "-c" || a == "--git-dir" || a == "--work-tree":
			if a == "-C" && i+1 < len(argv) {
				dir = argv[i+1]
			}
			i += 2
		case strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree="), a == "--no-pager", a == "--no-optional-locks":
			i++
		case strings.HasPrefix(a, "-"):
			i++
		default:
			return a, argv[i+1:], dir
		}
	}
	return "", nil, dir
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
		// 短选项合并,如 -am
		if len(flag) == 2 && flag[0] == '-' && flag[1] != '-' && len(a) > 2 && a[0] == '-' && a[1] != '-' && strings.IndexByte(a[1:], flag[1]) >= 0 {
			// 避免把 -m 的值当成选项:这里只在 a 全是字母时认定为合并选项
			ok := true
			for _, c := range a[1:] {
				if c < 'a' || c > 'z' {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
	}
	return false
}
