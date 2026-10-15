package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/project"
	"github.com/libingtong/ballast/internal/sys"
)

func runInit(a *app, c *command, args []string) int {
	fs := newFlagSet("init")
	var yes, force, dry bool
	fs.BoolVar(&yes, "yes", false, "")
	fs.BoolVar(&force, "force", false, "")
	fs.BoolVar(&dry, "dry-run", false, "")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return a.usageErr(c, "%v", err)
	}
	if len(pos) > 0 {
		return a.usageErr(c, "unexpected argument %q", pos[0])
	}
	root, err := a.projectRoot()
	if err != nil {
		return a.errorf(ExitInfra, "init: %v", err)
	}
	target := layout.ProjectJSON(root)
	exists := a.fileExists(target)
	if exists && !force {
		fmt.Fprintf(a.errw, "ballast init: %s already exists (use --force to overwrite)\n", target)
		return ExitParse
	}
	sugg, err := project.Detect(a.fs, root)
	if err != nil {
		return a.errorf(ExitInfra, "init: %v", err)
	}
	if len(sugg) == 0 {
		fmt.Fprintf(a.errw, "ballast init: no supported stack detected in %s; write .ballast/project.json by hand (see docs)\n", root)
		return ExitFindings
	}
	cfg := project.Propose(sugg)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return a.errorf(ExitInfra, "init: %v", err)
	}
	data = append(data, '\n')
	// Make sure what we are about to write passes the loader's validation.
	if _, derr := project.Decode(data); derr != nil {
		return a.errorf(ExitInfra, "init: generated project.json is invalid: %v", derr)
	}

	fmt.Fprintf(a.out, "检测到的技术栈:")
	for _, s := range sugg {
		fmt.Fprintf(a.out, " %s(%s)", s.Stack, strings.Join(s.Markers, ","))
	}
	fmt.Fprintf(a.out, "\n将写入 %s:\n%s", target, data)
	if dry {
		return ExitOK
	}
	if exists {
		if old, rerr := a.fs.ReadFile(target); rerr == nil && string(old) == string(data) {
			fmt.Fprintln(a.out, "project.json 已是最新,无需写入")
			return ExitOK
		}
	}
	if !yes && !a.confirm("写入? [y/N] ") {
		fmt.Fprintln(a.errw, "ballast init: cancelled")
		return ExitFindings
	}
	if err := sys.WriteFileAtomic(a.fs, a.ids, target, data, 0o644, false); err != nil {
		return a.errorf(ExitInfra, "init: %v", err)
	}
	fmt.Fprintf(a.out, "已写入 %s\n", target)
	return ExitOK
}

// confirm asks a yes/no question on stdin (default no).
func (a *app) confirm(prompt string) bool {
	fmt.Fprint(a.errw, prompt)
	if a.in == nil {
		return false
	}
	line, _ := bufio.NewReader(a.in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
