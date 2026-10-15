package install

import (
	"bytes"
	"fmt"

	"github.com/libingtong/ballast/internal/model"
)

// mdcMarkerPrefix identifies a ballast-managed .mdc (inject.MDCMarker begins
// with it; install may not import inject).
const mdcMarkerPrefix = "<!-- ballast-managed"

// mdcTarget owns the whole file ~/.cursor/rules/ballast.mdc.
type mdcTarget struct {
	path string
}

func (t *mdcTarget) Path() string   { return t.path }
func (t *mdcTarget) Format() Format { return FormatMDC }

// Plan implements Target: After is spec.MDC.
func (t *mdcTarget) Plan(cur []byte, spec EntrySpec, m *model.InstallManifest) (*PlanResult, error) {
	m = nonNilManifest(m)
	res := &PlanResult{Path: t.path, Format: FormatMDC, Before: cur, After: cur, Created: cur == nil}
	prior := fileRec(m, t.path)
	managed := prior != nil && prior.Whole
	if prior != nil && prior.Created {
		res.Created = true
	}
	record := func(content []byte) {
		res.Files = []model.ManagedFile{{
			Client: model.ClientCursor, Path: t.path, Hash: sha256Hex(content),
			Created: res.Created, Whole: true,
		}}
	}
	if len(spec.MDC) == 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s: no rendered rules supplied (Options.MDC); file not written", t.path))
		if managed && cur != nil {
			record(cur)
		}
		return res, nil
	}
	switch {
	case cur == nil || managed || bytes.Equal(cur, spec.MDC) || bytes.Contains(cur, []byte(mdcMarkerPrefix)):
		res.After = spec.MDC
		record(spec.MDC)
	default:
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s exists and is not managed by ballast; left untouched", t.path))
	}
	return res, nil
}

// Remove implements Target: the managed file is deleted.
func (t *mdcTarget) Remove(cur []byte, m *model.InstallManifest) (*PlanResult, error) {
	m = nonNilManifest(m)
	res := &PlanResult{Path: t.path, Format: FormatMDC, Before: cur, After: cur}
	prior := fileRec(m, t.path)
	if prior == nil || cur == nil {
		return res, nil
	}
	if prior.Hash == sha256Hex(cur) || bytes.Contains(cur, []byte(mdcMarkerPrefix)) {
		res.After = nil
		return res, nil
	}
	res.Warnings = append(res.Warnings, fmt.Sprintf("%s was replaced by the user (no ballast marker); kept", t.path))
	res.Files = []model.ManagedFile{*prior}
	return res, nil
}
