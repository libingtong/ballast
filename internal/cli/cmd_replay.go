package cli

import (
	"errors"
	"fmt"

	"github.com/libingtong/ballast/internal/bashparse"
	"github.com/libingtong/ballast/internal/psparse"
	"github.com/libingtong/ballast/internal/replay"
)

func runReplay(a *app, c *command, args []string) int {
	fs := newFlagSet("replay")
	var client, evidenceArg, initial, rules, format string
	var subagents bool
	fs.StringVar(&client, "client", "", "")
	fs.BoolVar(&subagents, "subagents", false, "")
	fs.StringVar(&evidenceArg, "evidence", "", "")
	fs.StringVar(&initial, "initial-state", "", "")
	fs.StringVar(&rules, "rules", "", "")
	fs.StringVar(&format, "format", "md", "")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return a.usageErr(c, "%v", err)
	}
	if len(pos) != 1 {
		return a.usageErr(c, "replay needs exactly one transcript path")
	}
	if format != "json" && format != "md" {
		return a.usageErr(c, "--format must be json or md, got %q", format)
	}
	opt := replay.Options{
		Transcript:   pos[0],
		Subagents:    subagents,
		InitialState: initial,
		RulesPath:    rules,
		Format:       format,
	}
	if client != "" {
		cl, err := parseClient(client)
		if err != nil {
			return a.usageErr(c, "%v", err)
		}
		opt.Client = cl
	}
	switch evidenceArg {
	case "", "auto":
		opt.EvidenceMode = "auto"
	default:
		opt.EvidenceMode, opt.EvidenceFile = "file", evidenceArg
	}
	home, _ := a.env.HomeDir()
	rep, err := replay.Run(opt, replay.Deps{
		FS:          a.fs,
		Clock:       a.clock,
		Adapters:    a.adapters,
		Compiler:    a.engine,
		Engine:      a.engine,
		Rules:       a.loader(true, a.errw),
		Bash:        bashparse.Parser{},
		PS:          psparse.Parser{},
		Paths:       a.pathSvc,
		EvidenceDir: a.lay.EvidenceDir,
		LabelsDir:   a.lay.LabelsDir,
		Home:        home,
	})
	if err != nil {
		if errors.Is(err, replay.ErrUsage) {
			return a.errorf(ExitUsage, "%v", err)
		}
		return a.errorf(ExitInfra, "%v", err)
	}
	if format == "json" {
		if err := a.writeJSON(rep); err != nil {
			return a.errorf(ExitInfra, "%v", err)
		}
		return ExitOK
	}
	if _, err := a.out.Write(replay.RenderMarkdown(rep)); err != nil {
		return a.errorf(ExitInfra, "%v", err)
	}
	if rep.SkippedLines > 0 {
		fmt.Fprintf(a.errw, "ballast replay: 跳过 %d 行无法解析的 transcript 内容\n", rep.SkippedLines)
	}
	return ExitOK
}
