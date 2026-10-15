package model

// Action is a rule's enforcement tier (plan B1/B2).
type Action string

const (
	// ActionNone is never written in rules; it is the "no decision" verdict.
	ActionNone Action = ""
	ActionLog  Action = "log"
	ActionWarn Action = "warn"
	ActionAsk  Action = "ask"
	ActionDeny Action = "deny"
)

// RuleActions are the values accepted by Rule.Action / Rule.Target.
var RuleActions = []Action{ActionLog, ActionWarn, ActionAsk, ActionDeny}

// Severity orders actions: none(0) < log(1) < warn(2) < ask(3) < deny(4).
// Unknown strings have severity -1.
func (a Action) Severity() int {
	switch a {
	case ActionNone:
		return 0
	case ActionLog:
		return 1
	case ActionWarn:
		return 2
	case ActionAsk:
		return 3
	case ActionDeny:
		return 4
	}
	return -1
}

// Valid reports whether a may appear in a rule's action/target field.
func (a Action) Valid() bool { return a.Severity() >= 1 }

// Stricter returns the stricter of a and b.
func Stricter(a, b Action) Action {
	if b.Severity() > a.Severity() {
		return b
	}
	return a
}

// WouldLabel returns the audit decision label for a shadow-mode (log) hit,
// e.g. "would_deny". It returns "log" when target is empty or log.
func WouldLabel(target Action) string {
	if target == ActionNone || target == ActionLog {
		return string(ActionLog)
	}
	return "would_" + string(target)
}

// Tri is a three-valued truth value (Kleene logic). The zero value is
// TriUnknown on purpose: anything not positively established fails open.
type Tri int8

const (
	TriUnknown Tri = 0
	TriFalse   Tri = 1
	TriTrue    Tri = 2
)

// TriOf converts a bool.
func TriOf(b bool) Tri {
	if b {
		return TriTrue
	}
	return TriFalse
}

// String implements fmt.Stringer ("true", "false", "unknown").
func (t Tri) String() string {
	switch t {
	case TriTrue:
		return "true"
	case TriFalse:
		return "false"
	}
	return "unknown"
}

// Not is Kleene negation (unknown stays unknown).
func (t Tri) Not() Tri {
	switch t {
	case TriTrue:
		return TriFalse
	case TriFalse:
		return TriTrue
	}
	return TriUnknown
}

// And is Kleene conjunction: false dominates, then unknown.
func (t Tri) And(u Tri) Tri {
	if t == TriFalse || u == TriFalse {
		return TriFalse
	}
	if t == TriTrue && u == TriTrue {
		return TriTrue
	}
	return TriUnknown
}

// Or is Kleene disjunction: true dominates, then unknown.
func (t Tri) Or(u Tri) Tri {
	if t == TriTrue || u == TriTrue {
		return TriTrue
	}
	if t == TriFalse && u == TriFalse {
		return TriFalse
	}
	return TriUnknown
}

// AllOf folds And over ts; the empty conjunction is TriTrue.
func AllOf(ts ...Tri) Tri {
	r := TriTrue
	for _, t := range ts {
		r = r.And(t)
	}
	return r
}

// AnyOf folds Or over ts; the empty disjunction is TriFalse.
func AnyOf(ts ...Tri) Tri {
	r := TriFalse
	for _, t := range ts {
		r = r.Or(t)
	}
	return r
}

// ParseTri parses "true" | "false" | "unknown" (used by example fixtures).
func ParseTri(s string) (Tri, bool) {
	switch s {
	case "true":
		return TriTrue, true
	case "false":
		return TriFalse, true
	case "unknown":
		return TriUnknown, true
	}
	return TriUnknown, false
}

// Cmp is the comparison syntax of the DSL: {"gte": 60}, {"lt": 0.3}, ...
// Several keys combine with AND ({"gte":1,"lt":5} is a range). A Cmp with no
// key set is invalid (rejected by validation).
type Cmp struct {
	Eq  *float64 `json:"eq,omitempty"`
	Ne  *float64 `json:"ne,omitempty"`
	Gt  *float64 `json:"gt,omitempty"`
	Gte *float64 `json:"gte,omitempty"`
	Lt  *float64 `json:"lt,omitempty"`
	Lte *float64 `json:"lte,omitempty"`
}

// Empty reports whether no comparison key is set.
func (c Cmp) Empty() bool {
	return c.Eq == nil && c.Ne == nil && c.Gt == nil && c.Gte == nil && c.Lt == nil && c.Lte == nil
}

// Eval returns whether x satisfies every key of c.
func (c Cmp) Eval(x float64) bool {
	if c.Eq != nil && !(x == *c.Eq) {
		return false
	}
	if c.Ne != nil && !(x != *c.Ne) {
		return false
	}
	if c.Gt != nil && !(x > *c.Gt) {
		return false
	}
	if c.Gte != nil && !(x >= *c.Gte) {
		return false
	}
	if c.Lt != nil && !(x < *c.Lt) {
		return false
	}
	if c.Lte != nil && !(x <= *c.Lte) {
		return false
	}
	return true
}

// EvalTri lifts Eval to three values: when known is false the result is unknown.
func (c Cmp) EvalTri(x float64, known bool) Tri {
	if !known {
		return TriUnknown
	}
	return TriOf(c.Eval(x))
}
