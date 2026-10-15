package replay

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// SampleID computes the stable sample id:
// hex(sha256(transcript_ref + "\x00" + uuid + "\x00" + rule_id + "\x00" + rules_hash))[:16].
func SampleID(transcriptRef, uuid, ruleID, rulesHash string) string {
	h := sha256.New()
	h.Write([]byte(transcriptRef))
	h.Write([]byte{0})
	h.Write([]byte(uuid))
	h.Write([]byte{0})
	h.Write([]byte(ruleID))
	h.Write([]byte{0})
	h.Write([]byte(rulesHash))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// report assembles the Report and attaches the human labels.
func (r *runner) report() *Report {
	rep := &Report{
		V:            ReportVersion,
		Transcript:   r.transcriptRef,
		Client:       r.client,
		RulesHash:    r.rulesHash,
		Items:        r.items,
		SkippedLines: r.skipped,
		Rules:        []RuleSummary{},
		Samples:      r.samples,
	}
	if rep.Samples == nil {
		rep.Samples = []Sample{}
	}
	for _, id := range r.order {
		rep.Rules = append(rep.Rules, *r.summaries[id])
	}

	labels := map[string]map[string]string{}
	for i := range rep.Samples {
		s := &rep.Samples[i]
		l, ok := labels[s.RuleID]
		if !ok {
			l = r.readLabels(s.RuleID)
			labels[s.RuleID] = l
		}
		s.Label = l[s.ID]
	}
	// Precision counts labeled known-hit samples only.
	type tally struct{ tp, fp int }
	tallies := map[string]*tally{}
	for i := range rep.Samples {
		s := &rep.Samples[i]
		if s.Class != KnownHit || s.Label == "" {
			continue
		}
		t := tallies[s.RuleID]
		if t == nil {
			t = &tally{}
			tallies[s.RuleID] = t
		}
		if s.Label == "tp" {
			t.tp++
		} else {
			t.fp++
		}
	}
	for i := range rep.Rules {
		t := tallies[rep.Rules[i].RuleID]
		if t == nil || t.tp+t.fp == 0 {
			continue
		}
		rep.Rules[i].Labeled = t.tp + t.fp
		p := float64(t.tp) / float64(t.tp+t.fp)
		rep.Rules[i].Precision = &p
	}
	return rep
}

type labelLine struct {
	SampleID string `json:"sample_id"`
	Label    string `json:"label"`
}

// readLabels loads <LabelsDir>/<rule_id>.jsonl: later lines override earlier
// ones, corrupt lines and labels other than tp/fp are ignored.
func (r *runner) readLabels(ruleID string) map[string]string {
	out := map[string]string{}
	if r.deps.LabelsDir == "" || ruleID == "" || strings.ContainsAny(ruleID, `/\`) || ruleID == ".." {
		return out
	}
	data, err := r.deps.FS.ReadFile(filepath.Join(r.deps.LabelsDir, ruleID+".jsonl"))
	if err != nil {
		return out
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var l labelLine
		if json.Unmarshal(bytes.TrimSpace(sc.Bytes()), &l) != nil || l.SampleID == "" {
			continue
		}
		if l.Label == "tp" || l.Label == "fp" {
			out[l.SampleID] = l.Label
		}
	}
	return out
}

// RenderMarkdown renders a report as Markdown.
func RenderMarkdown(r *Report) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Ballast replay report\n\n")
	fmt.Fprintf(&b, "- Transcript: `%s`\n", r.Transcript)
	fmt.Fprintf(&b, "- Client: %s\n", r.Client)
	fmt.Fprintf(&b, "- Rules hash: `%s`\n", r.RulesHash)
	fmt.Fprintf(&b, "- Items: %d (skipped lines: %d)\n\n", r.Items, r.SkippedLines)

	fmt.Fprintf(&b, "## Summary\n\n")
	if len(r.Rules) == 0 {
		fmt.Fprintf(&b, "_No rules._\n\n")
	} else {
		fmt.Fprintf(&b, "| rule | known-hit | known-miss | approximate | unknown | labeled | precision |\n")
		fmt.Fprintf(&b, "|---|---:|---:|---:|---:|---:|---:|\n")
		for _, s := range r.Rules {
			prec := "-"
			if s.Precision != nil {
				prec = fmt.Sprintf("%.1f%%", *s.Precision*100)
			}
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %s |\n", cell(s.RuleID),
				s.Counts[KnownHit], s.Counts[KnownMiss], s.Counts[Approximate], s.Counts[Unknown], s.Labeled, prec)
		}
		b.WriteByte('\n')
	}

	fmt.Fprintf(&b, "## Samples\n\n")
	any := false
	for _, rule := range r.Rules {
		var rows []Sample
		for _, s := range r.Samples {
			if s.RuleID == rule.RuleID {
				rows = append(rows, s)
			}
		}
		if len(rows) == 0 {
			continue
		}
		any = true
		fmt.Fprintf(&b, "### %s\n\n", cell(rule.RuleID))
		fmt.Fprintf(&b, "| id | class | event | ts | uuid | detail | label |\n")
		fmt.Fprintf(&b, "|---|---|---|---|---|---|---|\n")
		for _, s := range rows {
			ts := "-"
			if !s.TS.IsZero() {
				ts = s.TS.UTC().Format(time.RFC3339)
			}
			detail := s.Matched
			if s.Class == Unknown {
				detail = s.UnknownCause
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s | `%s` | %s | %s |\n", s.ID, s.Class, s.Event, ts, cell(s.UUID), cell(detail), dashIfEmpty(s.Label))
		}
		b.WriteByte('\n')
	}
	if !any {
		fmt.Fprintf(&b, "_No samples._\n")
	}
	return b.Bytes()
}

// cell makes s safe inside a Markdown table cell.
func cell(s string) string {
	s = strings.NewReplacer("|", `\|`, "\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	return s
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
