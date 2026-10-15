//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type auditCtx struct {
	in          *Input
	rs          *Ruleset
	hits        []Hit
	dec         Decision
	ctx         *evalCtx
	tx          txResult
	corruptRead bool
	cache       string
	ph          phases
}

type auditRec struct {
	TS            string           `json:"ts"`
	V             int              `json:"v"`
	Type          string           `json:"type"`
	EventSeq      int              `json:"event_seq,omitempty"`
	SessionID     string           `json:"session_id"`
	AgentID       string           `json:"agent_id,omitempty"`
	ToolUseID     string           `json:"tool_use_id,omitempty"`
	RepoInst      string           `json:"repo_inst"`
	Event         string           `json:"event"`
	Tool          string           `json:"tool,omitempty"`
	RuleID        string           `json:"rule_id,omitempty"`
	Generation    string           `json:"generation"`
	Action        string           `json:"action,omitempty"`
	Decision      string           `json:"decision,omitempty"`
	Matched       string           `json:"matched,omitempty"`
	LatencyUS     int64            `json:"latency_us,omitempty"`
	Phases        phases           `json:"phases_us,omitempty"`
	Cache         string           `json:"cache,omitempty"`
	State         string           `json:"state,omitempty"`
	LockWaitUS    int64            `json:"lock_wait_us,omitempty"`
	Corrupt       bool             `json:"state_corrupt,omitempty"`
	Unknown       int              `json:"unknown,omitempty"`
	SnapshotUS    int64            `json:"snapshot_us,omitempty"`
	SnapshotSteps map[string]int64 `json:"snapshot_steps_us,omitempty"`
	Version       string           `json:"ballast_version"`
}

// writeAudit 把本次的 hit 行与 eval_summary 行拼成一个缓冲区,单次 write 追加(O_APPEND)。
func writeAudit(home string, a *auditCtx, elapsed time.Duration) error {
	now := time.Now().UTC()
	base := auditRec{TS: now.Format(time.RFC3339Nano), V: 1, SessionID: a.in.SessionID, AgentID: a.in.AgentID,
		ToolUseID: a.in.ToolUseID, RepoInst: repoInst(a.in.Cwd), Event: a.in.HookEventName, Tool: a.in.ToolName,
		Generation: a.rs.Hash, EventSeq: a.tx.NEvents, Version: "m0c"}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, h := range a.hits {
		r := base
		r.Type = "hit"
		r.RuleID = h.Rule.ID
		r.Action = h.Rule.Action
		r.Decision = a.dec.Action
		r.Matched = h.Matched
		if err := enc.Encode(&r); err != nil {
			return err
		}
	}
	s := base
	s.Type = "eval_summary"
	s.Decision = a.dec.Action
	s.LatencyUS = elapsed.Microseconds()
	s.Phases = a.ph
	s.Cache = a.cache
	s.State = a.tx.Status
	s.LockWaitUS = a.tx.WaitUS
	s.Corrupt = a.corruptRead || a.tx.Corrupt
	s.Unknown = a.ctx.unknown
	if a.ctx.snapRan {
		s.SnapshotUS = a.ctx.snapMS
		st := a.ctx.snapStep
		s.SnapshotSteps = map[string]int64{"locate": st.Locate.Microseconds(), "copy": st.Copy.Microseconds(),
			"add_u": st.AddU.Microseconds(), "rm": st.Rm.Microseconds(), "write_tree": st.WriteTree.Microseconds(), "native": st.Native.Microseconds()}
	}
	if err := enc.Encode(&s); err != nil {
		return err
	}
	path := filepath.Join(home, "audit", now.Format("2006-01")+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		}
	}
	if err != nil {
		return err
	}
	n, werr := f.Write(buf.Bytes())
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if n != buf.Len() {
		return fmt.Errorf("short audit write %d/%d", n, buf.Len())
	}
	return cerr
}
