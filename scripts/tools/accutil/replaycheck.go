package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The structs mirror internal/replay.Report on purpose (the script checks
// the frozen JSON shape, not the Go type), so a field rename in the product
// makes the strict decode below fail.
type reportDoc struct {
	V            int           `json:"v"`
	Transcript   string        `json:"transcript"`
	Client       string        `json:"client"`
	RulesHash    string        `json:"rules_hash"`
	Items        int           `json:"items"`
	SkippedLines int           `json:"skipped_lines"`
	Rules        []reportRule  `json:"rules"`
	Samples      []reportEntry `json:"samples"`
}

type reportRule struct {
	RuleID    string         `json:"rule_id"`
	Counts    map[string]int `json:"counts"`
	Labeled   int            `json:"labeled"`
	Precision *float64       `json:"precision"`
}

type reportEntry struct {
	ID           string `json:"id"`
	RuleID       string `json:"rule_id"`
	Class        string `json:"class"`
	Event        string `json:"event"`
	TS           string `json:"ts"`
	UUID         string `json:"uuid"`
	Matched      string `json:"matched"`
	UnknownCause string `json:"unknown_cause"`
	Label        string `json:"label"`
}

var (
	classes  = map[string]bool{"known-hit": true, "known-miss": true, "unknown": true, "approximate": true}
	clients  = map[string]bool{"claude-code": true, "codex": true, "cursor": true, "hermes": true}
	sampleRE = regexp.MustCompile(`^[0-9a-f]{16}$`)
	ruleIDRE = regexp.MustCompile(`^[a-z0-9-]{3,40}$`)
	hashRE   = regexp.MustCompile(`^[0-9a-f]{8,64}$`)
	eventRE  = regexp.MustCompile(`^[A-Za-z]{3,32}$`)
)

func loadReport(path string) (*reportDoc, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rep reportDoc
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rep); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &rep, nil
}

// validate returns every schema violation of a replay report.
func (r *reportDoc) validate() []string {
	var bad []string
	add := func(f string, a ...any) { bad = append(bad, fmt.Sprintf(f, a...)) }
	if r.V != 1 {
		add("v = %d, want 1", r.V)
	}
	if r.Transcript == "" {
		add("transcript is empty")
	}
	if !clients[r.Client] {
		add("client %q is not a known client", r.Client)
	}
	if !hashRE.MatchString(r.RulesHash) {
		add("rules_hash %q is not hex", r.RulesHash)
	}
	if r.Items < 0 || r.SkippedLines < 0 {
		add("negative item counts")
	}
	if r.Rules == nil || r.Samples == nil {
		add("rules and samples must be arrays, not null")
	}
	perRule := map[string]map[string]int{}
	labeled := map[string]int{}
	for i, ru := range r.Rules {
		if !ruleIDRE.MatchString(ru.RuleID) {
			add("rules[%d].rule_id %q is not a rule id", i, ru.RuleID)
		}
		for c, n := range ru.Counts {
			if !classes[c] {
				add("rules[%d].counts has unknown class %q", i, c)
			}
			if n < 0 {
				add("rules[%d].counts[%s] is negative", i, c)
			}
		}
		if ru.Precision != nil && (*ru.Precision < 0 || *ru.Precision > 1) {
			add("rules[%d].precision %v is outside [0,1]", i, *ru.Precision)
		}
		perRule[ru.RuleID] = map[string]int{}
	}
	ids := map[string]bool{}
	for i, s := range r.Samples {
		if !sampleRE.MatchString(s.ID) {
			add("samples[%d].id %q is not 16 hex digits", i, s.ID)
		}
		if ids[s.ID] {
			add("samples[%d].id %s is duplicated", i, s.ID)
		}
		ids[s.ID] = true
		if !classes[s.Class] {
			add("samples[%d].class %q is unknown", i, s.Class)
		}
		if _, ok := perRule[s.RuleID]; !ok {
			add("samples[%d].rule_id %q has no entry in rules", i, s.RuleID)
			continue
		}
		perRule[s.RuleID][s.Class]++
		if !eventRE.MatchString(s.Event) {
			add("samples[%d].event %q is unknown", i, s.Event)
		}
		if _, err := time.Parse(time.RFC3339Nano, s.TS); err != nil {
			add("samples[%d].ts %q is not RFC 3339", i, s.TS)
		}
		if s.Label != "" && s.Label != "tp" && s.Label != "fp" {
			add("samples[%d].label %q is not tp/fp", i, s.Label)
		}
		if s.Label != "" && s.Class == "known-hit" {
			labeled[s.RuleID]++
		}
		if s.Class == "unknown" && s.UnknownCause == "" {
			add("samples[%d] is unknown without unknown_cause", i)
		}
	}
	for _, ru := range r.Rules {
		for c, n := range perRule[ru.RuleID] {
			if n > ru.Counts[c] {
				add("rule %s lists %d %s samples but counts only %d", ru.RuleID, n, c, ru.Counts[c])
			}
		}
		if ru.Labeled != labeled[ru.RuleID] {
			add("rule %s: labeled=%d but %d labeled known-hit samples", ru.RuleID, ru.Labeled, labeled[ru.RuleID])
		}
	}
	return bad
}

// cmdReplayCheck validates the schema of replay JSON reports and prints one
// summary line per report.
func cmdReplayCheck(args []string) int {
	if len(args) == 0 {
		return fail("replay-check: want REPORT.json...")
	}
	status := 0
	for _, p := range args {
		rep, err := loadReport(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "replay-check: %v\n", err)
			status = 1
			continue
		}
		bad := rep.validate()
		for _, b := range bad {
			fmt.Fprintf(os.Stderr, "replay-check: %s: %s\n", filepath.Base(p), b)
		}
		if len(bad) > 0 {
			status = 1
			continue
		}
		hits := 0
		for _, s := range rep.Samples {
			if s.Class == "known-hit" {
				hits++
			}
		}
		fmt.Printf("ok items=%d samples=%d known_hit=%d rules=%d\n", rep.Items, len(rep.Samples), hits, len(rep.Rules))
	}
	return status
}

// cmdReplayLabel writes N synthetic labels (known-hit samples first, mostly
// tp) for a report into LABELS_DIR/<rule>.jsonl. It exists to exercise the
// label plumbing in a temporary BALLAST_HOME; replay.sh never calls it.
func cmdReplayLabel(args []string) int {
	if len(args) != 3 {
		return fail("replay-label: want REPORT.json LABELS_DIR N")
	}
	var n int
	if _, err := fmt.Sscanf(args[2], "%d", &n); err != nil || n < 0 {
		return fail("replay-label: bad N %q", args[2])
	}
	rep, err := loadReport(args[0])
	if err != nil {
		return fail("replay-label: %v", err)
	}
	if err := os.MkdirAll(args[1], 0o755); err != nil {
		return fail("replay-label: %v", err)
	}
	pick := make([]reportEntry, 0, len(rep.Samples))
	for _, s := range rep.Samples {
		if s.Class == "known-hit" {
			pick = append(pick, s)
		}
	}
	for _, s := range rep.Samples {
		if s.Class != "known-hit" {
			pick = append(pick, s)
		}
	}
	if len(pick) > n {
		pick = pick[:n]
	}
	byRule := map[string][]string{}
	for i, s := range pick {
		label := "tp"
		if i%5 == 4 {
			label = "fp"
		}
		line, _ := json.Marshal(map[string]string{"sample_id": s.ID, "label": label, "note": "synthetic"})
		byRule[s.RuleID] = append(byRule[s.RuleID], string(line))
	}
	for rule, lines := range byRule {
		f, err := os.OpenFile(filepath.Join(args[1], rule+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fail("replay-label: %v", err)
		}
		_, werr := f.WriteString(strings.Join(lines, "\n") + "\n")
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return fail("replay-label: %v", werr)
		}
	}
	fmt.Println(len(pick))
	return 0
}

// cmdLabelsCheck asserts that LABELS_DIR holds at least MIN valid labels and
// that every label's sample id (and rule) resolves to a sample of one of the
// reports.
func cmdLabelsCheck(args []string) int {
	if len(args) < 3 {
		return fail("labels-check: want LABELS_DIR MIN REPORT.json...")
	}
	var min int
	if _, err := fmt.Sscanf(args[1], "%d", &min); err != nil {
		return fail("labels-check: bad MIN %q", args[1])
	}
	known := map[string]string{} // sample id → rule id
	for _, p := range args[2:] {
		rep, err := loadReport(p)
		if err != nil {
			return fail("labels-check: %v", err)
		}
		for _, s := range rep.Samples {
			known[s.ID] = s.RuleID
		}
	}
	files, err := filepath.Glob(filepath.Join(args[0], "*.jsonl"))
	if err != nil {
		return fail("labels-check: %v", err)
	}
	sort.Strings(files)
	total, status := 0, 0
	seen := map[string]bool{}
	for _, f := range files {
		rule := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		fh, err := os.Open(f)
		if err != nil {
			return fail("labels-check: %v", err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for no := 1; sc.Scan(); no++ {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var l struct {
				SampleID string `json:"sample_id"`
				Label    string `json:"label"`
			}
			if json.Unmarshal(line, &l) != nil || l.SampleID == "" || (l.Label != "tp" && l.Label != "fp") {
				fmt.Fprintf(os.Stderr, "labels-check: %s:%d is not a valid label line\n", filepath.Base(f), no)
				status = 1
				continue
			}
			owner, ok := known[l.SampleID]
			switch {
			case !ok:
				fmt.Fprintf(os.Stderr, "labels-check: %s:%d: sample id %s is not in any report\n", filepath.Base(f), no, l.SampleID)
				status = 1
			case owner != rule:
				fmt.Fprintf(os.Stderr, "labels-check: %s:%d: sample %s belongs to rule %s\n", filepath.Base(f), no, l.SampleID, owner)
				status = 1
			case !seen[l.SampleID]:
				seen[l.SampleID] = true
				total++
			}
		}
		_ = fh.Close()
	}
	fmt.Printf("labels=%d\n", total)
	if total < min {
		fmt.Fprintf(os.Stderr, "labels-check: %d resolvable labels, need at least %d\n", total, min)
		status = 1
	}
	return status
}

// cmdGenTranscript writes a synthetic Claude Code transcript of about
// SIZE_MB megabytes: read/edit/sleep turns with large tool results, so the
// replay of a big file can be measured without any real transcript.
func cmdGenTranscript(args []string) int {
	if len(args) != 4 {
		return fail("gen-transcript: want OUT.jsonl CWD SESSION SIZE_MB")
	}
	var mb float64
	if _, err := fmt.Sscanf(args[3], "%g", &mb); err != nil || mb <= 0 {
		return fail("gen-transcript: bad SIZE_MB %q", args[3])
	}
	out, cwd, session := args[0], args[1], args[2]
	f, err := os.Create(out)
	if err != nil {
		return fail("gen-transcript: %v", err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	target := int64(mb * 1024 * 1024)
	pad := strings.Repeat("x", 6000)
	var written int64
	base := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	emit := func(m map[string]any) {
		b, _ := json.Marshal(m)
		n, _ := w.Write(append(b, '\n'))
		written += int64(n)
	}
	for k := 0; written < target; k++ {
		ts := func(off int) string { return base.Add(time.Duration(k*10+off) * time.Second).Format(time.RFC3339) }
		uid := func(kind string) string { return fmt.Sprintf("%s-%06d", kind, k) }
		file := fmt.Sprintf("%s/src/f%02d.go", filepath.ToSlash(cwd), k%40)
		common := func(typ, id string, off int, msg map[string]any) map[string]any {
			return map[string]any{"type": typ, "uuid": id, "timestamp": ts(off), "sessionId": session, "cwd": cwd, "message": msg}
		}
		emit(common("user", uid("p"), 0, map[string]any{"role": "user", "content": fmt.Sprintf("task %d: please change the module", k)}))
		emit(common("assistant", uid("r"), 1, map[string]any{"role": "assistant", "stop_reason": "tool_use", "content": []any{
			map[string]any{"type": "tool_use", "id": fmt.Sprintf("tr%d", k), "name": "Read", "input": map[string]any{"file_path": file}}}}))
		emit(common("user", uid("rr"), 2, map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": fmt.Sprintf("tr%d", k), "content": pad}}}))
		editFile := file
		if k%3 == 0 { // never read in this transcript: read-before-edit hits
			editFile = fmt.Sprintf("%s/unread/g%d.go", filepath.ToSlash(cwd), k)
		}
		emit(common("assistant", uid("e"), 3, map[string]any{"role": "assistant", "stop_reason": "tool_use", "content": []any{
			map[string]any{"type": "tool_use", "id": fmt.Sprintf("te%d", k), "name": "Edit", "input": map[string]any{"file_path": editFile, "old_string": "a", "new_string": "b"}}}}))
		emit(common("user", uid("er"), 4, map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": fmt.Sprintf("te%d", k), "content": "ok"}}}))
		if k%4 == 0 {
			emit(common("assistant", uid("s"), 5, map[string]any{"role": "assistant", "stop_reason": "tool_use", "content": []any{
				map[string]any{"type": "tool_use", "id": fmt.Sprintf("ts%d", k), "name": "Bash", "input": map[string]any{"command": "sleep 300"}}}}))
			emit(common("user", uid("sr"), 6, map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": fmt.Sprintf("ts%d", k), "content": ""}}}))
		}
		emit(common("assistant", uid("t"), 7, map[string]any{"role": "assistant", "stop_reason": "end_turn", "content": []any{
			map[string]any{"type": "text", "text": "done"}}}))
	}
	if err := w.Flush(); err != nil {
		return fail("gen-transcript: %v", err)
	}
	if err := f.Close(); err != nil {
		return fail("gen-transcript: %v", err)
	}
	fmt.Println(written)
	return 0
}
