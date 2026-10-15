package hook

import (
	"strings"
	"testing"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

// pointFault arms exactly one (point, mode).
type pointFault struct {
	point FaultPoint
	mode  FaultMode
}

func (f pointFault) Hit(p FaultPoint) *FaultMode {
	if p != f.point {
		return nil
	}
	m := f.mode
	return &m
}

var allFaultModes = []FaultMode{FaultError, FaultPanic, FaultHang}

// sideEffects documents what has already happened when each point fires.
type sideEffects struct {
	evaluated bool
	committed bool
	audited   bool
}

func faultEffects(p FaultPoint) sideEffects {
	switch p {
	case FaultParse, FaultLoad, FaultStateRead:
		return sideEffects{}
	case FaultEvaluate, FaultDecided, FaultStateCommit:
		return sideEffects{evaluated: true}
	case FaultAuditWrite:
		return sideEffects{evaluated: true, committed: true}
	case FaultRender:
		return sideEffects{evaluated: true, committed: true, audited: true}
	}
	panic("unknown point " + string(p))
}

// TestFaultMatrix is the in-process equivalent of scripts/fault.sh: every
// FaultPoint × {error, panic, hang} leaves stdout empty, exits 1 and writes
// "ballast: ..." to stderr — including FaultDecided when the Decision is
// already a deny.
func TestFaultMatrix(t *testing.T) {
	for _, p := range AllFaultPoints {
		for _, m := range allFaultModes {
			t.Run(string(p)+"/"+string(m), func(t *testing.T) {
				h := newHarness(t)
				h.engine.verdict = denyVerdict()
				h.deps.Faults = pointFault{p, m}
				if m == FaultHang {
					h.deps.Watchdog = 100 * time.Millisecond
				}
				start := time.Now()
				code := h.claude("PreToolUse", preToolBash)
				h.wantFailure(code)
				if !strings.HasPrefix(h.stderr.String(), "ballast: ") {
					t.Errorf("stderr = %q", h.stderr.String())
				}
				exits := h.exitCalls()
				if m == FaultHang {
					if len(exits) != 1 || exits[0] != 1 {
						t.Errorf("hang must end in Exit(1) from the watchdog, got %v", exits)
					}
					if !strings.Contains(h.stderr.String(), "watchdog") {
						t.Errorf("stderr = %q", h.stderr.String())
					}
					if el := time.Since(start); el < 90*time.Millisecond {
						t.Errorf("returned after %v, before the watchdog", el)
					}
				} else if len(exits) != 0 {
					t.Errorf("Exit called without a watchdog expiry: %v", exits)
				}
				eff := faultEffects(p)
				if (h.engine.calls() > 0) != eff.evaluated {
					t.Errorf("evaluated = %v, want %v", h.engine.calls() > 0, eff.evaluated)
				}
				if (h.state.commitCount() > 0) != eff.committed {
					t.Errorf("committed = %v, want %v", h.state.commitCount() > 0, eff.committed)
				}
				if (h.audit.appendCalls() > 0) != eff.audited {
					t.Errorf("audited = %v, want %v", h.audit.appendCalls() > 0, eff.audited)
				}
			})
		}
	}
}

// TestFaultDecidedWithDeny pins the dangerous window: the Decision is a deny,
// nothing was committed, and the exception path must still let the call pass
// (empty stdout).
func TestFaultDecidedWithDeny(t *testing.T) {
	for _, m := range allFaultModes {
		h := newHarness(t)
		h.engine.verdict = denyVerdict()
		h.deps.Faults = pointFault{FaultDecided, m}
		if m == FaultHang {
			h.deps.Watchdog = 100 * time.Millisecond
		}
		var decided *model.Decision
		h.engine.each = func(*model.EvalInput) *model.Verdict {
			decided = &model.Decision{Action: model.ActionDeny}
			return denyVerdict()
		}
		h.wantFailure(h.claude("PreToolUse", preToolBash))
		if decided == nil {
			t.Errorf("%s: the deny verdict must have been formed", m)
		}
		if h.log.index("render") >= 0 || h.state.commitCount() != 0 || h.audit.appendCalls() != 0 {
			t.Errorf("%s: nothing may be committed at FaultDecided: %v", m, h.log.list())
		}
	}
}

// TestFaultMatrixOtherEvents: points are consulted on every event kind that
// runs the full pipeline, not just rule events.
func TestFaultMatrixOtherEvents(t *testing.T) {
	events := []struct {
		native, payload string
	}{
		{"SessionStart", `{"session_id":"s","cwd":"/repo"}`},
		{"UserPromptSubmit", `{"session_id":"s","cwd":"/repo","prompt":"p"}`},
		{"PostToolUse", preToolBash},
		{"Stop", `{"session_id":"s","cwd":"/repo"}`},
		{"SessionEnd", `{"session_id":"s","cwd":"/repo"}`},
	}
	for _, e := range events {
		for _, p := range AllFaultPoints {
			t.Run(e.native+"/"+string(p), func(t *testing.T) {
				h := newHarness(t)
				h.deps.Faults = pointFault{p, FaultError}
				h.state.session = &fakeSession{evals: map[string]model.EvalCount{"r": {Evals: 1}}}
				h.injector.inj = &model.Injection{Text: "ctx"}
				h.wantFailure(h.claude(e.native, e.payload))
			})
		}
	}
	// The compact helper short-circuits after the state read.
	for _, p := range []FaultPoint{FaultParse, FaultLoad, FaultStateRead} {
		h := newHarness(t)
		h.deps.Faults = pointFault{p, FaultPanic}
		h.wantFailure(h.claude("SubagentStop", `{"session_id":"s","cwd":"/repo","agent_id":"a","agent_type":""}`))
	}
}

func TestFaultsOnlyThroughDeps(t *testing.T) {
	// A nil injector or an unarmed point never alters the pipeline.
	h := newHarness(t)
	h.engine.verdict = denyVerdict()
	h.deps.Faults = pointFault{"no-such-point", FaultPanic}
	mustExit0(t, h, h.claude("PreToolUse", preToolBash))
	if !strings.Contains(h.stdout.String(), denyJSON) {
		t.Errorf("stdout = %q", h.stdout.String())
	}
	if got := len(AllFaultPoints); got != 8 {
		t.Errorf("fault points = %d, want 8", got)
	}
	// Unknown modes are an error, not a pass-through.
	h = newHarness(t)
	h.deps.Faults = pointFault{FaultParse, "weird"}
	h.wantFailure(h.claude("PreToolUse", preToolBash))
}

func TestParseFaultSpec(t *testing.T) {
	for _, p := range AllFaultPoints {
		for _, m := range allFaultModes {
			gp, gm, err := parseFaultSpec(string(p) + ":" + string(m))
			if err != nil || gp != p || gm != m {
				t.Errorf("%s:%s → %v %v %v", p, m, gp, gm, err)
			}
		}
	}
	if _, _, err := parseFaultSpec(" decided:panic "); err != nil {
		t.Errorf("surrounding space: %v", err)
	}
	for _, bad := range []string{"", "decided", ":panic", "decided:", "nope:panic", "decided:nope", "decided:panic:x", "DECIDED:panic"} {
		if _, _, err := parseFaultSpec(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	var inj FaultInjector = singleFault{point: FaultRender, mode: FaultHang}
	if m := inj.Hit(FaultRender); m == nil || *m != FaultHang {
		t.Errorf("Hit = %v", m)
	}
	if inj.Hit(FaultParse) != nil {
		t.Error("unarmed point")
	}
}

// TestRunHangWithoutWatchdog: Run called directly must not block forever.
func TestRunHangWithoutWatchdog(t *testing.T) {
	h := newHarness(t)
	h.deps.Watchdog = 20 * time.Millisecond
	h.deps.Faults = pointFault{FaultLoad, FaultHang}
	start := time.Now()
	if _, err := Run(Args{Client: model.ClientClaude, Native: "PreToolUse"}, []byte(preToolBash), h.deps); err == nil {
		t.Fatal("Run must fail")
	}
	if el := time.Since(start); el < 30*time.Millisecond || el > 2*time.Second {
		t.Errorf("hang lasted %v", el)
	}
}
