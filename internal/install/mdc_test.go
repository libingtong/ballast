package install

import (
	"strings"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

func gMDCT() *mdcTarget { return &mdcTarget{path: gMDC} }

func TestMDCPlanWritesWholeFile(t *testing.T) {
	spec := gspec(model.ClientCursor)
	p, err := gMDCT().Plan(nil, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.After) != testMDC || !p.Created || !p.Changed() {
		t.Fatalf("after=%q created=%v", p.After, p.Created)
	}
	if len(p.Files) != 1 || !p.Files[0].Whole || !p.Files[0].Created || p.Files[0].Hash != sha256Hex([]byte(testMDC)) || p.Files[0].Client != model.ClientCursor {
		t.Errorf("files = %+v", p.Files)
	}
	if p.Format != FormatMDC || len(p.Entries) != 0 {
		t.Errorf("format=%s entries=%v", p.Format, p.Entries)
	}
}

func TestMDCRegeneratesManagedFile(t *testing.T) {
	spec := gspec(model.ClientCursor)
	p1, _ := gMDCT().Plan(nil, spec, nil)
	m := manifestFrom(gBinary, p1)
	spec.MDC = []byte(strings.Replace(testMDC, "rules v1", "rules v2", 1))
	p2, err := gMDCT().Plan(p1.After, spec, m)
	if err != nil {
		t.Fatal(err)
	}
	if string(p2.After) != string(spec.MDC) || !p2.Created {
		t.Errorf("after=%q created=%v (Created must be remembered)", p2.After, p2.Created)
	}
	// identical content: no change
	p3, _ := gMDCT().Plan(p2.After, spec, manifestFrom(gBinary, p2))
	if p3.Changed() {
		t.Error("regenerating identical content must be a no-op")
	}
	// hand-edited managed file (still carries the marker) is regenerated too
	p4, _ := gMDCT().Plan([]byte(testMDC+"edited\n"), spec, m)
	if string(p4.After) != string(spec.MDC) {
		t.Error("managed file must be overwritten")
	}
}

func TestMDCForeignFileIsNotOverwritten(t *testing.T) {
	spec := gspec(model.ClientCursor)
	foreign := "---\nalwaysApply: true\n---\nmy own ballast.mdc\n"
	p, err := gMDCT().Plan([]byte(foreign), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.After) != foreign || p.Changed() || len(p.Warnings) != 1 || len(p.Files) != 0 {
		t.Errorf("after=%q warnings=%v files=%v", p.After, p.Warnings, p.Files)
	}
	// ... and uninstall leaves it alone because it is not recorded
	r, _ := gMDCT().Remove([]byte(foreign), &model.InstallManifest{})
	if r.Changed() {
		t.Error("unrecorded file must not be removed")
	}
	// a stray ballast-marked file without manifest is adopted
	p, _ = gMDCT().Plan([]byte(testMDC+"old\n"), spec, nil)
	if string(p.After) != testMDC {
		t.Error("marked file should be adopted")
	}
}

func TestMDCWithoutContent(t *testing.T) {
	spec := gspec(model.ClientCursor)
	spec.MDC = nil
	p, _ := gMDCT().Plan(nil, spec, nil)
	if p.After != nil || p.Changed() || len(p.Warnings) != 1 {
		t.Errorf("after=%q warnings=%v", p.After, p.Warnings)
	}
	// an installed file is kept (and stays recorded)
	good := gspec(model.ClientCursor)
	p1, _ := gMDCT().Plan(nil, good, nil)
	p2, _ := gMDCT().Plan(p1.After, spec, manifestFrom(gBinary, p1))
	if string(p2.After) != testMDC || len(p2.Files) != 1 {
		t.Errorf("after=%q files=%v", p2.After, p2.Files)
	}
}

func TestMDCRemove(t *testing.T) {
	spec := gspec(model.ClientCursor)
	p, _ := gMDCT().Plan(nil, spec, nil)
	m := manifestFrom(gBinary, p)
	r, err := gMDCT().Remove(p.After, m)
	if err != nil || r.After != nil || !r.Changed() {
		t.Fatalf("whole file must be deleted: %v %q", err, r.After)
	}
	// missing file is fine
	if r, _ := gMDCT().Remove(nil, m); r.Changed() {
		t.Error("nothing to remove")
	}
	// replaced by the user's own content (no marker, hash differs): kept with a warning
	r, _ = gMDCT().Remove([]byte("mine\n"), m)
	if string(r.After) != "mine\n" || len(r.Warnings) != 1 || len(r.Files) != 1 {
		t.Errorf("after=%q warnings=%v files=%v", r.After, r.Warnings, r.Files)
	}
	// edited but still marked: deleted
	r, _ = gMDCT().Remove([]byte(testMDC+"edit\n"), m)
	if r.After != nil {
		t.Error("marked file is ours even when edited")
	}
}
