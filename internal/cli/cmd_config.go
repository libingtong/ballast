package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/libingtong/ballast/internal/model"
)

func runConfig(a *app, c *command, args []string) int {
	if len(args) == 0 || args[0] != "show" {
		return a.usageErr(c, "expected the subcommand \"show\"")
	}
	fs := newFlagSet("config show")
	var explain, asJSON bool
	fs.BoolVar(&explain, "explain", false, "")
	fs.BoolVar(&asJSON, "json", false, "")
	pos, err := parseArgs(fs, args[1:])
	if err != nil {
		return a.usageErr(c, "%v", err)
	}
	if len(pos) > 0 {
		return a.usageErr(c, "unexpected argument %q", pos[0])
	}
	req, err := a.loadRequest(model.ClientClaude)
	if err != nil {
		return a.errorf(ExitInfra, "%v", err)
	}
	loader := a.loader(true, a.errw)

	type ruleRow struct {
		ID     string          `json:"id"`
		Layer  model.Layer     `json:"layer"`
		Source string          `json:"source"`
		Event  model.EventName `json:"event"`
		Action model.Action    `json:"action"`
		Target model.Action    `json:"target"`
		Fate   string          `json:"fate,omitempty"`
		Note   string          `json:"note,omitempty"`
	}
	out := struct {
		Generation string               `json:"generation"`
		Sources    []model.LayerSource  `json:"sources"`
		Settings   model.Settings       `json:"settings"`
		Rules      []ruleRow            `json:"rules"`
		Notices    []model.ConfigNotice `json:"notices,omitempty"`
	}{}

	if explain {
		rep, err := loader.Explain(req)
		if err != nil {
			return a.errorf(ExitInfra, "%v", err)
		}
		out.Generation = rep.Generation
		out.Sources = rep.Sources
		out.Notices = rep.Notices
		for _, r := range rep.Rules {
			out.Rules = append(out.Rules, ruleRow{ID: r.ID, Layer: r.Layer, Source: r.Source, Event: r.Event,
				Action: r.Action, Target: r.Target, Fate: string(r.Fate), Note: r.Note})
		}
		rs, err := loader.Load(req)
		if err == nil {
			out.Settings = rs.Settings
		}
	} else {
		rs, err := loader.Load(req)
		if err != nil {
			return a.errorf(ExitInfra, "%v", err)
		}
		out.Generation = rs.Generation
		out.Sources = rs.Sources
		out.Settings = rs.Settings
		out.Notices = rs.Notices
		for _, r := range rs.Rules {
			out.Rules = append(out.Rules, ruleRow{ID: r.Rule.ID, Layer: r.Layer, Source: r.Source, Event: r.Rule.Event,
				Action: r.Action, Target: r.Target})
		}
	}
	if out.Rules == nil {
		out.Rules = []ruleRow{}
	}

	bad := false
	for _, s := range out.Sources {
		if s.Status == model.LayerInvalid || s.Status == model.LayerLKG {
			bad = true
		}
	}

	if asJSON {
		if err := a.writeJSON(out); err != nil {
			return a.errorf(ExitInfra, "%v", err)
		}
	} else {
		fmt.Fprintf(a.out, "generation: %s\n\n", out.Generation)
		tw := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "层\t状态\t文件")
		for _, s := range out.Sources {
			line := fmt.Sprintf("%s\t%s\t%s", s.Layer, s.Status, s.Path)
			if s.Error != "" {
				line += "  (" + oneLine(s.Error) + ")"
			}
			fmt.Fprintln(tw, line)
		}
		_ = tw.Flush()
		fmt.Fprintf(a.out, "\n规则(%d):\n", len(out.Rules))
		tw = tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
		if explain {
			fmt.Fprintln(tw, "id\t层\t事件\taction\ttarget\t结局\t说明")
		} else {
			fmt.Fprintln(tw, "id\t层\t事件\taction\ttarget")
		}
		for _, r := range out.Rules {
			if explain {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Layer, r.Event, dash(string(r.Action)), dash(string(r.Target)), r.Fate, r.Note)
			} else {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.ID, r.Layer, r.Event, dash(string(r.Action)), dash(string(r.Target)))
			}
		}
		_ = tw.Flush()
		if in := out.Settings.Inject; in != nil {
			fmt.Fprintln(a.out, "\n设置 inject:")
			if in.AnchorEvery != nil {
				fmt.Fprintf(a.out, "  anchor_every = %d\n", *in.AnchorEvery)
			}
			if in.BatchAfterTools != nil {
				fmt.Fprintf(a.out, "  batch_after_tools = %d\n", *in.BatchAfterTools)
			}
		}
		if len(out.Notices) > 0 {
			fmt.Fprintln(a.out, "\n通知:")
			for _, n := range out.Notices {
				fmt.Fprintf(a.out, "  [%s] %s\n", n.Kind, n.Message)
			}
		}
	}
	if bad {
		return ExitFindings
	}
	return ExitOK
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
