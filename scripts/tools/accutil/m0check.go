package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// m0RequiredCases are the M0a case ids the milestone table demands.
var m0RequiredCases = []string{
	"bash-ok", "bash-exit1", "grep-exit1", "bash-startfail", "exit1-json-deny",
	"updatedinput-no-allow", "updatedinput-allow", "warn-additionalcontext",
	"stop-systemmessage", "stop-cap", "parallel-posttooluse", "interrupt", "subagent", "compact", "common-fields",
}

type m0Case struct {
	CaseID     string          `json:"case_id"`
	Events     []string        `json:"events"`
	Event      string          `json:"event"`
	SchemaKeys json.RawMessage `json:"schema_keys"`
	Assertion  string          `json:"assertion"`
	Result     string          `json:"result"`
	Evidence   []string        `json:"evidence"`
}

// cmdM0Check validates the M0 facts of the repository at ROOT (accept-m0.sh):
//   - docs/facts/cases.json holds every required case with event(s),
//     schema_keys, assertion and a result of pass/unverifiable, and the
//     evidence directories it cites exist;
//   - every plan A-row still waiting for M0a has a conclusion row in
//     M0a-CONCLUSIONS.md;
//   - the M0c summary states a worst p95 not above 30ms.
func cmdM0Check(args []string) int {
	if len(args) != 1 {
		return fail("m0-check: want ROOT")
	}
	root := args[0]
	status := 0
	bad := func(f string, a ...any) {
		fmt.Fprintf(os.Stderr, "m0-check: "+f+"\n", a...)
		status = 1
	}

	raw, err := os.ReadFile(filepath.Join(root, "docs", "facts", "cases.json"))
	if err != nil {
		return fail("m0-check: %v", err)
	}
	var cases []m0Case
	if err := json.Unmarshal(raw, &cases); err != nil {
		return fail("m0-check: cases.json: %v", err)
	}
	byID := map[string]m0Case{}
	for _, c := range cases {
		if _, dup := byID[c.CaseID]; dup {
			bad("case %q appears twice", c.CaseID)
		}
		byID[c.CaseID] = c
	}
	pass, unverifiable := 0, 0
	for _, id := range m0RequiredCases {
		c, ok := byID[id]
		if !ok {
			bad("required case %q is missing", id)
			continue
		}
		if len(c.Events) == 0 && c.Event == "" {
			bad("case %s has no event", id)
		}
		if len(c.SchemaKeys) == 0 || string(c.SchemaKeys) == "null" {
			bad("case %s has no schema_keys", id)
		}
		if strings.TrimSpace(c.Assertion) == "" {
			bad("case %s has no assertion", id)
		}
		switch c.Result {
		case "pass":
			pass++
		case "unverifiable":
			unverifiable++
		default:
			bad("case %s has result %q (want pass or unverifiable)", id, c.Result)
		}
		for _, ev := range c.Evidence {
			if fi, err := os.Stat(filepath.Join(root, "docs", "facts", filepath.FromSlash(ev))); err != nil || !fi.IsDir() {
				bad("case %s cites missing evidence %s", id, ev)
			}
		}
	}
	fmt.Printf("cases: %d required, %d pass, %d unverifiable\n", len(m0RequiredCases), pass, unverifiable)

	// A-rows waiting for M0a must have a conclusion.
	plan, err := os.Open(filepath.Join(root, "docs", "ballast-plan.md"))
	if err != nil {
		return fail("m0-check: %v", err)
	}
	defer plan.Close()
	conclusions, err := os.ReadFile(filepath.Join(root, "docs", "facts", "M0a-CONCLUSIONS.md"))
	if err != nil {
		return fail("m0-check: %v", err)
	}
	rowRE := regexp.MustCompile(`^\|\s*(A\d+)\s*\|`)
	pending := 0
	sc := bufio.NewScanner(plan)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		m := rowRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		cell := strings.TrimSpace(line[strings.LastIndex(strings.TrimSuffix(line, "|"), "|")+1:])
		cell = strings.TrimSuffix(cell, "|")
		if strings.Contains(cell, "M0a") && strings.Contains(cell, "待") || strings.Contains(cell, "M0a 实测") || strings.Contains(cell, "M0a 顺带") {
			pending++
			re := regexp.MustCompile(`(?m)^\|\s*` + m[1] + `\s[^|]*\|\s*\S`)
			if !re.Match(conclusions) {
				bad("plan item %s is waiting for M0a but M0a-CONCLUSIONS.md has no conclusion row for it", m[1])
			}
		}
	}
	fmt.Printf("plan items waiting for M0a: %d\n", pending)
	if pending == 0 {
		bad("found no plan A-rows waiting for M0a; the check parses docs/ballast-plan.md and may be out of date")
	}

	// M0c gate.
	perf, err := os.ReadFile(filepath.Join(root, "docs", "facts", "M0c-PERF.md"))
	if err != nil {
		return fail("m0-check: %v", err)
	}
	pm := regexp.MustCompile(`最差 p95 为 ([0-9.]+)ms`).FindSubmatch(perf)
	if pm == nil {
		bad("M0c-PERF.md does not state the worst hook p95")
	} else {
		v, _ := strconv.ParseFloat(string(pm[1]), 64)
		fmt.Printf("M0c worst hook p95: %.2fms (gate 30ms)\n", v)
		if v > 30 {
			bad("M0c worst p95 %.2fms exceeds the 30ms gate", v)
		}
	}
	return status
}
