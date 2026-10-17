// Command t0 is the case driver of scripts/boundary-t0.sh (docs/p2-design.md
// §5.2). It reads the fixture scripts/boundary/t0-cases.json, feeds each row
// as a synthetic payload to the REAL `ballast hook` process and checks the
// audit log:
//
//   - expect "detected": the hook exits 0 and the audit holds a rule_id
//     "no-tamper" hit (a would_* or log decision) for this case's session;
//   - expect "gap": the hook exits 0 and the audit holds NO such hit. A gap
//     row that IS detected fails the run with "the coverage changed, update
//     docs/trust-model.md §2.1" - the fixture is the executable gap
//     register of the T0 limits.
//
// It prints one line per row ("ok   <id>" / "FAIL <id>") and a final
// "T0CASES pass=N fail=M". The environment is inherited from the script,
// which has redirected HOME, BALLAST_HOME, CLAUDE_CONFIG_DIR, ... into a
// temporary directory; the driver refuses to run unless BALLAST_HOME lies
// under -tmp-root. It never touches a real client configuration.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type caseRow struct {
	ID      string         `json:"id"`
	Client  string         `json:"client"`
	Tool    string         `json:"tool"`
	Input   map[string]any `json:"input"`
	Targets []string       `json:"targets"`
	Expect  string         `json:"expect"`
	Via     string         `json:"via"`
	// Env overrides environment variables of the hook process for this row
	// (placeholders allowed); used to redirect the client config roots.
	Env map[string]string `json:"env,omitempty"`
	// Class is the trust-model §2.1 "未覆盖的写法" class (1-7) of a gap row.
	Class int `json:"class,omitempty"`
}

type fixture struct {
	Version int               `json:"version"`
	Targets map[string]string `json:"targets"`
	Cases   []caseRow         `json:"cases"`
}

type driver struct {
	bin, repo string
	vars      map[string]string
	targets   map[string]string
	audit     string
	pass      int
	fail      int
	seq       int
}

func main() {
	var bin, cases, repo, tmpRoot string
	flag.StringVar(&bin, "bin", "", "ballast binary")
	flag.StringVar(&cases, "cases", "", "fixture JSON")
	flag.StringVar(&repo, "repo", "", "git repository used as the session cwd (has .ballast/project.json)")
	flag.StringVar(&tmpRoot, "tmp-root", "", "temporary root; BALLAST_HOME must lie under it")
	flag.Parse()
	if bin == "" || cases == "" || repo == "" || tmpRoot == "" {
		fmt.Fprintln(os.Stderr, "t0: -bin, -cases, -repo and -tmp-root are required")
		os.Exit(2)
	}
	home := os.Getenv("BALLAST_HOME")
	if home == "" || !within(home, tmpRoot) {
		fmt.Fprintf(os.Stderr, "t0: refusing to run: BALLAST_HOME=%q is not under %q\n", home, tmpRoot)
		os.Exit(2)
	}
	for _, k := range []string{"HOME", "BALLAST_MANAGED_DIR", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "HERMES_HOME"} {
		if v := os.Getenv(k); v == "" || !within(v, tmpRoot) {
			fmt.Fprintf(os.Stderr, "t0: refusing to run: %s=%q is not under %q\n", k, v, tmpRoot)
			os.Exit(2)
		}
	}
	data, err := os.ReadFile(cases)
	if err != nil {
		fatal(err)
	}
	var fx fixture
	if err := json.Unmarshal(data, &fx); err != nil {
		fatal(fmt.Errorf("%s: %w", cases, err))
	}
	if fx.Version != 1 {
		fatal(fmt.Errorf("%s: unsupported version %d", cases, fx.Version))
	}
	d := &driver{bin: bin, repo: repo, audit: filepath.Join(home, "audit")}
	d.vars = map[string]string{
		"BALLAST_HOME": home,
		"REPO":         repo,
		"CLAUDE_DIR":   os.Getenv("CLAUDE_CONFIG_DIR"),
		"CODEX_DIR":    os.Getenv("CODEX_HOME"),
		"HERMES_DIR":   os.Getenv("HERMES_HOME"),
		"MANAGED":      os.Getenv("BALLAST_MANAGED_DIR"),
		"ORG_MANAGED":  defaultManagedDir(),
		"HOME":         os.Getenv("HOME"),
		"TMP":          tmpRoot,
	}
	d.targets = map[string]string{}
	for k, v := range fx.Targets {
		d.targets[k] = d.sub(v, "")
	}
	for _, c := range fx.Cases {
		d.run(c)
	}
	fmt.Printf("T0CASES pass=%d fail=%d\n", d.pass, d.fail)
	if d.fail > 0 {
		os.Exit(1)
	}
}

// defaultManagedDir is the platform's default Claude managed directory
// (internal/layout.ManagedDirFor), the literal location of the org rules
// directory <managed>/ballast/ that the shipped no-tamper rule protects. The
// sandbox redirects BALLAST_MANAGED_DIR into a temp dir, which no static rule
// path can name, so org-directory rows use this literal path in synthetic
// payloads only: the hook parses them and never writes anything.
func defaultManagedDir() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode"
	case "windows":
		return `C:\Program Files\ClaudeCode`
	default:
		return "/etc/claude-code"
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "t0:", err)
	os.Exit(2)
}

func within(p, root string) bool {
	p, root = filepath.ToSlash(filepath.Clean(p)), filepath.ToSlash(filepath.Clean(root))
	return p == root || strings.HasPrefix(p, strings.TrimRight(root, "/")+"/")
}

// sub replaces {{NAME}} placeholders (and {{T}} with target).
func (d *driver) sub(s, target string) string {
	for k, v := range d.vars {
		s = strings.ReplaceAll(s, "{{"+k+"}}", filepath.ToSlash(v))
	}
	return strings.ReplaceAll(s, "{{T}}", filepath.ToSlash(target))
}

func (d *driver) subAny(v any, target string) any {
	switch x := v.(type) {
	case string:
		return d.sub(x, target)
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = d.subAny(x[i], target)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = d.subAny(e, target)
		}
		return out
	}
	return v
}

func (d *driver) report(ok bool, id, detail string) {
	if ok {
		d.pass++
		fmt.Printf("ok   %s\n", id)
		return
	}
	d.fail++
	fmt.Printf("FAIL %s\n", id)
	if detail != "" {
		fmt.Printf("     %s\n", detail)
	}
}

// payload builds the native event name and stdin JSON for a row.
func (d *driver) payload(c caseRow, session, useID string, input map[string]any) (event string, stdin []byte, err error) {
	cwd := filepath.ToSlash(d.repo)
	var m map[string]any
	switch c.Client {
	case "claude-code":
		event = "PreToolUse"
		m = map[string]any{"session_id": session, "cwd": cwd, "hook_event_name": event,
			"tool_name": c.Tool, "tool_use_id": useID, "tool_input": input}
	case "codex":
		event = "PreToolUse"
		m = map[string]any{"session_id": session, "cwd": cwd, "hook_event_name": event,
			"tool_name": c.Tool, "tool_use_id": useID, "tool_input": input}
	case "cursor":
		if c.Tool == "Shell" {
			event = "beforeShellExecution"
			m = map[string]any{"conversation_id": session, "cwd": cwd, "command": input["command"]}
		} else {
			event = "preToolUse"
			m = map[string]any{"conversation_id": session, "cwd": cwd, "tool_use_id": useID,
				"tool_name": c.Tool, "tool_input": input}
		}
	case "hermes":
		event = "pre_tool_call"
		m = map[string]any{"session_id": session, "cwd": cwd, "tool_name": c.Tool, "tool_input": input}
	default:
		return "", nil, fmt.Errorf("unknown client %q", c.Client)
	}
	stdin, err = json.Marshal(m)
	return event, stdin, err
}

type auditHit struct {
	Type     string `json:"type"`
	RuleID   string `json:"rule_id"`
	Decision string `json:"decision"`
	Session  string `json:"session_id"`
}

// tamperHits counts the no-tamper hit records of a session.
func (d *driver) tamperHits(session string) int {
	files, _ := filepath.Glob(filepath.Join(d.audit, "*.jsonl"))
	n := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, ln := range bytes.Split(b, []byte("\n")) {
			if len(ln) == 0 {
				continue
			}
			var h auditHit
			if json.Unmarshal(ln, &h) != nil {
				continue
			}
			if h.Session == session && h.Type == "hit" && h.RuleID == "no-tamper" {
				n++
			}
		}
	}
	return n
}

func (d *driver) run(c caseRow) {
	targets := c.Targets
	if len(targets) == 0 {
		targets = []string{"rules"}
	}
	for _, tname := range targets {
		d.seq++
		id := c.ID
		if len(targets) > 1 {
			id += "@" + tname
		}
		label := fmt.Sprintf("%s [%s] %s %s", id, c.Expect, c.Client, c.Via)
		target, ok := d.targets[tname]
		if !ok {
			d.report(false, label, "unknown target "+tname)
			continue
		}
		if c.Expect != "detected" && c.Expect != "gap" {
			d.report(false, label, "expect must be detected or gap")
			continue
		}
		input, _ := d.subAny(c.Input, target).(map[string]any)
		session := fmt.Sprintf("t0-%03d", d.seq)
		event, stdin, err := d.payload(c, session, fmt.Sprintf("tu-%03d", d.seq), input)
		if err != nil {
			d.report(false, label, err.Error())
			continue
		}
		cmd := exec.Command(d.bin, "hook", "--client", c.Client, "--event", event)
		cmd.Dir = d.repo
		cmd.Env = os.Environ()
		for k, v := range c.Env {
			cmd.Env = append(cmd.Env, k+"="+d.sub(v, target))
		}
		cmd.Stdin = bytes.NewReader(stdin)
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr = &so, &se
		code := 0
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				d.report(false, label, err.Error())
				continue
			}
		}
		if code != 0 {
			d.report(false, label, fmt.Sprintf("hook exit %d, want 0; stderr: %s", code, strings.TrimSpace(se.String())))
			continue
		}
		hits := d.tamperHits(session)
		switch {
		case c.Expect == "detected" && hits == 0:
			d.report(false, label, "not detected: the audit has no no-tamper hit (the T0 coverage shrank; fix the detector or move the row to gap AND update docs/trust-model.md §2.1)")
		case c.Expect == "gap" && hits > 0:
			d.report(false, label, "a documented gap is now detected: the coverage changed, please update docs/trust-model.md §2.1 and this fixture")
		default:
			d.report(true, label, "")
		}
	}
}
