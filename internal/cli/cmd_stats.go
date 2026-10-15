package cli

import (
	"errors"
	"flag"
	"fmt"

	"github.com/libingtong/ballast/internal/audit"
	"github.com/libingtong/ballast/internal/dsl"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/stats"
)

func runStats(a *app, c *command, args []string) int {
	fs := newFlagSet("stats")
	var since, by, rule string
	var sample int
	var asJSON bool
	fs.StringVar(&since, "since", "", "")
	fs.StringVar(&by, "by", "", "")
	fs.StringVar(&rule, "rule", "", "")
	fs.IntVar(&sample, "sample", 0, "")
	fs.BoolVar(&asJSON, "json", false, "")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return a.usageErr(c, "%v", err)
	}
	if len(pos) > 0 {
		return a.usageErr(c, "unexpected argument %q", pos[0])
	}
	sampleSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "sample" {
			sampleSet = true
		}
	})
	if sampleSet && rule == "" {
		return a.usageErr(c, "--sample needs --rule <id>")
	}
	if sampleSet && sample <= 0 {
		return a.usageErr(c, "--sample must be positive")
	}
	src := a.auditReader()

	if sampleSet {
		rev, err := stats.Sample(src, a.lay, a.fs, a.ids, a.clock, rule, sample)
		if err != nil {
			return a.statsErr(err)
		}
		if asJSON {
			if err := a.writeJSON(rev); err != nil {
				return a.errorf(ExitInfra, "%v", err)
			}
			return ExitOK
		}
		fmt.Fprintf(a.out, "规则 %s:抽样 %d 条命中,复核凭据 %s\n", rev.RuleID, len(rev.Samples), stats.ReviewPath(a.lay, rev.RuleID))
		for _, s := range rev.Samples {
			r := s.Record
			fmt.Fprintf(a.out, "  %s  %s  session=%s tool=%s decision=%s matched=%q\n",
				s.ID, r.TS.UTC().Format("2006-01-02T15:04:05Z"), r.SessionID, r.Tool, r.Decision, r.Matched)
		}
		return ExitOK
	}

	now := a.clock.Now()
	from, err := stats.ParseSince(since, now)
	if err != nil {
		return a.statsErr(err)
	}
	q := stats.Query{Since: from, By: stats.GroupBy(by), Rule: rule, Current: a.currentRuleInfo()}
	rows, corrupt, err := stats.ComputeDetailed(src, q)
	if err != nil {
		return a.statsErr(err)
	}
	if corrupt > 0 {
		fmt.Fprintf(a.errw, "ballast stats: 跳过 %d 条损坏的审计行(运行 ballast audit lint 查看)\n", corrupt)
	}
	if asJSON {
		if rows == nil {
			rows = []stats.Row{}
		}
		if err := a.writeJSON(rows); err != nil {
			return a.errorf(ExitInfra, "%v", err)
		}
		return ExitOK
	}
	_, _ = a.out.Write(stats.RenderTable(rows, q.By))
	return ExitOK
}

func (a *app) statsErr(err error) int {
	if errors.Is(err, stats.ErrUsage) {
		return a.errorf(ExitUsage, "%v", err)
	}
	return a.errorf(ExitInfra, "%v", err)
}

// currentRuleInfo returns the configured action/target of the rules in
// effect (best effort: stats still works when the rules cannot be loaded).
func (a *app) currentRuleInfo() map[string]stats.RuleInfo {
	req, err := a.loadRequest(model.ClientClaude)
	if err != nil {
		return nil
	}
	rs, err := a.loader(true, nil).Load(req)
	if err != nil || rs == nil {
		return nil
	}
	out := map[string]stats.RuleInfo{}
	for _, r := range rs.Rules {
		out[r.Rule.ID] = stats.RuleInfo{Action: r.Action, Target: r.Target}
	}
	return out
}

func runRules(a *app, c *command, args []string) int {
	if len(args) == 0 || args[0] != "promote" {
		return a.usageErr(c, "expected the subcommand \"promote\"")
	}
	fs := newFlagSet("rules promote")
	var to string
	var force bool
	fs.StringVar(&to, "to", "", "")
	fs.BoolVar(&force, "force", false, "")
	pos, err := parseArgs(fs, args[1:])
	if err != nil {
		return a.usageErr(c, "%v", err)
	}
	if len(pos) != 1 {
		return a.usageErr(c, "promote needs exactly one rule id")
	}
	var action model.Action
	switch to {
	case "warn", "ask", "deny":
		action = model.Action(to)
	default:
		return a.usageErr(c, "--to must be warn, ask or deny")
	}
	clients, err := a.manifestClients()
	if err != nil {
		return a.errorf(ExitInfra, "rules promote: %v", err)
	}
	root, err := a.projectRoot()
	if err != nil {
		return a.errorf(ExitInfra, "rules promote: %v", err)
	}
	res, err := stats.Promote(stats.PromoteOptions{RuleID: pos[0], To: action, Force: force, Clients: clients}, stats.PromoteDeps{
		Layout:            a.lay,
		FS:                a.fs,
		Clock:             a.clock,
		IDs:               a.ids,
		Audit:             a.auditWriter(),
		Source:            a.auditReader(),
		Compiler:          a.engine,
		CheckCapabilities: dsl.CheckCapabilities,
		RepoRoot:          root,
	})
	if err != nil {
		switch {
		case errors.Is(err, stats.ErrUsage):
			return a.errorf(ExitUsage, "%v", err)
		case errors.Is(err, stats.ErrGate):
			return a.errorf(ExitFindings, "%v (review the hits first: ballast stats --rule %s --sample 20; or use --force)", err, pos[0])
		case errors.Is(err, stats.ErrRuleNotFound), errors.Is(err, stats.ErrOrgRule), errors.Is(err, stats.ErrCapability):
			return a.errorf(ExitRefused, "%v", err)
		}
		return a.errorf(ExitInfra, "%v", err)
	}
	forced := ""
	if res.Forced {
		forced = "(--force)"
	}
	fmt.Fprintf(a.out, "规则 %s:%s → %s %s\n  文件 %s\n", pos[0], res.From, res.To, forced, res.File)
	return ExitOK
}

func runAudit(a *app, c *command, args []string) int {
	if len(args) == 0 || args[0] != "lint" {
		return a.usageErr(c, "expected the subcommand \"lint\"")
	}
	fs := newFlagSet("audit lint")
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "")
	pos, err := parseArgs(fs, args[1:])
	if err != nil {
		return a.usageErr(c, "%v", err)
	}
	if len(pos) > 0 {
		return a.usageErr(c, "unexpected argument %q", pos[0])
	}
	findings, st, err := audit.Lint(a.auditReader())
	if err != nil {
		return a.errorf(ExitInfra, "audit lint: %v", err)
	}
	if asJSON {
		type row struct {
			File    string `json:"file"`
			Line    int    `json:"line"`
			Message string `json:"message"`
		}
		rows := make([]row, 0, len(findings))
		for _, f := range findings {
			rows = append(rows, row{f.Loc.File, f.Loc.Line, f.Message})
		}
		if err := a.writeJSON(map[string]any{"findings": rows, "files": st.Files, "lines": st.Lines, "corrupt": st.Corrupt}); err != nil {
			return a.errorf(ExitInfra, "%v", err)
		}
	} else {
		for _, f := range findings {
			fmt.Fprintln(a.out, f.String())
		}
		fmt.Fprintf(a.out, "audit lint: %d 个文件,%d 行,%d 个问题\n", st.Files, st.Lines, len(findings))
	}
	if len(findings) > 0 {
		return ExitFindings
	}
	return ExitOK
}
