package main

import (
	"encoding/json"
	"strings"
)

type phases map[string]int64

type Decision struct {
	Action string // 命中的最严档位,无则 ""
	Reason string
	Output []byte
}

var severity = map[string]int{"log": 1, "warn": 2, "ask": 3, "deny": 4}

func parseInput(raw []byte) (*Input, error) {
	in := &Input{}
	if err := json.Unmarshal(raw, in); err != nil {
		return nil, err
	}
	return in, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// decide 按"事件 × 动作矩阵"形成唯一 Decision。无决策时输出为空,绝不输出 allow。
func decide(in *Input, rs *Ruleset, hits []Hit) Decision {
	var d Decision
	var msgs []string
	for _, h := range hits {
		if severity[h.Rule.Action] > severity[d.Action] {
			d.Action = h.Rule.Action
		}
	}
	for _, h := range hits {
		if h.Rule.Action == d.Action && h.Rule.Message != "" {
			msgs = append(msgs, "["+h.Rule.ID+"] "+h.Rule.Message)
		}
	}
	d.Reason = strings.Join(msgs, "\n")

	hso := func(extra map[string]any) []byte {
		m := map[string]any{"hookEventName": in.HookEventName}
		for k, v := range extra {
			m[k] = v
		}
		b, _ := json.Marshal(map[string]any{"hookSpecificOutput": m})
		return b
	}
	switch in.HookEventName {
	case "SessionStart":
		var sb strings.Builder
		for _, r := range rs.Rules {
			if r.Inject != "" {
				sb.WriteString("- " + r.ID + ": " + r.Inject + "\n")
			}
		}
		d.Output = hso(map[string]any{"additionalContext": clip(sb.String(), 8192)})
	case "UserPromptSubmit":
		if d.Action == "deny" {
			d.Output, _ = json.Marshal(map[string]any{"decision": "block", "reason": d.Reason})
			break
		}
		var sb strings.Builder
		for _, r := range rs.Rules {
			if r.Summary != "" {
				sb.WriteString("- " + r.ID + ": " + r.Summary + "\n")
			}
		}
		anchor := clip(sb.String(), 1200)
		if d.Action == "warn" {
			anchor += d.Reason
		}
		d.Output = hso(map[string]any{"additionalContext": anchor})
	case "PreToolUse":
		switch d.Action {
		case "deny", "ask":
			d.Output = hso(map[string]any{"permissionDecision": d.Action, "permissionDecisionReason": d.Reason})
		case "warn":
			d.Output = hso(map[string]any{"additionalContext": d.Reason})
		}
	case "PostToolUse", "PostToolUseFailure":
		if d.Action == "warn" {
			d.Output = hso(map[string]any{"additionalContext": d.Reason})
		}
	case "Stop", "SubagentStop":
		switch d.Action {
		case "deny":
			d.Output, _ = json.Marshal(map[string]any{"decision": "block", "reason": d.Reason})
		case "warn":
			d.Output, _ = json.Marshal(map[string]any{"systemMessage": d.Reason})
		}
	}
	return d
}

func buildDelta(ctx *evalCtx, hits []Hit) *Delta {
	in := ctx.in
	d := &Delta{ToolUseID: in.ToolUseID, Gen: ctx.rs.Hash}
	d.Ev = RingEv{Ev: in.HookEventName, Tool: in.ToolName}
	d.IsTool = in.HookEventName == "PreToolUse"
	for _, h := range hits {
		d.Hits = append(d.Hits, h.Rule.ID)
	}
	if len(hits) > 0 {
		d.Ev.Sum = hits[0].Rule.ID
	}
	switch in.HookEventName {
	case "UserPromptSubmit":
		d.LastPrompt = tail(in.Prompt, 200)
	case "PostToolUse":
		switch in.ToolName {
		case "Read", "Edit", "Write", "MultiEdit":
			d.ReadPaths = ctx.targetPaths()
		}
	}
	return d
}
