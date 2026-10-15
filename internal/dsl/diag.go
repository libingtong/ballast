package dsl

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// diags accumulates diagnostics for one file.
type diags struct {
	file string
	list []model.Diagnostic
}

func (d *diags) add(sev model.Severity, code, pointer, ruleID, format string, args ...any) {
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	d.list = append(d.list, model.Diagnostic{
		Severity: sev, Code: code, File: d.file, Pointer: pointer, RuleID: ruleID, Message: msg,
	})
}

func (d *diags) err(code, pointer, ruleID, format string, args ...any) {
	d.add(model.SevError, code, pointer, ruleID, format, args...)
}

func (d *diags) warn(code, pointer, ruleID, format string, args ...any) {
	d.add(model.SevWarning, code, pointer, ruleID, format, args...)
}

// ptr builds a JSON pointer from path segments (strings or ints).
func ptr(parts ...any) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteByte('/')
		switch v := p.(type) {
		case int:
			b.WriteString(strconv.Itoa(v))
		case string:
			v = strings.ReplaceAll(v, "~", "~0")
			v = strings.ReplaceAll(v, "/", "~1")
			b.WriteString(v)
		default:
			b.WriteString(fmt.Sprint(v))
		}
	}
	return b.String()
}

// sortDiagnostics orders diagnostics by file, then pointer (numeric segments
// compare numerically), then code, then message.
func sortDiagnostics(ds []model.Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if c := comparePointers(a.Pointer, b.Pointer); c != 0 {
			return c < 0
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Message < b.Message
	})
}

func comparePointers(a, b string) int {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] == bs[i] {
			continue
		}
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		if aerr == nil && berr == nil {
			if an < bn {
				return -1
			}
			return 1
		}
		if as[i] < bs[i] {
			return -1
		}
		return 1
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}
	return 0
}
