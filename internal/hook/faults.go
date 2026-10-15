package hook

import (
	"fmt"
	"strings"
)

// FaultPoint names an injection point of the pipeline (plan B7 fault tests).
type FaultPoint string

// Fault points in pipeline order.
const (
	FaultParse       FaultPoint = "parse"        // after ParseEvent
	FaultLoad        FaultPoint = "load"         // after Rules.Load
	FaultStateRead   FaultPoint = "state_read"   // after State.Load
	FaultEvaluate    FaultPoint = "evaluate"     // after Engine.Evaluate
	FaultDecided     FaultPoint = "decided"      // Decision formed (e.g. deny), stdout NOT committed
	FaultStateCommit FaultPoint = "state_commit" // instead of State.Commit
	FaultAuditWrite  FaultPoint = "audit_write"  // instead of Audit.Append
	FaultRender      FaultPoint = "render"       // after Render, before the stdout write
)

// AllFaultPoints lists every point (fault.sh iterates it).
var AllFaultPoints = []FaultPoint{
	FaultParse, FaultLoad, FaultStateRead, FaultEvaluate,
	FaultDecided, FaultStateCommit, FaultAuditWrite, FaultRender,
}

// FaultMode is what happens at an armed point.
type FaultMode string

const (
	FaultError FaultMode = "error" // return an error
	FaultPanic FaultMode = "panic" // panic
	FaultHang  FaultMode = "hang"  // block until the watchdog fires
)

// FaultInjector is consulted at every FaultPoint. Hit returns nil when the
// point is not armed; otherwise the pipeline applies the returned mode.
type FaultInjector interface {
	Hit(p FaultPoint) *FaultMode
}

// FaultsFromEnv returns the injector configured by BALLAST_FAULT=<point>:<mode>
// — ONLY in binaries built with `-tags ballast_fault`; production builds
// always return nil (an agent must not be able to disable enforcement via
// the environment).
func FaultsFromEnv(getenv func(string) (string, bool)) FaultInjector {
	return faultsFromEnv(getenv)
}

// parseFaultSpec parses "<point>:<mode>" (the BALLAST_FAULT value).
func parseFaultSpec(spec string) (FaultPoint, FaultMode, error) {
	ps, ms, ok := strings.Cut(strings.TrimSpace(spec), ":")
	if !ok {
		return "", "", fmt.Errorf("fault spec %q: want <point>:<mode>", spec)
	}
	p := FaultPoint(ps)
	known := false
	for _, x := range AllFaultPoints {
		if x == p {
			known = true
		}
	}
	if !known {
		return "", "", fmt.Errorf("fault spec %q: unknown point %q", spec, ps)
	}
	m := FaultMode(ms)
	switch m {
	case FaultError, FaultPanic, FaultHang:
		return p, m, nil
	}
	return "", "", fmt.Errorf("fault spec %q: unknown mode %q", spec, ms)
}

// singleFault arms exactly one point.
type singleFault struct {
	point FaultPoint
	mode  FaultMode
}

func (f singleFault) Hit(p FaultPoint) *FaultMode {
	if p != f.point {
		return nil
	}
	m := f.mode
	return &m
}
