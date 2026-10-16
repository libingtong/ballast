package bashparse

import (
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// WriteTargets derives write targets from a node's argv (tee/cp/mv/sed -i,
// perl/ruby -i, git checkout -- / git restore, ...);
// redirect targets are added by Parse. name is the command's basename and
// argv[0] the command word.
func WriteTargets(name string, argv []model.Word) []model.WriteTarget {
	if len(argv) < 2 {
		return nil
	}
	args := argv[1:]
	if isGitName(argv[0].Lit) {
		return gitTargets(argv)
	}
	if lang := interpreterName(name); lang != "" {
		return inPlaceScriptTargets(lang, args)
	}
	switch name {
	case "tee":
		return targets(scanArgs(args, nil, noopOpt), "tee", nil)
	case "cp", "mv", "install":
		return copyTargets(name, args)
	case "rsync":
		ops := scanArgs(args, &rsyncSpec, noopOpt)
		if len(ops) < 2 {
			return nil
		}
		return targets(ops[len(ops)-1:], "rsync", nil)
	case "sed":
		inPlace, _, files := sedParse(args)
		if !inPlace {
			return nil
		}
		return targets(files, "sed -i", nil)
	case "rm":
		return targets(scanArgs(args, nil, noopOpt), "rm", nil)
	case "truncate":
		return targets(scanArgs(args, &truncateSpec, noopOpt), "truncate", nil)
	case "touch":
		return targets(scanArgs(args, &touchSpec, noopOpt), "touch", nil)
	case "dd":
		return ddTargets(args)
	case "ln":
		return lnTargets(args)
	}
	return nil
}

// ReadTargets derives read targets (CommandNode.Reads) from a node's argv for
// the frozen read-only command table; redirect targets are added by Parse.
// name is the command's basename and argv[0] the command word. psparse uses it
// for external executables.
func ReadTargets(name string, argv []model.Word) []model.WriteTarget {
	if len(argv) < 2 {
		return nil
	}
	args := argv[1:]
	switch name {
	case "cat":
		return targets(scanArgs(args, nil, noopOpt), name, nil)
	case "tac":
		return targets(scanArgs(args, &optSpec{short: "s", long: []string{"separator"}}, noopOpt), name, nil)
	case "nl":
		return targets(scanArgs(args, &nlSpec, noopOpt), name, nil)
	case "wc":
		return targets(scanArgs(args, &wcSpec, noopOpt), name, nil)
	case "file":
		return targets(scanArgs(args, &fileSpec, noopOpt), name, nil)
	case "less":
		return targets(scanArgs(args, &lessSpec, noopOpt), name, plusWord)
	case "more":
		return targets(scanArgs(args, &moreSpec, noopOpt), name, plusWord)
	case "head":
		return targets(scanArgs(args, &headSpec, noopOpt), name, plusNumber)
	case "tail":
		return targets(scanArgs(args, &tailSpec, noopOpt), name, plusNumber)
	case "sed":
		inPlace, _, files := sedParse(args)
		if inPlace {
			return nil
		}
		return targets(files, name, nil)
	case "grep", "egrep", "fgrep":
		return grepTargets(name, args, false)
	case "rg":
		return grepTargets(name, args, true)
	case "awk", "gawk", "mawk":
		return awkTargets(name, args)
	}
	return nil
}

func noopOpt(byte, string, *model.Word) {}

// plusWord skips less/more startup commands such as "+G" or "+/pattern".
func plusWord(s string) bool { return strings.HasPrefix(s, "+") }

// plusNumber skips obsolete head/tail "+N" line offsets.
func plusNumber(s string) bool { return len(s) > 1 && s[0] == '+' && isNumberWord(s[1:]) }

var (
	nlSpec = optSpec{short: "bdfhilnpsvw", long: []string{
		"body-numbering", "section-delimiter", "footer-numbering", "header-numbering",
		"line-increment", "join-blank-lines", "number-format", "number-separator",
		"starting-line-number", "number-width"}}
	wcSpec   = optSpec{long: []string{"files0-from"}}
	fileSpec = optSpec{short: "fmeP", long: []string{"files-from", "magic-file", "exclude", "parameter", "exclude-quiet"}}
	lessSpec = optSpec{short: "bhjkoOpPtTxyz#", long: []string{
		"buffers", "max-back-scroll", "jump-target", "pattern", "prompt", "tag", "tag-file", "tabs",
		"shift", "output-file", "log-file", "LOG-FILE", "lesskey-file", "lesskey-src"}}
	moreSpec = optSpec{short: "n", long: []string{"lines"}}
	headSpec = optSpec{short: "nc", long: []string{"lines", "bytes"}}
	tailSpec = optSpec{short: "ncs", long: []string{"lines", "bytes", "pid", "sleep-interval", "max-unchanged-stats"}}

	rsyncSpec = optSpec{short: "efBMT@", long: []string{
		"rsh", "exclude", "include", "exclude-from", "include-from", "files-from", "filter", "port",
		"bwlimit", "timeout", "contimeout", "log-file", "log-file-format", "partial-dir", "backup-dir",
		"suffix", "chmod", "chown", "usermap", "groupmap", "rsync-path", "max-size", "min-size",
		"compare-dest", "copy-dest", "link-dest", "block-size", "temp-dir", "iconv", "out-format",
		"password-file", "address", "sockopts", "write-batch", "only-write-batch", "read-batch",
		"max-delete", "modify-window", "checksum-seed", "protocol", "compress-level", "skip-compress",
		"checksum-choice", "compress-choice", "config", "info", "debug", "early-input", "outbuf",
		"stop-after", "stop-at", "time-limit", "remote-option", "bind", "preallocate"}}
	truncateSpec = optSpec{short: "sr", long: []string{"size", "reference"}}
	touchSpec    = optSpec{short: "dtr", long: []string{"date", "reference", "time"}}
	lnSpec       = optSpec{short: "tS", long: []string{"target-directory", "suffix"}}
	cpSpec       = optSpec{short: "tS", long: []string{"target-directory", "suffix"}}
	installSpec  = optSpec{short: "mogtS", long: []string{
		"mode", "owner", "group", "target-directory", "suffix", "strip-program", "context"}}

	grepSpec = optSpec{short: "ABCmdDef", long: []string{
		"max-count", "context", "after-context", "before-context", "include", "exclude", "exclude-dir",
		"label", "binary-files", "color", "colour", "devices", "directories", "regexp", "file", "exclude-from"}}
	rgSpec = optSpec{short: "ABCmdDefgtTjMEr", long: []string{
		"max-count", "context", "after-context", "before-context", "label", "binary-files", "color",
		"regexp", "file", "glob", "iglob", "type", "type-not", "threads", "max-columns", "encoding",
		"max-depth", "sort", "sortr", "replace", "pre", "type-add", "max-filesize", "context-separator",
		"ignore-file", "pre-glob", "colors", "engine", "path-separator", "regex-size-limit",
		"dfa-size-limit", "field-context-separator", "field-match-separator", "hyperlink-format"}}
	sedSpec = optSpec{short: "efl", optShort: "i", long: []string{"expression", "file", "line-length"}}
)

// copyTargets handles cp/mv/install: the destination is the -t directory, or
// the last operand; `install -d` creates every operand.
func copyTargets(name string, args []model.Word) []model.WriteTarget {
	sp := &cpSpec
	if name == "install" {
		sp = &installSpec
	}
	var tDir *model.Word
	dirMode := false
	ops := scanArgs(args, sp, func(short byte, long string, val *model.Word) {
		switch {
		case short == 't' || long == "target-directory":
			tDir = val
		case name == "install" && short == 'd':
			dirMode = true
		case name == "install" && long == "directory":
			dirMode = true
		}
	})
	switch {
	case tDir != nil:
		t, ok := target(*tDir, name)
		if !ok {
			return nil
		}
		return []model.WriteTarget{t}
	case dirMode:
		return targets(ops, name, nil)
	case len(ops) >= 2:
		return targets(ops[len(ops)-1:], name, nil)
	}
	return nil
}

func ddTargets(args []model.Word) []model.WriteTarget {
	var out []model.WriteTarget
	for _, a := range args {
		if !strings.HasPrefix(a.Lit, "of=") {
			continue
		}
		w := model.Word{Lit: a.Lit[3:], Dynamic: a.Dynamic}
		if t, ok := target(w, "dd"); ok {
			out = append(out, t)
		}
	}
	return out
}

// lnTargets: the link is the last operand, the -t directory, or (single
// operand) a file named like the target in the current directory.
func lnTargets(args []model.Word) []model.WriteTarget {
	var tDir *model.Word
	ops := scanArgs(args, &lnSpec, func(short byte, long string, val *model.Word) {
		if short == 't' || long == "target-directory" {
			tDir = val
		}
	})
	switch {
	case tDir != nil:
		if t, ok := target(*tDir, "ln"); ok {
			return []model.WriteTarget{t}
		}
	case len(ops) >= 2:
		return targets(ops[len(ops)-1:], "ln", nil)
	case len(ops) == 1:
		w := ops[0]
		if !w.Dynamic {
			w.Lit = strings.TrimRight(w.Lit, "/")
			if i := strings.LastIndexByte(w.Lit, '/'); i >= 0 {
				w.Lit = w.Lit[i+1:]
			}
		}
		if t, ok := target(w, "ln"); ok {
			return []model.WriteTarget{t}
		}
	}
	return nil
}

// sedParse splits sed arguments into whether editing is in place, the script
// operands consumed, and the file operands.
func sedParse(args []model.Word) (inPlace, scriptGiven bool, files []model.Word) {
	bsdSuffix := false
	ops := scanArgs(args, &sedSpec, func(short byte, long string, val *model.Word) {
		switch {
		case short == 'i':
			inPlace = true
			bsdSuffix = val == nil
		case long == "in-place":
			inPlace = true
		case short == 'e' || short == 'f' || long == "expression" || long == "file":
			scriptGiven = true
		}
	})
	if inPlace && bsdSuffix && len(ops) > 0 && !ops[0].Dynamic && ops[0].Lit == "" {
		ops = ops[1:] // BSD/macOS `sed -i '' script file`
	}
	if !scriptGiven && len(ops) > 0 {
		ops = ops[1:]
	}
	return inPlace, scriptGiven, ops
}

func grepTargets(name string, args []model.Word, rg bool) []model.WriteTarget {
	sp := &grepSpec
	if rg {
		sp = &rgSpec
	}
	havePattern, filesMode := false, false
	ops := scanArgs(args, sp, func(short byte, long string, val *model.Word) {
		switch {
		case short == 'e' || short == 'f' || long == "regexp" || long == "file":
			havePattern = true
		case rg && long == "files":
			filesMode = true
		}
	})
	if !havePattern && !filesMode && len(ops) > 0 {
		ops = ops[1:]
	}
	return targets(ops, name, nil)
}

func awkTargets(name string, args []model.Word) []model.WriteTarget {
	short := "Fvf"
	var long []string
	switch name {
	case "gawk":
		short = "FvfeiElW"
		long = []string{"field-separator", "assign", "file", "source", "include", "load", "exec"}
	case "mawk":
		short = "FvfW"
	}
	haveProgram := false
	ops := scanArgs(args, &optSpec{short: short, long: long}, func(s byte, l string, val *model.Word) {
		switch {
		case s == 'f' || l == "file":
			haveProgram = true
		case name == "gawk" && (s == 'e' || s == 'E' || l == "source" || l == "exec"):
			haveProgram = true
		}
	})
	if !haveProgram && len(ops) > 0 {
		ops = ops[1:]
	}
	files := make([]model.Word, 0, len(ops))
	for _, o := range ops {
		if !isAssignWord(o) { // NAME=value operands are assignments, not files
			files = append(files, o)
		}
	}
	return targets(files, name, nil)
}

// interpreterName maps perl and ruby (also versioned names such as perl5.38
// or ruby3.2) to "perl" / "ruby"; other commands give "".
func interpreterName(name string) string {
	for _, lang := range []string{"perl", "ruby"} {
		rest, ok := strings.CutPrefix(name, lang)
		if !ok {
			continue
		}
		for _, c := range rest {
			if (c < '0' || c > '9') && c != '.' {
				return ""
			}
		}
		return lang
	}
	return ""
}

// inPlaceScriptTargets handles `perl -i[ext] ... FILE...` and
// `ruby -i[ext] ... FILE...`: with -i every file operand is edited in place.
// Switches are read like the interpreters do: up to the first operand (or
// "--"); -e/-E take the script (attached or next word); without -e the first
// operand is the script file. A script that writes by itself (perl -e
// 'open ...') is not recognized, only the -i switch.
func inPlaceScriptTargets(lang string, args []model.Word) []model.WriteTarget {
	// switches whose value is the rest of the cluster, or the next word
	// when nothing follows; and those whose value is only ever attached.
	valued, attached := "eEI", "iMmFxCdDV"
	if lang == "ruby" {
		valued, attached = "eIrCXE", "iKFxWT"
	}
	inPlace, scriptGiven := false, false
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		s := a.Lit
		if a.Dynamic || len(s) < 2 || s[0] != '-' {
			break
		}
		if s == "--" {
			i++
			break
		}
		if s[1] == '-' {
			if lang == "ruby" && !strings.Contains(s, "=") {
				switch s {
				case "--encoding", "--external-encoding", "--internal-encoding":
					i++ // takes the next word
				}
			}
			continue
		}
		for j := 1; j < len(s); j++ {
			c := s[j]
			switch {
			case c == 'i':
				inPlace = true
				j = len(s)
			case strings.IndexByte(valued, c) >= 0:
				if c == 'e' || (c == 'E' && lang == "perl") {
					scriptGiven = true
				}
				if j+1 >= len(s) {
					i++ // the value is the next word
				}
				j = len(s)
			case strings.IndexByte(attached, c) >= 0:
				j = len(s)
			case c == 'l' || c == '0':
				for j+1 < len(s) && s[j+1] >= '0' && s[j+1] <= '9' {
					j++
				}
			}
		}
	}
	if !inPlace || i > len(args) {
		return nil
	}
	ops := args[i:]
	if !scriptGiven && len(ops) > 0 {
		ops = ops[1:] // the script file
	}
	return targets(ops, lang+" -i", nil)
}

// restoreSpec lists the git restore options that take a value.
var restoreSpec = optSpec{short: "sU", long: []string{
	"source", "pathspec-from-file", "unified", "inter-hunk-context", "conflict"}}

// gitTargets handles the git subcommands that overwrite working-tree files
// from a tree-ish: `git checkout [<rev>] -- <path>...` and
// `git restore [options] <path>...` (also with --staged: the path is still
// the subject of the write). `git checkout <path>` without "--" is
// indistinguishable from a branch switch and is not recognized.
func gitTargets(argv []model.Word) []model.WriteTarget {
	g := ParseGit(argv)
	if g == nil || g.SubDynamic {
		return nil
	}
	var paths []model.Word
	switch g.Sub {
	case "checkout":
		for i, a := range g.Args {
			if !a.Dynamic && a.Lit == "--" {
				paths = g.Args[i+1:]
				break
			}
		}
	case "restore":
		paths = scanArgs(g.Args, &restoreSpec, noopOpt)
	default:
		return nil
	}
	var out []model.WriteTarget
	for _, p := range paths {
		if t, ok := gitPath(g, p, "git "+g.Sub); ok {
			out = append(out, t)
		}
	}
	return out
}

// gitPath resolves a pathspec against `git -C <dir>` the way git does.
func gitPath(g *model.GitInvocation, w model.Word, via string) (model.WriteTarget, bool) {
	if w.Dynamic || g.CwdDynamic {
		return model.WriteTarget{Dynamic: true, Via: via}, true
	}
	if w.Lit == "" {
		return model.WriteTarget{}, false
	}
	if g.Cwd != "" && !model.IsAbsShellPath(w.Lit) {
		sep := "/"
		if strings.HasSuffix(g.Cwd, "/") || strings.HasSuffix(g.Cwd, `\`) {
			sep = ""
		}
		w.Lit = g.Cwd + sep + w.Lit
	}
	return model.WriteTarget{Path: w.Lit, Via: via}, true
}
