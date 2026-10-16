// Command check validates the M0b artifacts (docs/p2-design.md §6.5):
//
//	go run ./scripts/probe-m0b/check docs/facts/m0b-cases.json
//
// It is a standalone main package, outside the archtest scope. It verifies
// that every required case is present with all fields, that needs_credentials
// entries carry a runnable recheck command, that the evidence directories
// exist and hold no local paths or secrets (the same patterns as the CI
// hygiene step plus credential shapes), that the conclusions file covers every
// case, and that the degradation rule for a failed nocred-events holds.
//
// run.sh calls this program for its closing hygiene scan instead of keeping a
// second set of patterns; "--hygiene-only" runs just the path/secret scan and
// the payload-format rule over the evidence tree, for partial runs.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// NoCredCases are the 15 cases that need no credentials (§6.4), in table order.
var NoCredCases = []string{
	"nocred-events", "managed-only-blocks-lower", "hooks-merge-all-layers",
	"lower-disableallhooks-vs-managed", "managed-disableallhooks", "managed-d-merge-hooks",
	"managed-d-merge-scalar", "managed-hook-path-forms", "managed-file-owner",
	"sandbox-failifunavailable", "mcp-allowlist-nocred", "configchange-payload",
	"runner-bwrap-in-docker", "hooks-run-outside-sandbox", "lower-sandbox-merge",
}

// CredCases are the 6 cases that need real model calls (§6.4).
var CredCases = []string{
	"sandbox-denywrite-bash", "permissions-deny-edit", "unsandboxed-refused",
	"managed-denywrite-path-forms", "managed-denywrite-anydepth", "mcp-write",
}

// independentOfEvents lists the no-credential cases that stay valid even when
// nocred-events fails (§6.4, last paragraph: runner-bwrap-in-docker,
// mcp-allowlist-nocred and the event-free part of managed-file-owner).
// sandbox-failifunavailable is deliberately absent: its control group is
// judged by managed hook events.
var independentOfEvents = map[string]bool{
	"nocred-events": true, "runner-bwrap-in-docker": true, "mcp-allowlist-nocred": true,
	"managed-file-owner": true,
}

// noEventsAllowed lists cases whose events array may be empty.
var noEventsAllowed = map[string]bool{
	"mcp-allowlist-nocred": true, "runner-bwrap-in-docker": true,
	"sandbox-denywrite-bash": true, "permissions-deny-edit": true, "unsandboxed-refused": true,
	"managed-denywrite-path-forms": true, "managed-denywrite-anydepth": true, "mcp-write": true,
}

var resultValues = map[string]bool{"pass": true, "fail": true, "unverifiable": true, "needs_credentials": true}
var modeValues = map[string]bool{"none": true, "blackhole": true, "hang": true, "credentials": true, "mock": true}

// Case mirrors one element of m0b-cases.json (§6.5).
type Case struct {
	CaseID        string          `json:"case_id"`
	Events        []string        `json:"events"`
	SchemaKeys    map[string]any  `json:"schema_keys"`
	Assertion     string          `json:"assertion"`
	Result        string          `json:"result"`
	Observation   string          `json:"observation"`
	Evidence      []string        `json:"evidence"`
	Mode          string          `json:"mode"`
	ClaudeVersion string          `json:"claude_version"`
	Recheck       string          `json:"recheck"`
	Raw           json.RawMessage `json:"-"`
}

// hygiene patterns: the CI repository hygiene step (local absolute paths,
// directory structure and uid fragments) plus credential shapes.
var (
	reLocalPath = regexp.MustCompile(`(^|[^A-Za-z:}])/Users/[a-z][A-Za-z0-9._-]*/(NewCode|Documents|Desktop|Library|\.claude|\.ballast)`)
	reLocalTree = regexp.MustCompile(`[~]/NewCode|claude-[0-9]+-`)
	reLinuxHome = regexp.MustCompile(`/home/agent[/"\s]`)
	secretRes   = []*regexp.Regexp{
		regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{8,}`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/-]{20,}`),
		regexp.MustCompile(`eyJ[A-Za-z0-9_-]{15,}\.[A-Za-z0-9_-]{10,}`),
		regexp.MustCompile(`\bghp_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)["']?\s*[:=]\s*["']?[A-Za-z0-9+/_-]{24,}`),
	}
	reAPIKeyAssign = regexp.MustCompile(`ANTHROPIC_API_KEY=([^\s"'<]+)`)
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	hygieneOnly := false
	if len(args) == 2 && args[0] == "--hygiene-only" {
		hygieneOnly = true
		args = args[1:]
	}
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: check [--hygiene-only] <path/to/m0b-cases.json>")
		return 2
	}
	var problems []string
	var summary string
	if hygieneOnly {
		problems, summary = Hygiene(args[0])
	} else {
		problems, summary = Validate(args[0])
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(stderr, "m0b-check:", p)
		}
		return 1
	}
	fmt.Fprintln(stdout, summary)
	return 0
}

// Hygiene runs only the local-path/secret scan and the payload-format rule
// over the evidence tree next to casesPath and over casesPath itself.
func Hygiene(casesPath string) (problems []string, summary string) {
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	raw, err := os.ReadFile(casesPath)
	if err != nil {
		bad("read %s: %v", casesPath, err)
		return
	}
	scanTree(filepath.Join(filepath.Dir(casesPath), "payloads", "m0b"), bad)
	scanText(casesPath, string(raw), bad)
	return problems, "m0b-check: hygiene ok"
}

// Validate checks the artifacts that belong to casesPath and returns the
// problems found (empty when valid) plus a one-line summary.
func Validate(casesPath string) (problems []string, summary string) {
	bad := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	raw, err := os.ReadFile(casesPath)
	if err != nil {
		return []string{err.Error()}, ""
	}
	var cases []Case
	if err := json.Unmarshal(raw, &cases); err != nil {
		return []string{"m0b-cases.json: " + err.Error()}, ""
	}
	factsDir := filepath.Dir(casesPath)
	byID := map[string]Case{}
	for _, c := range cases {
		if _, dup := byID[c.CaseID]; dup {
			bad("case %q appears twice", c.CaseID)
		}
		byID[c.CaseID] = c
	}
	required := append(append([]string{}, NoCredCases...), CredCases...)
	known := map[string]bool{}
	for _, id := range required {
		known[id] = true
	}
	for id := range byID {
		if !known[id] {
			bad("unexpected case %q (not in docs/p2-design.md §6.4)", id)
		}
	}
	counts := map[string]int{}
	for _, id := range required {
		c, ok := byID[id]
		if !ok {
			bad("required case %q is missing", id)
			continue
		}
		counts[c.Result]++
		validateCase(c, factsDir, bad)
	}
	// §6.4: 15 no-credential + 6 credential cases.
	for _, id := range CredCases {
		c, ok := byID[id]
		if !ok {
			continue
		}
		if c.Mode != "credentials" && c.Result != "needs_credentials" && c.Result != "unverifiable" {
			bad("case %s: needs credentials; without mode \"credentials\" its result must be needs_credentials (got %q, mode %q)", id, c.Result, c.Mode)
		}
		if c.Result == "pass" && c.Mode != "credentials" {
			bad("case %s: only a credentials-mode run may record pass (mode %q)", id, c.Mode)
		}
	}
	// §6.4 degradation rule: nocred-events not passing ⇒ event-dependent cases are needs_credentials.
	if ne, ok := byID["nocred-events"]; ok && ne.Result != "pass" {
		for _, id := range NoCredCases {
			if independentOfEvents[id] {
				continue
			}
			if c, ok := byID[id]; ok && c.Result != "needs_credentials" {
				bad("nocred-events is %q, so %s must be needs_credentials (got %q)", ne.Result, id, c.Result)
			}
		}
	}
	// hooks-run-outside-sandbox depends on nocred-events passing.
	if ne, ok := byID["nocred-events"]; ok && ne.Result == "pass" {
		if h, ok := byID["hooks-run-outside-sandbox"]; ok && h.Result == "needs_credentials" && h.Recheck == "" {
			bad("hooks-run-outside-sandbox: needs_credentials requires recheck")
		}
	}
	// payload hygiene over the whole payloads/m0b tree.
	payloadRoot := filepath.Join(factsDir, "payloads", "m0b")
	scanTree(payloadRoot, bad)
	// the cases file itself is public too.
	scanText(casesPath, string(raw), bad)
	// conclusions file.
	concl := filepath.Join(factsDir, "M0b-CONCLUSIONS.md")
	if b, err := os.ReadFile(concl); err != nil {
		bad("M0b-CONCLUSIONS.md: %v", err)
	} else {
		text := string(b)
		scanText(concl, text, bad)
		for _, id := range required {
			if !strings.Contains(text, id) {
				bad("M0b-CONCLUSIONS.md does not mention case %s", id)
			}
		}
		for _, a := range []string{"A13", "A14", "A15", "A16", "A20"} {
			if !strings.Contains(text, a) {
				bad("M0b-CONCLUSIONS.md has no conclusion row for %s", a)
			}
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	summary = fmt.Sprintf("m0b-check: %d cases ok (%s)", len(byID), strings.Join(parts, ", "))
	return problems, summary
}

func validateCase(c Case, factsDir string, bad func(string, ...any)) {
	id := c.CaseID
	if c.Events == nil && !noEventsAllowed[id] {
		bad("case %s: events missing", id)
	}
	if len(c.Events) == 0 && !noEventsAllowed[id] {
		bad("case %s: events is empty", id)
	}
	if c.SchemaKeys == nil {
		bad("case %s: schema_keys missing (use {} when the case has none)", id)
	}
	if strings.TrimSpace(c.Assertion) == "" {
		bad("case %s: assertion missing", id)
	}
	if !resultValues[c.Result] {
		bad("case %s: result %q is not one of pass|fail|unverifiable|needs_credentials", id, c.Result)
	}
	if !modeValues[c.Mode] {
		bad("case %s: mode %q is not one of none|blackhole|hang|credentials|mock", id, c.Mode)
	}
	if strings.TrimSpace(c.Observation) == "" {
		bad("case %s: observation missing", id)
	}
	if strings.TrimSpace(c.ClaudeVersion) == "" {
		bad("case %s: claude_version missing", id)
	}
	if c.Result == "needs_credentials" {
		if strings.TrimSpace(c.Recheck) == "" {
			bad("case %s: needs_credentials requires a recheck command", id)
		} else if !strings.Contains(c.Recheck, "scripts/probe-m0b/run.sh") || !strings.Contains(c.Recheck, "--with-credentials") {
			bad("case %s: recheck must be a runnable scripts/probe-m0b/run.sh --with-credentials command (got %q)", id, c.Recheck)
		} else if !strings.Contains(c.Recheck, "ANTHROPIC_API_KEY=<") {
			bad("case %s: recheck must pass the key as ANTHROPIC_API_KEY=<user-provided> placeholder (got %q)", id, c.Recheck)
		}
		if strings.Contains(c.Recheck, "--cases") && !strings.Contains(c.Recheck, id) {
			bad("case %s: recheck --cases list does not name the case", id)
		}
		return
	}
	if len(c.Evidence) == 0 {
		bad("case %s: evidence missing for a case that ran", id)
	}
	for _, e := range c.Evidence {
		if filepath.IsAbs(e) || strings.Contains(e, "..") {
			bad("case %s: evidence %q must be a relative path under docs/facts", id, e)
			continue
		}
		p := filepath.Join(factsDir, e)
		st, err := os.Stat(p)
		if err != nil {
			bad("case %s: evidence %q does not exist", id, e)
			continue
		}
		if st.IsDir() {
			n := 0
			_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					n++
				}
				return nil
			})
			if n == 0 {
				bad("case %s: evidence directory %q is empty", id, e)
			}
		}
	}
}

func scanTree(root string, bad func(string, ...any)) {
	if _, err := os.Stat(root); err != nil {
		bad("payload directory %s: %v", root, err)
		return
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			bad("%s: %v", p, err)
			return nil
		}
		scanText(p, string(b), bad)
		checkPayloadFormat(p, b, bad)
		return nil
	})
}

// checkPayloadFormat enforces that every *.json under payloads/m0b is a hook
// payload (a JSON object with a string hook_event_name): the adapter golden
// test TestFactsPayloadsParse walks docs/facts/payloads/** and rejects any
// other .json there. Other evidence (probe results, matrices) must use a
// different extension such as .txt.
func checkPayloadFormat(path string, b []byte, bad func(string, ...any)) {
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		return
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		bad("%s: .json payload is not a JSON object (%v); non-payload evidence must not end in .json", path, err)
		return
	}
	if name, _ := doc["hook_event_name"].(string); name == "" {
		bad("%s: no hook_event_name; only hook payloads may end in .json under payloads/m0b (use .txt for other evidence)", path)
	}
}

// scanText applies the hygiene and secret patterns to one file.
func scanText(name, text string, bad func(string, ...any)) {
	if m := reLocalPath.FindString(text); m != "" {
		bad("%s: local absolute path %q", name, strings.TrimSpace(m))
	}
	if m := reLocalTree.FindString(text); m != "" {
		bad("%s: local directory structure or uid fragment %q (use <repo>, claude-<uid>)", name, m)
	}
	if m := reLinuxHome.FindString(text); m != "" {
		bad("%s: container home path %q was not normalized to <home>/<repo>", name, strings.TrimSpace(m))
	}
	for _, re := range secretRes {
		if m := re.FindString(text); m != "" {
			bad("%s: looks like a secret (%s)", name, re.String())
			break
		}
	}
	for _, m := range reAPIKeyAssign.FindAllStringSubmatch(text, -1) {
		if m[1] != "dummy-not-a-credential" && !strings.HasPrefix(m[1], `\u003c`) {
			bad("%s: ANTHROPIC_API_KEY assignment with a non-dummy value", name)
			break
		}
	}
}
