package replay

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

func mustRun(t testing.TB, e *env, path string, opt Options) *Report {
	t.Helper()
	opt.Transcript = path
	rep, err := Run(opt, e.deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rep
}

func summary(t testing.TB, rep *Report, rule string) RuleSummary {
	t.Helper()
	for _, s := range rep.Rules {
		if s.RuleID == rule {
			return s
		}
	}
	t.Fatalf("no summary for %s in %+v", rule, rep.Rules)
	return RuleSummary{}
}

func samplesOf(rep *Report, rule string) []Sample {
	var out []Sample
	for _, s := range rep.Samples {
		if s.RuleID == rule {
			out = append(out, s)
		}
	}
	return out
}

func counts(s RuleSummary) [4]int {
	return [4]int{s.Counts[KnownHit], s.Counts[KnownMiss], s.Counts[Approximate], s.Counts[Unknown]}
}

func TestRunReadBeforeEdit(t *testing.T) {
	e := newEnv(t, rset("gen1", readBeforeEdit()))
	cwd := e.workdir
	x := newTx("sess-1", cwd)
	x.user("edit things")
	x.tool("t1", "Edit", map[string]any{"file_path": cwd + "/a.go"}).result("t1", "ok", false) // unread: hit
	x.tool("t2", "Edit", map[string]any{"file_path": cwd + "/a.go"}).result("t2", "ok", false) // the first edit counted as read: miss
	x.tool("t3", "Read", map[string]any{"file_path": "b.go"}).result("t3", "package b", false) // relative path against cwd
	x.tool("t4", "Edit", map[string]any{"file_path": cwd + "/b.go"})                           // miss
	x.tool("t5", "Write", map[string]any{"file_path": cwd + "/c.go"})                          // hit
	x.final("done")
	path := x.write(t, filepath.Join(e.dir, "sess-1.jsonl"))

	rep := mustRun(t, e, path, Options{})
	if rep.V != 1 || rep.Client != model.ClientClaude || rep.RulesHash != "gen1" || rep.SkippedLines != 0 {
		t.Errorf("header = %+v", rep)
	}
	s := summary(t, rep, "read-before-edit")
	if got := counts(s); got != [4]int{2, 3, 0, 0} {
		t.Errorf("counts = %v", got)
	}
	// Only hits are samples, in transcript order, with stable ids.
	smp := samplesOf(rep, "read-before-edit")
	if len(smp) != 2 {
		t.Fatalf("samples = %+v", smp)
	}
	if smp[0].UUID != "u002#0" && !strings.HasPrefix(smp[0].UUID, "u00") {
		t.Errorf("uuid = %s", smp[0].UUID)
	}
	for _, sm := range smp {
		if sm.Class != KnownHit || sm.Event != model.EventPreToolUse || sm.Matched == "" || sm.TS.IsZero() {
			t.Errorf("sample = %+v", sm)
		}
		if sm.ID != SampleID(rep.Transcript, sm.UUID, sm.RuleID, rep.RulesHash) {
			t.Errorf("id %s does not follow SampleID", sm.ID)
		}
	}
	if !strings.HasSuffix(smp[0].Matched, "a.go") || !strings.HasSuffix(smp[1].Matched, "c.go") {
		t.Errorf("matched = %q / %q", smp[0].Matched, smp[1].Matched)
	}
	// The loader saw the first item's cwd.
	if len(e.loader.reqs) != 1 || e.loader.reqs[0].Cwd != cwd || e.loader.reqs[0].Client != model.ClientClaude {
		t.Errorf("load requests = %+v", e.loader.reqs)
	}
}

func TestRunFailedToolDoesNotRegisterRead(t *testing.T) {
	failure := cr("failed-read", model.EventPostToolUseFailure, func(r *model.Rule) {
		r.Match = &model.Match{Tool: model.StringList{"Read"}}
	})
	e := newEnv(t, rset("g", readBeforeEdit(), failure))
	cwd := e.workdir
	x := newTx("s", cwd)
	x.user("go")
	x.tool("t1", "Read", map[string]any{"file_path": cwd + "/a.go"}).result("t1", "File does not exist", true)
	x.tool("t2", "Edit", map[string]any{"file_path": cwd + "/a.go"}) // still unread: hit
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "s.jsonl")), Options{})
	if got := counts(summary(t, rep, "read-before-edit")); got != [4]int{1, 1, 0, 0} {
		t.Errorf("read-before-edit = %v", got)
	}
	if got := counts(summary(t, rep, "failed-read")); got != [4]int{1, 0, 0, 0} {
		t.Errorf("failed-read = %v", got)
	}
	smp := samplesOf(rep, "failed-read")
	if len(smp) != 1 || smp[0].Event != model.EventPostToolUseFailure {
		t.Errorf("samples = %+v", smp)
	}
}

func TestRunOrphanResultsAndUnmatchedCalls(t *testing.T) {
	e := newEnv(t, rset("g", readBeforeEdit()))
	cwd := e.workdir
	x := newTx("s", cwd)
	x.result("never-called", "stray", false)
	x.tool("t1", "Read", map[string]any{"file_path": cwd + "/a.go"}) // no result line: not a read
	x.tool("t2", "Edit", map[string]any{"file_path": cwd + "/a.go"}) // hit
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "s.jsonl")), Options{})
	if got := counts(summary(t, rep, "read-before-edit")); got != [4]int{1, 1, 0, 0} {
		t.Errorf("counts = %v", got)
	}
}

func TestRunSessionsAreIndependent(t *testing.T) {
	e := newEnv(t, rset("g", readBeforeEdit()))
	cwd := e.workdir
	a := newTx("A", cwd)
	b := newTx("B", cwd)
	a.tool("a1", "Read", map[string]any{"file_path": cwd + "/f.go"}).result("a1", "x", false)
	b.tool("b1", "Edit", map[string]any{"file_path": cwd + "/f.go"}) // session B never read f.go
	a.tool("a2", "Edit", map[string]any{"file_path": cwd + "/f.go"})
	merged := append(append([]string{}, b.lines...), a.lines...)
	path := filepath.Join(e.dir, "m.jsonl")
	if err := os.MkdirAll(e.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(merged, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := mustRun(t, e, path, Options{})
	if got := counts(summary(t, rep, "read-before-edit")); got != [4]int{1, 2, 0, 0} {
		t.Errorf("counts = %v", got)
	}
}

func TestRunSubagents(t *testing.T) {
	isSub := cr("sub-edit", model.EventPreToolUse, func(r *model.Rule) {
		r.Match = &model.Match{Tool: model.StringList{"Edit"}}
		r.When = []model.Cond{{IsSubagent: &model.Empty{}}}
	})
	e := newEnv(t, rset("g", readBeforeEdit(), isSub))
	cwd := e.workdir
	main := newTx("S", cwd)
	main.user("delegate")
	main.tool("m1", "Read", map[string]any{"file_path": cwd + "/a.go"}).result("m1", "x", false)
	main.tool("m2", "Task", map[string]any{"description": "d"})
	path := main.write(t, filepath.Join(e.dir, "S.jsonl"))

	sub := newTx("S", cwd)
	sub.agent, sub.side = "ag1", true
	sub.user("sub task")
	sub.tool("s1", "Edit", map[string]any{"file_path": cwd + "/a.go"}) // main read a.go, the subagent did not
	sub.final("sub done")
	sub.write(t, filepath.Join(e.dir, "S", "subagents", "agent-ag1.jsonl"))

	// Without --subagents the sidechain is not replayed.
	rep := mustRun(t, e, path, Options{})
	if got := counts(summary(t, rep, "sub-edit")); got != [4]int{0, 2, 0, 0} {
		t.Errorf("without flag: sub-edit = %v", got)
	}
	itemsBefore := rep.Items

	e2 := newEnv(t, rset("g", readBeforeEdit(), isSub))
	rep = mustRun(t, e2, path, Options{Subagents: true})
	if got := counts(summary(t, rep, "sub-edit")); got != [4]int{1, 2, 0, 0} {
		t.Errorf("with flag: sub-edit = %v", got)
	}
	if got := counts(summary(t, rep, "read-before-edit")); got != [4]int{1, 2, 0, 0} {
		t.Errorf("with flag: read-before-edit = %v (the subagent has its own read set)", got)
	}
	if rep.Items <= itemsBefore {
		t.Errorf("items %d <= %d", rep.Items, itemsBefore)
	}

	// SubagentStop rule fires for the subagent's final answer, Stop does not.
	stopRule := cr("sub-stop", model.EventSubagentStop, nil)
	mainStop := cr("main-stop", model.EventStop, nil)
	e3 := newEnv(t, rset("g", stopRule, mainStop))
	rep = mustRun(t, e3, path, Options{Subagents: true})
	if got := counts(summary(t, rep, "sub-stop")); got != [4]int{0, 0, 1, 0} {
		t.Errorf("sub-stop = %v", got)
	}
	if got := counts(summary(t, rep, "main-stop")); got != [4]int{0, 0, 0, 0} {
		t.Errorf("main-stop = %v (main transcript has no final answer)", got)
	}

	// The sub-agent's first user record is the Task input: no UserPromptSubmit
	// fires for it, and it does not feed the prompt state.
	prompt := cr("any-prompt", model.EventUserPromptSubmit, nil)
	e4 := newEnv(t, rset("g", prompt))
	rep = mustRun(t, e4, path, Options{Subagents: true})
	if got := counts(summary(t, rep, "any-prompt")); got != [4]int{1, 0, 0, 0} {
		t.Errorf("any-prompt = %v (only the main prompt counts)", got)
	}
}

func TestRunMissingSubagentDirIsFine(t *testing.T) {
	e := newEnv(t, rset("g", readBeforeEdit()))
	x := newTx("S", e.workdir).user("hi")
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "S.jsonl")), Options{Subagents: true})
	if rep.Items != 1 {
		t.Errorf("items = %d", rep.Items)
	}
}

func TestRunStopFamilyIsApproximate(t *testing.T) {
	done := cr("claims-done", model.EventStop, func(r *model.Rule) {
		r.Match = &model.Match{Text: &model.TextMatch{Regex: model.StringList{"(?i)all done"}}}
	})
	e := newEnv(t, rset("g", done))
	x := newTx("s", e.workdir)
	x.user("q1").final("All done, honest")
	x.user("q2").final("still working")
	x.user("q3").text("no stop reason on this one") // closed by EOF
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "s.jsonl")), Options{})
	s := summary(t, rep, "claims-done")
	if got := counts(s); got != [4]int{0, 0, 3 /*1 hit + 2 misses*/, 0} {
		t.Errorf("counts = %v", got)
	}
	smp := samplesOf(rep, "claims-done")
	if len(smp) != 1 || smp[0].Class != Approximate || smp[0].Event != model.EventStop {
		t.Errorf("samples = %+v", smp)
	}
}

func TestRunStatePredicates(t *testing.T) {
	noisy := cr("many-tools", model.EventPreToolUse, func(r *model.Rule) {
		two := 2.0
		r.When = []model.Cond{{ToolCountSinceInject: &model.Cmp{Gte: &two}}}
	})
	followUp := cr("after-deploy", model.EventUserPromptSubmit, func(r *model.Rule) {
		r.When = []model.Cond{{PrevPromptRegex: &model.RegexArg{Regex: "^deploy"}}}
	})
	repeat := cr("second-strike", model.EventPreToolUse, func(r *model.Rule) {
		one := 1.0
		r.Match = &model.Match{Tool: model.StringList{"Bash"}}
		r.When = []model.Cond{{PriorHit: &model.PriorHitArg{Rule: "many-tools", Cmp: model.Cmp{Gte: &one}}}}
	})
	rs := rset("g", noisy, followUp, repeat)
	off := 0
	rs.Settings.Inject = &model.InjectSettings{BatchAfterTools: &off}
	e := newEnv(t, rs)

	x := newTx("s", e.workdir)
	x.user("deploy to staging")
	for i := 1; i <= 4; i++ { // sees counts 0,1,2,3 -> hits on calls 3 and 4
		x.tool(fmt.Sprintf("t%d", i), "Bash", map[string]any{"command": "ls"})
	}
	x.user("did it work?")                                // after-deploy fires (prev prompt "deploy..."); anchor resets the count
	x.tool("t5", "Bash", map[string]any{"command": "ls"}) // count 0: no hit; prior hit of many-tools >= 1 -> second-strike
	x.user("thanks")                                      // prev prompt no longer matches
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "s.jsonl")), Options{})

	if got := counts(summary(t, rep, "many-tools")); got != [4]int{2, 3, 0, 0} {
		t.Errorf("many-tools = %v", got)
	}
	if got := counts(summary(t, rep, "after-deploy")); got != [4]int{1, 2, 0, 0} {
		t.Errorf("after-deploy = %v", got)
	}
	// second-strike is evaluated on each Bash call: prior hits appear after t3 hits.
	// t1: 0 hits -> miss, t2 miss, t3 miss (many-tools hits at t3 itself), t4 hit, t5 hit.
	if got := counts(summary(t, rep, "second-strike")); got != [4]int{2, 3, 0, 0} {
		t.Errorf("second-strike = %v", got)
	}
}

func TestRunMidTurnAnchorResetsToolCount(t *testing.T) {
	noisy := cr("many-tools", model.EventPreToolUse, func(r *model.Rule) {
		three := 3.0
		r.When = []model.Cond{{ToolCountSinceInject: &model.Cmp{Gte: &three}}}
	})
	rs := rset("g", noisy)
	two := 2
	rs.Settings.Inject = &model.InjectSettings{BatchAfterTools: &two}
	e := newEnv(t, rs)
	x := newTx("s", e.workdir)
	x.user("go")
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("t%d", i)
		x.tool(id, "Bash", map[string]any{"command": "ls"}).result(id, "ok", false)
	}
	// After every second completed call the anchor resets the counter, so it
	// never reaches 3.
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "s.jsonl")), Options{})
	if got := counts(summary(t, rep, "many-tools")); got != [4]int{0, 6, 0, 0} {
		t.Errorf("many-tools = %v", got)
	}
}

func TestRunVerifyNeedsTrustedEvidence(t *testing.T) {
	stale := cr("verify-before-stop", model.EventStop, func(r *model.Rule) {
		r.When = []model.Cond{{Not: &model.Cond{VerifyFresh: &model.VerifyFreshArg{Names: []string{"test"}}}}}
	})
	build := func(e *env) string {
		x := newTx("sess-v", e.workdir)
		x.user("fix")
		x.tool("b1", "Bash", map[string]any{"command": "ballast verify test"}).result("b1", "job started", false)
		x.tool("e1", "Edit", map[string]any{"file_path": e.workdir + "/a.go"}).result("e1", "ok", false)
		x.final("fixed and verified")
		return x.write(t, filepath.Join(e.dir, "sess-v.jsonl"))
	}
	record := func(session string, tend time.Time, outcome model.Outcome, s1, s2 model.TreeID) model.VerifyRecord {
		return model.VerifyRecord{V: 1, JobID: "j1", Name: "test", RepoInst: "ri", Tier: model.TierT0, SStart: s1, SEnd: s2,
			TStart: tend.Add(-time.Minute), TEnd: tend, Outcome: outcome, SessionID: session}
	}
	writeRec := func(t *testing.T, e *env, rec model.VerifyRecord) {
		t.Helper()
		dir := filepath.Join(e.evDir, rec.RepoInst, rec.Name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(rec)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%020d-%s.json", rec.TStart.UnixNano(), rec.JobID)), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)

	t.Run("no evidence: a verify call in the transcript is not evidence", func(t *testing.T) {
		e := newEnv(t, rset("g", stale))
		rep := mustRun(t, e, build(e), Options{})
		if got := counts(summary(t, rep, "verify-before-stop")); got != [4]int{0, 0, 0, 1} {
			t.Fatalf("counts = %v", got)
		}
		smp := samplesOf(rep, "verify-before-stop")
		if len(smp) != 1 || smp[0].Class != Unknown || smp[0].UnknownCause != "no_trusted_evidence" {
			t.Errorf("samples = %+v", smp)
		}
	})
	t.Run("record of another session is ignored", func(t *testing.T) {
		e := newEnv(t, rset("g", stale))
		writeRec(t, e, record("other-session", base.Add(30*time.Second), model.OutcomePass, "T", "T"))
		rep := mustRun(t, e, build(e), Options{})
		if got := counts(summary(t, rep, "verify-before-stop")); got != [4]int{0, 0, 0, 1} {
			t.Errorf("counts = %v", got)
		}
	})
	t.Run("fresh record: judged, not unknown", func(t *testing.T) {
		e := newEnv(t, rset("g", stale))
		writeRec(t, e, record("sess-v", base.Add(time.Minute), model.OutcomePass, "T", "T"))
		rep := mustRun(t, e, build(e), Options{})
		// The record ends after the stop: it was not visible yet -> no_record -> stale -> hit.
		if got := counts(summary(t, rep, "verify-before-stop")); got != [4]int{0, 0, 1, 0} {
			t.Errorf("counts = %v", got)
		}
		if smp := samplesOf(rep, "verify-before-stop"); len(smp) != 1 || smp[0].Class != Approximate {
			t.Errorf("samples = %+v", smp)
		}
	})
	t.Run("pass after the last write is fresh", func(t *testing.T) {
		e := newEnv(t, rset("g", stale))
		// Edit result at 09:00:05, stop at 09:00:06; the pass ends at 09:00:05.5.
		writeRec(t, e, record("sess-v", base.Add(5500*time.Millisecond), model.OutcomePass, "T", "T"))
		rep := mustRun(t, e, build(e), Options{})
		if got := counts(summary(t, rep, "verify-before-stop")); got != [4]int{0, 0, 1, 0} {
			t.Errorf("counts = %v", got)
		}
		if smp := samplesOf(rep, "verify-before-stop"); len(smp) != 0 {
			t.Errorf("a fresh verification must not hit: %+v", smp)
		}
	})
	t.Run("pass before the last write is stale", func(t *testing.T) {
		e := newEnv(t, rset("g", stale))
		writeRec(t, e, record("sess-v", base.Add(3*time.Second), model.OutcomePass, "T", "T"))
		rep := mustRun(t, e, build(e), Options{})
		smp := samplesOf(rep, "verify-before-stop")
		if len(smp) != 1 || smp[0].Class != Approximate {
			t.Errorf("samples = %+v", smp)
		}
	})
	t.Run("failed or torn record is not a valid pass", func(t *testing.T) {
		e := newEnv(t, rset("g", stale))
		writeRec(t, e, record("sess-v", base.Add(5500*time.Millisecond), model.OutcomePass, "T1", "T2")) // tree changed during the run
		rep := mustRun(t, e, build(e), Options{})
		if smp := samplesOf(rep, "verify-before-stop"); len(smp) != 1 {
			t.Errorf("samples = %+v", smp)
		}
	})
	t.Run("--evidence file is trusted for every record", func(t *testing.T) {
		e := newEnv(t, rset("g", stale))
		rec := record("whatever", base.Add(5500*time.Millisecond), model.OutcomePass, "T", "T")
		for name, content := range map[string]string{
			"array.json":  mustJSON(t, []model.VerifyRecord{rec}),
			"single.json": mustJSON(t, rec),
			"lines.jsonl": mustJSON(t, rec) + "\n\n" + mustJSON(t, rec) + "\n",
		} {
			p := filepath.Join(e.sb.Root, name)
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			rep := mustRun(t, e, build(e), Options{EvidenceMode: "file", EvidenceFile: p})
			if got := counts(summary(t, rep, "verify-before-stop")); got != [4]int{0, 0, 1, 0} {
				t.Errorf("%s: counts = %v", name, got)
			}
			if smp := samplesOf(rep, "verify-before-stop"); len(smp) != 0 {
				t.Errorf("%s: fresh evidence hit: %+v", name, smp)
			}
		}
		// A missing file is an I/O error, undecodable content a usage error.
		tp := build(e)
		_, err := Run(Options{Transcript: tp, EvidenceMode: "file", EvidenceFile: filepath.Join(e.sb.Root, "nope.json")}, e.deps)
		if !errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrUsage) {
			t.Errorf("missing file: %v", err)
		}
		for name, content := range map[string]string{"array": `[{"job_id":`, "lines": "{\"job_id\":\"a\"}\nnot json\n", "wrong type": `[1,2]`} {
			p := filepath.Join(e.sb.Root, "bad-"+name+".json")
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Run(Options{Transcript: tp, EvidenceMode: "file", EvidenceFile: p}, e.deps); !errors.Is(err, ErrUsage) {
				t.Errorf("%s: err = %v", name, err)
			}
		}
		// An empty file is valid: the rule stays unknown.
		empty := filepath.Join(e.sb.Root, "empty.json")
		if err := os.WriteFile(empty, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		rep := mustRun(t, e, tp, Options{EvidenceMode: "file", EvidenceFile: empty})
		if got := counts(summary(t, rep, "verify-before-stop")); got != [4]int{0, 0, 0, 1} {
			t.Errorf("empty file: counts = %v", got)
		}
	})
}

func mustJSON(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRunWorldPredicatesAreApproximate(t *testing.T) {
	env := cr("in-ci", model.EventPreToolUse, func(r *model.Rule) {
		r.When = []model.Cond{{EnvEquals: &model.EnvEqualsArg{Name: "CI", Value: "1"}}}
	})
	branch := cr("on-main", model.EventPreToolUse, func(r *model.Rule) {
		r.Match = &model.Match{Tool: model.StringList{"Bash"}}
		r.When = []model.Cond{{BranchGlob: &model.GlobArg{Patterns: model.StringList{"main"}}}}
	})
	exists := cr("lockfile", model.EventPreToolUse, func(r *model.Rule) {
		r.Match = &model.Match{Tool: model.StringList{"Bash"}}
		r.When = []model.Cond{{PathExists: &model.PathArg{Path: "go.sum"}}}
	})
	e := newEnv(t, rset("g", env, branch, exists))
	if err := os.MkdirAll(e.workdir, 0o700); err != nil {
		t.Fatal(err)
	}

	x := newTx("s", e.workdir)
	x.br = "main"
	x.user("go")
	x.tool("t1", "Bash", map[string]any{"command": "ls"}) // branch main -> hit; go.sum absent -> miss
	x.tool("w1", "Write", map[string]any{"file_path": e.workdir + "/go.sum"}).result("w1", "ok", false)
	x.tool("t2", "Bash", map[string]any{"command": "ls"}) // go.sum written in the transcript -> exists
	x.br = "feature"
	x.tool("t3", "Bash", map[string]any{"command": "ls"}) // branch now feature
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "s.jsonl")), Options{})

	if got := counts(summary(t, rep, "in-ci")); got != [4]int{0, 0, 4, 0} {
		t.Errorf("in-ci = %v", got)
	}
	if got := counts(summary(t, rep, "on-main")); got != [4]int{0, 0, 4, 0} {
		t.Errorf("on-main = %v", got)
	}
	if got := counts(summary(t, rep, "lockfile")); got != [4]int{0, 0, 4, 0} {
		t.Errorf("lockfile = %v", got)
	}
	var hits []string
	for _, s := range rep.Samples {
		if s.Class != Approximate {
			t.Errorf("sample %+v is not approximate", s)
		}
		hits = append(hits, s.RuleID)
	}
	// on-main hits t1 and t2; lockfile hits t2 (go.sum written in the transcript) and t3.
	sort.Strings(hits)
	if !reflect.DeepEqual(hits, []string{"lockfile", "lockfile", "on-main", "on-main"}) {
		t.Errorf("hits = %v", hits)
	}

	// A transcript without branch info makes branch_glob unknown.
	e2 := newEnv(t, rset("g", branch))
	y := newTx("s", e2.workdir).user("go").tool("t1", "Bash", map[string]any{"command": "ls"})
	rep = mustRun(t, e2, y.write(t, filepath.Join(e2.dir, "s.jsonl")), Options{})
	if got := counts(summary(t, rep, "on-main")); got != [4]int{0, 0, 0, 1} {
		t.Errorf("no branch: %v", got)
	}
}

func TestRunPathExistsFallsBackToDisk(t *testing.T) {
	exists := cr("lockfile", model.EventPreToolUse, func(r *model.Rule) {
		r.When = []model.Cond{{PathExists: &model.PathArg{Path: "present.txt"}}}
	})
	e := newEnv(t, rset("g", exists))
	if err := os.MkdirAll(e.workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.workdir, "present.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	x := newTx("s", e.workdir).user("go").tool("t1", "Bash", map[string]any{"command": "ls"})
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "s.jsonl")), Options{})
	if smp := samplesOf(rep, "lockfile"); len(smp) != 1 || smp[0].Class != Approximate {
		t.Errorf("samples = %+v", smp)
	}
}

func TestRunEngineFailureYieldsUnknown(t *testing.T) {
	e := newEnv(t, rset("g", readBeforeEdit()))
	e.engine.err = errors.New("engine exploded")
	x := newTx("s", e.workdir).user("go").tool("t1", "Edit", map[string]any{"file_path": e.workdir + "/a.go"})
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "s.jsonl")), Options{})
	if got := counts(summary(t, rep, "read-before-edit")); got != [4]int{0, 0, 0, 1} {
		t.Errorf("counts = %v", got)
	}
	if smp := samplesOf(rep, "read-before-edit"); len(smp) != 1 || smp[0].UnknownCause != "evaluate_error" {
		t.Errorf("samples = %+v", smp)
	}
}

func TestRunUnknownSamplesAreCapped(t *testing.T) {
	e := newEnv(t, rset("g", readBeforeEdit()))
	e.engine.err = errors.New("nope")
	x := newTx("s", e.workdir)
	for i := 0; i < 120; i++ {
		x.tool(fmt.Sprintf("t%d", i), "Edit", map[string]any{"file_path": e.workdir + "/a.go"})
	}
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "s.jsonl")), Options{})
	if got := summary(t, rep, "read-before-edit").Counts[Unknown]; got != 120 {
		t.Errorf("count = %d", got)
	}
	if n := len(samplesOf(rep, "read-before-edit")); n != maxUnknownSamples {
		t.Errorf("kept %d unknown samples, want %d", n, maxUnknownSamples)
	}
}

func TestRunCorruptLinesAreCounted(t *testing.T) {
	e := newEnv(t, rset("g", readBeforeEdit()))
	x := newTx("s", e.workdir)
	x.user("go")
	x.raw(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tr`) // truncated
	x.raw("garbage")
	x.tool("t1", "Edit", map[string]any{"file_path": e.workdir + "/a.go"})
	x.raw(`{"type":"from-the-future"}`)
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "s.jsonl")), Options{})
	if rep.SkippedLines != 2 || rep.Items != 2 {
		t.Errorf("skipped=%d items=%d", rep.SkippedLines, rep.Items)
	}
	if got := counts(summary(t, rep, "read-before-edit")); got != [4]int{1, 0, 0, 0} {
		t.Errorf("counts = %v", got)
	}
}

func TestRunTranscriptRefAndStableIDs(t *testing.T) {
	e := newEnv(t, rset("g", readBeforeEdit()))
	x := newTx("s", e.workdir).tool("t1", "Edit", map[string]any{"file_path": e.workdir + "/a.go"})

	inHome := x.write(t, filepath.Join(e.home, ".claude", "projects", "p", "s.jsonl"))
	r1 := mustRun(t, e, inHome, Options{})
	r2 := mustRun(t, e, inHome, Options{})
	if r1.Transcript != "~/.claude/projects/p/s.jsonl" {
		t.Errorf("ref = %q", r1.Transcript)
	}
	if !reflect.DeepEqual(r1.Samples, r2.Samples) || len(r1.Samples) != 1 {
		t.Errorf("runs differ: %+v vs %+v", r1.Samples, r2.Samples)
	}

	outside := x.write(t, filepath.Join(e.sb.Root, "elsewhere", "s.jsonl"))
	r3 := mustRun(t, e, outside, Options{})
	if r3.Transcript != filepath.ToSlash(outside) {
		t.Errorf("ref = %q", r3.Transcript)
	}
	if r3.Samples[0].ID == r1.Samples[0].ID {
		t.Error("sample id ignores the transcript ref")
	}

	// A different rules hash changes the ids.
	e2 := newEnv(t, rset("other-gen", readBeforeEdit()))
	r4 := mustRun(t, e2, inHome, Options{})
	if r4.Samples[0].ID == r1.Samples[0].ID {
		t.Error("sample id ignores the rules hash")
	}
}

func TestSampleIDFormula(t *testing.T) {
	// printf 'a\0b\0c\0d' | shasum -a 256 | cut -c1-16
	if got := SampleID("a", "b", "c", "d"); got != sampleIDWant {
		t.Errorf("SampleID = %s, want %s", got, sampleIDWant)
	}
	base := SampleID("t", "u", "r", "h")
	for _, v := range []string{SampleID("t2", "u", "r", "h"), SampleID("t", "u2", "r", "h"), SampleID("t", "u", "r2", "h"), SampleID("t", "u", "r", "h2")} {
		if v == base {
			t.Error("a component is ignored")
		}
	}
	// NUL separators prevent boundary shifting.
	if SampleID("ab", "c", "d", "e") == SampleID("a", "bc", "d", "e") {
		t.Error("components are not delimited")
	}
}

func TestRunLabelsAndPrecision(t *testing.T) {
	approx := cr("claims-done", model.EventStop, func(r *model.Rule) {
		r.Match = &model.Match{Text: &model.TextMatch{Regex: model.StringList{"done"}}}
	})
	e := newEnv(t, rset("g", readBeforeEdit(), approx))
	x := newTx("s", e.workdir)
	x.user("go")
	for i := 0; i < 5; i++ {
		x.tool(fmt.Sprintf("t%d", i), "Edit", map[string]any{"file_path": fmt.Sprintf("%s/f%d.go", e.workdir, i)})
	}
	x.final("done")
	path := x.write(t, filepath.Join(e.dir, "s.jsonl"))

	rep := mustRun(t, e, path, Options{})
	hits := samplesOf(rep, "read-before-edit")
	stop := samplesOf(rep, "claims-done")
	if len(hits) != 5 || len(stop) != 1 {
		t.Fatalf("hits=%d stop=%d", len(hits), len(stop))
	}

	if err := os.MkdirAll(e.labels, 0o700); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		fmt.Sprintf(`{"sample_id":%q,"label":"tp","note":"real"}`, hits[0].ID),
		fmt.Sprintf(`{"sample_id":%q,"label":"tp"}`, hits[1].ID),
		fmt.Sprintf(`{"sample_id":%q,"label":"fp"}`, hits[2].ID),
		fmt.Sprintf(`{"sample_id":%q,"label":"fp"}`, hits[3].ID),
		fmt.Sprintf(`{"sample_id":%q,"label":"tp"}`, hits[3].ID), // later line wins
		`{"sample_id":"not-in-this-report","label":"tp"}`,
		fmt.Sprintf(`{"sample_id":%q,"label":"maybe"}`, hits[4].ID), // invalid label
		`not json`,
		``,
	}
	if err := os.WriteFile(filepath.Join(e.labels, "read-before-edit.jsonl"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	// A label on an approximate sample is attached but not scored.
	if err := os.WriteFile(filepath.Join(e.labels, "claims-done.jsonl"), []byte(fmt.Sprintf(`{"sample_id":%q,"label":"fp"}`, stop[0].ID)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rep = mustRun(t, e, path, Options{})
	s := summary(t, rep, "read-before-edit")
	if s.Labeled != 4 || s.Precision == nil || *s.Precision != 0.75 {
		t.Errorf("labeled=%d precision=%v", s.Labeled, s.Precision)
	}
	labels := []string{}
	for _, sm := range samplesOf(rep, "read-before-edit") {
		labels = append(labels, sm.Label)
	}
	if !reflect.DeepEqual(labels, []string{"tp", "tp", "fp", "tp", ""}) {
		t.Errorf("labels = %v", labels)
	}
	a := summary(t, rep, "claims-done")
	if a.Labeled != 0 || a.Precision != nil {
		t.Errorf("approximate samples must not be scored: %+v", a)
	}
	if samplesOf(rep, "claims-done")[0].Label != "fp" {
		t.Error("label not attached to the approximate sample")
	}
}

func TestRunInitialState(t *testing.T) {
	prior := cr("again", model.EventPreToolUse, func(r *model.Rule) {
		one := 1.0
		r.When = []model.Cond{{PriorHit: &model.PriorHitArg{Rule: "x-rule", Cmp: model.Cmp{Gte: &one}}}}
	})
	cont := cr("continues", model.EventUserPromptSubmit, func(r *model.Rule) {
		r.When = []model.Cond{{PrevPromptRegex: &model.RegexArg{Regex: "deploy$"}}}
	})
	tools := cr("busy", model.EventPreToolUse, func(r *model.Rule) {
		ten := 10.0
		r.When = []model.Cond{{ToolCountSinceInject: &model.Cmp{Gte: &ten}}}
	})
	rs := rset("g", readBeforeEdit(), prior, cont, tools)
	off := 0
	rs.Settings.Inject = &model.InjectSettings{AnchorEvery: &off, BatchAfterTools: &off}
	e := newEnv(t, rs)
	statePath := filepath.Join(e.sb.Root, "state.json")
	if err := os.WriteFile(statePath, []byte(`{"read":["a.go","/abs/z.go"],"prior_hits":{"x-rule":2},"prev_prompt":"please deploy","tool_count_since_inject":12}`), 0o600); err != nil {
		t.Fatal(err)
	}
	x := newTx("s", e.workdir)
	x.user("and now?")
	x.tool("t1", "Edit", map[string]any{"file_path": e.workdir + "/a.go"})   // seeded read (relative to cwd): miss
	x.tool("t2", "Edit", map[string]any{"file_path": "/abs/z.go"})           // seeded read: miss
	x.tool("t3", "Edit", map[string]any{"file_path": e.workdir + "/new.go"}) // hit
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "s.jsonl")), Options{InitialState: statePath})
	if got := counts(summary(t, rep, "read-before-edit")); got != [4]int{1, 2, 0, 0} {
		t.Errorf("read-before-edit = %v", got)
	}
	if got := counts(summary(t, rep, "again")); got != [4]int{3, 0, 0, 0} {
		t.Errorf("again = %v", got)
	}
	if got := counts(summary(t, rep, "continues")); got != [4]int{1, 0, 0, 0} {
		t.Errorf("continues = %v", got)
	}
	if got := counts(summary(t, rep, "busy")); got != [4]int{3, 0, 0, 0} {
		t.Errorf("busy = %v", got)
	}

	// Seeds apply to the main agent of every session.
	y := newTx("other", e.workdir)
	y.tool("o1", "Edit", map[string]any{"file_path": e.workdir + "/a.go"})
	rep = mustRun(t, newEnv2(t, e, rs), y.write(t, filepath.Join(e.dir, "o.jsonl")), Options{InitialState: statePath})
	if got := counts(summary(t, rep, "read-before-edit")); got != [4]int{0, 1, 0, 0} {
		t.Errorf("other session = %v", got)
	}
}

// newEnv2 returns a fresh engine/loader over the same sandbox.
func newEnv2(t testing.TB, e *env, rs *model.CompiledRuleSet) *env {
	t.Helper()
	e2 := *e
	e2.loader = &fakeLoader{rs: rs}
	e2.engine = &miniEngine{}
	e2.deps.Rules = e2.loader
	e2.deps.Engine = e2.engine
	return &e2
}

func TestRunInitialStateRejectsOtherKeys(t *testing.T) {
	cases := map[string]string{
		"exists":          `{"exists":["/a"]}`,
		"verify_fresh":    `{"verify_fresh":"true"}`,
		"env":             `{"env":{"CI":"1"}}`,
		"branch":          `{"branch":"main"}`,
		"failed":          `{"failed_since_read":["a"]}`,
		"cwd":             `{"cwd":"/x"}`,
		"home":            `{"home":"/x"}`,
		"goos":            `{"goos":"windows"}`,
		"not_a_repo":      `{"not_a_repo":true}`,
		"empty exists":    `{"exists":[]}`,
		"mixed":           `{"read":["a"],"env":{}}`,
		"unknown key":     `{"bogus":1}`,
		"array":           `[]`,
		"string":          `"read"`,
		"garbage":         `{`,
		"wrong type read": `{"read":"a.go"}`,
		"wrong type n":    `{"tool_count_since_inject":"x"}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, rset("g", readBeforeEdit()))
			p := filepath.Join(e.sb.Root, "s.json")
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			x := newTx("s", e.workdir).user("hi")
			path := x.write(t, filepath.Join(e.dir, "s.jsonl"))
			_, err := Run(Options{Transcript: path, InitialState: p}, e.deps)
			if !errors.Is(err, ErrUsage) {
				t.Fatalf("err = %v, want a usage error", err)
			}
			if len(e.loader.reqs) != 0 || e.engine.calls != 0 {
				t.Error("the replay started although the state file was rejected")
			}
		})
	}
	for _, ok := range []string{`{}`, `{"read":[]}`, `{"prior_hits":{"a-b":1},"prev_prompt":"x","tool_count_since_inject":0}`} {
		e := newEnv(t, rset("g", readBeforeEdit()))
		p := filepath.Join(e.sb.Root, "s.json")
		_ = os.WriteFile(p, []byte(ok), 0o600)
		x := newTx("s", e.workdir).user("hi")
		if _, err := Run(Options{Transcript: x.write(t, filepath.Join(e.dir, "s.jsonl")), InitialState: p}, e.deps); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	// A missing file is an I/O error, not a usage error.
	e := newEnv(t, rset("g", readBeforeEdit()))
	_, err := Run(Options{Transcript: "x", InitialState: filepath.Join(e.sb.Root, "missing.json")}, e.deps)
	if err == nil || errors.Is(err, ErrUsage) || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing state file: %v", err)
	}
}

func TestRunShellReadsViaSharedFunction(t *testing.T) {
	useFakeShellReads(t)
	rbe := cr("read-before-edit", model.EventPreToolUse, func(r *model.Rule) {
		r.Match = &model.Match{Tool: model.StringList{"Edit"}}
		r.When = []model.Cond{{Not: &model.Cond{ReadThisSession: &model.PathArg{Path: model.TargetBinding}}}}
	})
	build := func(e *env, cmd string, withResult bool) string {
		cwd := e.workdir
		lines := []string{
			fmt.Sprintf(`{"timestamp":"2026-10-10T09:00:00Z","type":"session_meta","payload":{"id":"cx","cwd":%q}}`, cwd),
			`{"timestamp":"2026-10-10T09:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"edit a.go"}]}}`,
			fmt.Sprintf(`{"timestamp":"2026-10-10T09:00:02Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":%q,"call_id":"c1"}}`, mustJSON(t, map[string]string{"cmd": cmd})),
		}
		if withResult {
			lines = append(lines, `{"timestamp":"2026-10-10T09:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"package a"}}`)
		}
		patch := "*** Begin Patch\n*** Update File: " + cwd + "/a.go\n*** End Patch"
		lines = append(lines, fmt.Sprintf(`{"timestamp":"2026-10-10T09:00:04Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"c2","input":%q}}`, patch))
		p := filepath.Join(e.dir, "rollout.jsonl")
		if err := os.MkdirAll(e.dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name       string
		cmd        string
		withResult bool
		want       [4]int
	}{
		{"sed -n then patch: read registered", "sed -n 1,80p a.go", true, [4]int{0, 2, 0, 0}},
		{"cat absolute path", "cat WORKDIR/a.go", true, [4]int{0, 2, 0, 0}},
		{"no tool_result: the read cannot be assumed", "sed -n 1,80p a.go", false, [4]int{1, 1, 0, 0}},
		{"other file", "sed -n 1,80p b.go", true, [4]int{1, 1, 0, 0}},
		{"cd drops relative reads", "cd sub && cat a.go", true, [4]int{1, 1, 0, 0}},
		{"cd keeps absolute reads", "cd sub && cat WORKDIR/a.go", true, [4]int{0, 2, 0, 0}},
		{"unparsable command registers nothing", "BROKEN( cat a.go", true, [4]int{1, 1, 0, 0}},
		{"not a read", "ls a.go", true, [4]int{1, 1, 0, 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, rset("g", rbe))
			cmd := strings.ReplaceAll(tc.cmd, "WORKDIR", e.workdir)
			rep := mustRun(t, e, build(e, cmd, tc.withResult), Options{Client: model.ClientCodex})
			if got := counts(summary(t, rep, "read-before-edit")); got != tc.want {
				t.Errorf("counts = %v, want %v", got, tc.want)
			}
			if rep.Client != model.ClientCodex {
				t.Errorf("client = %s", rep.Client)
			}
		})
	}
}

func TestRunClientRuleFilter(t *testing.T) {
	codexOnly := cr("codex-only", model.EventPreToolUse, func(r *model.Rule) { r.Clients = []model.ClientID{model.ClientCodex} })
	e := newEnv(t, rset("g", readBeforeEdit(), codexOnly))
	x := newTx("s", e.workdir).user("x")
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "s.jsonl")), Options{})
	if len(rep.Rules) != 1 || rep.Rules[0].RuleID != "read-before-edit" {
		t.Errorf("rules = %+v", rep.Rules)
	}
}

func TestRunRulesFileOverridesLoader(t *testing.T) {
	e := newEnv(t, rset("loader-gen", readBeforeEdit()))
	rules := `{"version":1,"rules":[
	  {"id":"custom-rule","action":"warn","event":"PreToolUse","match":{"tool":"Bash"},"examples":[]},
	  {"id":"disabled-one","enabled":false,"event":"PreToolUse","examples":[]}]}`
	rp := filepath.Join(e.sb.Root, "rules.json")
	if err := os.WriteFile(rp, []byte(rules), 0o600); err != nil {
		t.Fatal(err)
	}
	x := newTx("s", e.workdir).user("go").tool("t1", "Bash", map[string]any{"command": "ls"})
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "s.jsonl")), Options{RulesPath: rp})
	if len(e.loader.reqs) != 0 {
		t.Error("loader used although --rules was given")
	}
	if len(rep.Rules) != 1 || rep.Rules[0].RuleID != "custom-rule" || rep.Rules[0].Counts[KnownHit] != 1 {
		t.Errorf("rules = %+v", rep.Rules)
	}
	if len(rep.RulesHash) != 16 || rep.RulesHash == "loader-gen" {
		t.Errorf("rules hash = %q", rep.RulesHash)
	}
	// Same file => same hash; a changed file => different hash.
	again := mustRun(t, e, filepath.Join(e.dir, "s.jsonl"), Options{RulesPath: rp})
	if again.RulesHash != rep.RulesHash {
		t.Error("hash is not stable")
	}
	if err := os.WriteFile(rp, []byte(strings.ReplaceAll(rules, "warn", "deny")), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed := mustRun(t, e, filepath.Join(e.dir, "s.jsonl"), Options{RulesPath: rp}); changed.RulesHash == rep.RulesHash {
		t.Error("hash ignores the file contents")
	}
	// Broken rule files are usage errors; a missing one is an I/O error.
	if err := os.WriteFile(rp, []byte(`{nope`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(Options{Transcript: filepath.Join(e.dir, "s.jsonl"), RulesPath: rp}, e.deps); !errors.Is(err, ErrUsage) {
		t.Errorf("broken rules: %v", err)
	}
	if _, err := Run(Options{Transcript: filepath.Join(e.dir, "s.jsonl"), RulesPath: rp + ".missing"}, e.deps); err == nil || errors.Is(err, ErrUsage) {
		t.Errorf("missing rules: %v", err)
	}
}

func TestRunErrors(t *testing.T) {
	e := newEnv(t, rset("g", readBeforeEdit()))
	x := newTx("s", e.workdir).user("hi")
	path := x.write(t, filepath.Join(e.dir, "s.jsonl"))

	usage := map[string]Options{
		"no transcript":      {},
		"bad format":         {Transcript: path, Format: "yaml"},
		"bad evidence mode":  {Transcript: path, EvidenceMode: "magic"},
		"evidence file need": {Transcript: path, EvidenceMode: "file"},
		"hermes client":      {Transcript: path, Client: model.ClientHermes},
		"bogus client":       {Transcript: path, Client: "emacs"},
	}
	for name, opt := range usage {
		if _, err := Run(opt, e.deps); !errors.Is(err, ErrUsage) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	for _, f := range []string{"", "json", "md"} {
		if _, err := Run(Options{Transcript: path, Format: f}, e.deps); err != nil {
			t.Errorf("format %q: %v", f, err)
		}
	}

	_, err := Run(Options{Transcript: filepath.Join(e.dir, "missing.jsonl")}, e.deps)
	if err == nil || errors.Is(err, ErrUsage) || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing transcript: %v", err)
	}

	e.loader.err = errors.New("rules unreadable")
	if _, err := Run(Options{Transcript: path}, e.deps); err == nil || !strings.Contains(err.Error(), "rules unreadable") {
		t.Errorf("loader error: %v", err)
	}

	for name, mut := range map[string]func(*Deps){
		"fs": func(d *Deps) { d.FS = nil }, "engine": func(d *Deps) { d.Engine = nil },
		"adapters": func(d *Deps) { d.Adapters = nil }, "paths": func(d *Deps) { d.Paths = nil },
		"rules": func(d *Deps) { d.Rules = nil }, "clock": func(d *Deps) { d.Clock = nil },
	} {
		d := newEnv(t, rset("g", readBeforeEdit())).deps
		mut(&d)
		if _, err := Run(Options{Transcript: path}, d); err == nil {
			t.Errorf("missing %s accepted", name)
		}
	}
}

func TestRunEmptyTranscriptReportsRules(t *testing.T) {
	e := newEnv(t, rset("g", readBeforeEdit()))
	p := filepath.Join(e.dir, "empty.jsonl")
	_ = os.MkdirAll(e.dir, 0o700)
	_ = os.WriteFile(p, nil, 0o600)
	rep := mustRun(t, e, p, Options{})
	if rep.Items != 0 || len(rep.Rules) != 1 || rep.Samples == nil || len(rep.Samples) != 0 {
		t.Errorf("rep = %+v", rep)
	}
	if rep.RulesHash != "g" {
		t.Errorf("hash = %q", rep.RulesHash)
	}
}

func TestReportJSONShape(t *testing.T) {
	e := newEnv(t, rset("g", readBeforeEdit()))
	x := newTx("s", e.workdir).tool("t1", "Edit", map[string]any{"file_path": e.workdir + "/a.go"})
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "s.jsonl")), Options{})
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"v", "transcript", "client", "rules_hash", "items", "skipped_lines", "rules", "samples"} {
		if _, ok := generic[k]; !ok {
			t.Errorf("missing key %q in %s", k, data)
		}
	}
	rule := generic["rules"].([]any)[0].(map[string]any)
	counts := rule["counts"].(map[string]any)
	for _, k := range []string{"known-hit", "known-miss", "unknown", "approximate"} {
		if _, ok := counts[k]; !ok {
			t.Errorf("counts lacks %q", k)
		}
	}
	if _, has := rule["precision"]; has {
		t.Error("precision present without labels")
	}
	var strict Report
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&strict); err != nil || !reflect.DeepEqual(strict.Samples, rep.Samples) {
		t.Errorf("round trip: %v", err)
	}
}

func TestRunConcurrent(t *testing.T) {
	base := newEnv(t, rset("g", readBeforeEdit()))
	x := newTx("s", base.workdir)
	x.user("go")
	for i := 0; i < 50; i++ {
		x.tool(fmt.Sprintf("t%d", i), "Edit", map[string]any{"file_path": fmt.Sprintf("%s/f%d.go", base.workdir, i%7)}).result(fmt.Sprintf("t%d", i), "ok", false)
	}
	path := x.write(t, filepath.Join(base.dir, "s.jsonl"))
	want := mustRun(t, base, path, Options{})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := newEnv(t, rset("g", readBeforeEdit()))
			for j := 0; j < 5; j++ {
				rep, err := Run(Options{Transcript: path}, e.deps)
				if err != nil {
					t.Errorf("Run: %v", err)
					return
				}
				if !reflect.DeepEqual(rep.Rules, want.Rules) || len(rep.Samples) != len(want.Samples) {
					t.Errorf("result differs under concurrency")
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestRunClaudeRulesPerStoreCapMimic(t *testing.T) {
	// The read set is FIFO-bounded like the live store (4096 paths).
	e := newEnv(t, rset("g", readBeforeEdit()))
	x := newTx("s", e.workdir)
	for i := 0; i < maxReadPaths+10; i++ {
		id := fmt.Sprintf("r%d", i)
		x.tool(id, "Read", map[string]any{"file_path": fmt.Sprintf("%s/f%d.go", e.workdir, i)}).result(id, "x", false)
	}
	x.tool("e0", "Edit", map[string]any{"file_path": e.workdir + "/f0.go"})                                // evicted: hit
	x.tool("e1", "Edit", map[string]any{"file_path": fmt.Sprintf("%s/f%d.go", e.workdir, maxReadPaths+9)}) // recent: miss
	rep := mustRun(t, e, x.write(t, filepath.Join(e.dir, "s.jsonl")), Options{})
	if got := counts(summary(t, rep, "read-before-edit")); got != [4]int{1, 4107, 0, 0} {
		t.Errorf("counts = %v", got)
	}
}

// printf 'a\0b\0c\0d' | shasum -a 256 | cut -c1-16
const sampleIDWant = "cf92326f85f50aa8"

func TestRunTwentyLabelsAllResolve(t *testing.T) {
	e := newEnv(t, rset("g", readBeforeEdit()))
	x := newTx("s", e.workdir)
	for i := 0; i < 25; i++ {
		x.tool(fmt.Sprintf("t%d", i), "Edit", map[string]any{"file_path": fmt.Sprintf("%s/f%d.go", e.workdir, i)})
	}
	path := x.write(t, filepath.Join(e.dir, "s.jsonl"))
	rep := mustRun(t, e, path, Options{})
	hits := samplesOf(rep, "read-before-edit")
	if len(hits) != 25 {
		t.Fatalf("hits = %d", len(hits))
	}
	var lines []string
	for i, s := range hits {
		label := "tp"
		if i%5 == 4 {
			label = "fp"
		}
		lines = append(lines, fmt.Sprintf(`{"sample_id":%q,"label":%q,"note":"n%d"}`, s.ID, label, i))
	}
	if err := os.MkdirAll(e.labels, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.labels, "read-before-edit.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep = mustRun(t, e, path, Options{})
	resolved := 0
	for _, s := range samplesOf(rep, "read-before-edit") {
		if s.Label != "" {
			resolved++
		}
	}
	if resolved != 25 {
		t.Errorf("%d of 25 labels resolved to report samples", resolved)
	}
	if s := summary(t, rep, "read-before-edit"); s.Labeled != 25 || s.Precision == nil || *s.Precision != 0.8 {
		t.Errorf("summary = %+v", s)
	}
}

func TestRunSubagentFileWithoutAgentIDInRecords(t *testing.T) {
	isSub := cr("sub-edit", model.EventPreToolUse, func(r *model.Rule) {
		r.Match = &model.Match{Tool: model.StringList{"Edit"}}
		r.When = []model.Cond{{IsSubagent: &model.Empty{}}}
	})
	e := newEnv(t, rset("g", isSub))
	main := newTx("S", e.workdir).user("go")
	path := main.write(t, filepath.Join(e.dir, "S.jsonl"))
	for _, id := range []string{"a1", "a2"} {
		sub := newTx("S", e.workdir)
		sub.side = true // isSidechain without agentId: the file name names the agent
		sub.tool("x"+id, "Edit", map[string]any{"file_path": e.workdir + "/a.go"})
		sub.write(t, filepath.Join(e.dir, "S", "subagents", "agent-"+id+".jsonl"))
	}
	rep := mustRun(t, e, path, Options{Subagents: true})
	if got := counts(summary(t, rep, "sub-edit")); got != [4]int{2, 0, 0, 0} {
		t.Errorf("counts = %v", got)
	}
}

func TestScopePendingBounded(t *testing.T) {
	sc := &scope{pending: map[string]pendingCall{}}
	// Resolved calls must neither count toward the bound nor grow order unboundedly.
	for i := 0; i < 20*maxPending; i++ {
		id := fmt.Sprintf("r%d", i)
		sc.addPending(id, pendingCall{})
		delete(sc.pending, id)
	}
	if len(sc.order) > 4*maxPending {
		t.Errorf("order grew to %d with nothing outstanding", len(sc.order))
	}
	for i := 0; i < maxPending; i++ {
		sc.addPending(fmt.Sprintf("u%d", i), pendingCall{})
	}
	if len(sc.pending) != maxPending {
		t.Fatalf("pending = %d, want %d (resolved ids must not evict live ones)", len(sc.pending), maxPending)
	}
	// Overflow evicts the oldest unresolved call and keeps the bound.
	for i := 0; i < 3*maxPending; i++ {
		sc.addPending(fmt.Sprintf("o%d", i), pendingCall{})
		if len(sc.pending) > maxPending {
			t.Fatalf("pending = %d exceeds %d", len(sc.pending), maxPending)
		}
	}
	if _, ok := sc.pending["u0"]; ok {
		t.Error("oldest call survived")
	}
	if _, ok := sc.pending[fmt.Sprintf("o%d", 3*maxPending-1)]; !ok {
		t.Error("newest call evicted")
	}
}

func TestRunEvidenceUsesWritesAsOfEvent(t *testing.T) {
	e := newEnv(t, rset("g", cr("sub-verify", model.EventSubagentStop, func(r *model.Rule) {
		r.When = []model.Cond{{Not: &model.Cond{VerifyFresh: &model.VerifyFreshArg{Names: []string{"test"}}}}}
	})))
	main := newTx("S", e.workdir)
	main.user("delegate")
	main.tool("m1", "Task", map[string]any{"description": "d"})
	main.ts = main.ts.Add(100 * time.Second) // the main agent writes long after the sub-agent finished
	main.tool("m2", "Edit", map[string]any{"file_path": e.workdir + "/late.go"}).result("m2", "ok", false)
	path := main.write(t, filepath.Join(e.dir, "S.jsonl"))

	sub := newTx("S", e.workdir)
	sub.agent, sub.side = "ag1", true
	sub.user("sub task")
	sub.tool("s1", "Edit", map[string]any{"file_path": e.workdir + "/a.go"}).result("s1", "ok", false) // 09:00:03
	sub.final("done")                                                                                  // 09:00:04
	sub.write(t, filepath.Join(e.dir, "S", "subagents", "agent-ag1.jsonl"))

	base := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	rec := model.VerifyRecord{V: 1, JobID: "j1", Name: "test", RepoInst: "ri", Tier: model.TierT0, SStart: "T", SEnd: "T",
		TStart: base.Add(3200 * time.Millisecond), TEnd: base.Add(3500 * time.Millisecond), Outcome: model.OutcomePass, SessionID: "S"}
	dir := filepath.Join(e.evDir, "ri", "test")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(mustJSON(t, rec)), 0o600); err != nil {
		t.Fatal(err)
	}

	rep := mustRun(t, e, path, Options{Subagents: true})
	if smp := samplesOf(rep, "sub-verify"); len(smp) != 0 {
		t.Errorf("a pass after the sub-agent's last write is fresh at that moment: %+v", smp)
	}
	if got := counts(summary(t, rep, "sub-verify")); got != [4]int{0, 0, 1, 0} {
		t.Errorf("counts = %v", got)
	}
}

// A sub-agent write that happens after the last verify pass makes a LATER
// main-agent event stale, although sub-agent streams are replayed after the
// main one (contract v1.3, review u9 minor).
func TestRunMainEventSeesEarlierSubagentWrites(t *testing.T) {
	rule := cr("stop-verify", model.EventStop, func(r *model.Rule) {
		r.When = []model.Cond{{Not: &model.Cond{VerifyFresh: &model.VerifyFreshArg{Names: []string{"test"}}}}}
	})
	base := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	run := func(t *testing.T, subWrites bool) *Report {
		e := newEnv(t, rset("g", rule))
		main := newTx("S", e.workdir)
		main.user("delegate")
		main.tool("m1", "Task", map[string]any{"description": "d"})
		main.ts = main.ts.Add(100 * time.Second) // the main agent stops long after the sub-agent
		main.final("all done")
		path := main.write(t, filepath.Join(e.dir, "S.jsonl"))

		sub := newTx("S", e.workdir)
		sub.agent, sub.side = "ag1", true
		sub.user("sub task")
		if subWrites {
			sub.tool("s1", "Edit", map[string]any{"file_path": e.workdir + "/a.go"}).result("s1", "ok", false) // 09:00:03
		} else {
			sub.tool("s1", "Read", map[string]any{"file_path": e.workdir + "/a.go"}).result("s1", "ok", false)
		}
		sub.final("done")
		sub.write(t, filepath.Join(e.dir, "S", "subagents", "agent-ag1.jsonl"))

		rec := model.VerifyRecord{V: 1, JobID: "j1", Name: "test", RepoInst: "ri", Tier: model.TierT0, SStart: "T", SEnd: "T",
			TStart: base.Add(1200 * time.Millisecond), TEnd: base.Add(1500 * time.Millisecond), Outcome: model.OutcomePass, SessionID: "S"}
		dir := filepath.Join(e.evDir, "ri", "test")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(mustJSON(t, rec)), 0o600); err != nil {
			t.Fatal(err)
		}
		return mustRun(t, e, path, Options{Subagents: true})
	}
	if smp := samplesOf(run(t, true), "stop-verify"); len(smp) != 1 {
		t.Errorf("a sub-agent write after the pass must make the main Stop stale: %+v", smp)
	}
	if smp := samplesOf(run(t, false), "stop-verify"); len(smp) != 0 {
		t.Errorf("without a write the pass stays fresh: %+v", smp)
	}
}

func TestRunVerifyFreshWithoutNamesUsesProjectRequired(t *testing.T) {
	rule := cr("verify-all", model.EventStop, func(r *model.Rule) {
		r.When = []model.Cond{{Not: &model.Cond{VerifyFresh: &model.VerifyFreshArg{}}}}
	})
	base := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	run := func(t *testing.T, project string) *Report {
		e := newEnv(t, rset("g", rule))
		if err := os.MkdirAll(filepath.Join(e.workdir, ".ballast"), 0o700); err != nil {
			t.Fatal(err)
		}
		if project != "" {
			if err := os.WriteFile(filepath.Join(e.workdir, ".ballast", "project.json"), []byte(project), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		// Only "lint" ever passed.
		rec := model.VerifyRecord{V: 1, JobID: "j1", Name: "lint", RepoInst: "ri", Tier: model.TierT0, SStart: "T", SEnd: "T",
			TStart: base.Add(time.Second), TEnd: base.Add(2 * time.Second), Outcome: model.OutcomePass, SessionID: "S"}
		dir := filepath.Join(e.evDir, "ri", "lint")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(mustJSON(t, rec)), 0o600); err != nil {
			t.Fatal(err)
		}
		x := newTx("S", e.workdir)
		x.user("go").final("done")
		return mustRun(t, e, x.write(t, filepath.Join(e.dir, "S.jsonl")), Options{})
	}
	req := func(names ...string) string {
		var vs []model.VerifySpec
		for _, n := range names {
			vs = append(vs, model.VerifySpec{Name: n, Argv: []string{"x"}, Required: true})
		}
		return mustJSON(t, model.ProjectConfig{Version: 1, Verify: vs})
	}
	t.Run("required check that never ran is not fresh", func(t *testing.T) {
		rep := run(t, req("lint", "test"))
		if got := counts(summary(t, rep, "verify-all")); got != [4]int{0, 0, 1, 0} {
			t.Errorf("counts = %v", got)
		}
		if smp := samplesOf(rep, "verify-all"); len(smp) != 1 || smp[0].Class != Approximate {
			t.Errorf("samples = %+v", smp)
		}
	})
	t.Run("all required checks passed", func(t *testing.T) {
		rep := run(t, req("lint"))
		if got := counts(summary(t, rep, "verify-all")); got != [4]int{0, 0, 1, 0} {
			t.Errorf("counts = %v", got)
		}
		if smp := samplesOf(rep, "verify-all"); len(smp) != 0 {
			t.Errorf("samples = %+v", smp)
		}
	})
	for name, project := range map[string]string{"no project.json": "", "invalid project.json": "{", "no required entries": `{"verify":[{"name":"x","argv":["x"]}]}`} {
		t.Run(name, func(t *testing.T) {
			rep := run(t, project)
			if got := counts(summary(t, rep, "verify-all")); got != [4]int{0, 0, 0, 1} {
				t.Errorf("counts = %v (the required list is unknown: unknown, not approximate)", got)
			}
		})
	}
}

// strictProject stands in for U8's project.Loader: unknown keys are invalid.
type strictProject struct{ roots *[]string }

func (p strictProject) LoadProject(root string) (*model.ProjectConfig, error) {
	*p.roots = append(*p.roots, root)
	data, err := os.ReadFile(filepath.Join(root, ".ballast", "project.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var pc model.ProjectConfig
	if err := dec.Decode(&pc); err != nil {
		return nil, err
	}
	return &pc, nil
}

// With Deps.Project injected, project.json is classified like the hook's
// evidence provider (contract v1.3, review u9 minor): a file the strict
// loader rejects is project_invalid ⇒ unknown, not a lenient approximate.
func TestRunUsesInjectedProjectLoader(t *testing.T) {
	rule := cr("verify-all", model.EventStop, func(r *model.Rule) {
		r.When = []model.Cond{{Not: &model.Cond{VerifyFresh: &model.VerifyFreshArg{}}}}
	})
	base := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	project := `{"version":1,"verify":[{"name":"lint","argv":["x"],"required":true}],"bogus":1}`
	run := func(t *testing.T, strict bool, body string) (*Report, []string) {
		e := newEnv(t, rset("g", rule))
		var roots []string
		if strict {
			e.deps.Project = strictProject{&roots}
		}
		if err := os.MkdirAll(filepath.Join(e.workdir, ".ballast"), 0o700); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			if err := os.WriteFile(filepath.Join(e.workdir, ".ballast", "project.json"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		rec := model.VerifyRecord{V: 1, JobID: "j1", Name: "lint", RepoInst: "ri", Tier: model.TierT0, SStart: "T", SEnd: "T",
			TStart: base.Add(time.Second), TEnd: base.Add(2 * time.Second), Outcome: model.OutcomePass, SessionID: "S"}
		dir := filepath.Join(e.evDir, "ri", "lint")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(mustJSON(t, rec)), 0o600); err != nil {
			t.Fatal(err)
		}
		x := newTx("S", e.workdir)
		x.user("go").final("done")
		return mustRun(t, e, x.write(t, filepath.Join(e.dir, "S.jsonl")), Options{}), roots
	}
	if rep, _ := run(t, false, project); counts(summary(t, rep, "verify-all")) != [4]int{0, 0, 1, 0} {
		t.Errorf("lenient fallback: counts = %v", counts(summary(t, rep, "verify-all")))
	}
	rep, roots := run(t, true, project)
	if got := counts(summary(t, rep, "verify-all")); got != [4]int{0, 0, 0, 1} {
		t.Errorf("strict loader: counts = %v, want unknown", got)
	}
	if len(roots) != 1 {
		t.Errorf("loader roots = %v", roots)
	}
	if rep, _ := run(t, true, ""); counts(summary(t, rep, "verify-all")) != [4]int{0, 0, 0, 1} {
		t.Errorf("strict loader, no project.json: counts = %v", counts(summary(t, rep, "verify-all")))
	}
	valid := `{"version":1,"verify":[{"name":"lint","argv":["x"],"required":true}]}`
	if rep, _ := run(t, true, valid); counts(summary(t, rep, "verify-all")) != [4]int{0, 0, 1, 0} {
		t.Errorf("strict loader, valid file: counts = %v", counts(summary(t, rep, "verify-all")))
	}
}
