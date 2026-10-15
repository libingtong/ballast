package bashparse

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

// expectFile is the schema of testdata/expect.json: per CommandNode only the
// asserted fields are present (pointers), omitted fields are not checked.
type expectFile struct {
	Comment string       `json:"_comment"`
	Cases   []expectCase `json:"cases"`
}

type expectCase struct {
	Src     string            `json:"src"`
	Nodes   *[]expectNode     `json:"nodes"`
	Pipes   *[]model.PipeEdge `json:"pipes"`
	Unknown *bool             `json:"unknown"`
	Error   bool              `json:"error"`
}

type expectNode struct {
	Name     *string         `json:"name"`
	Wrappers *[]string       `json:"wrappers"`
	Sudo     *bool           `json:"sudo"`
	Async    *bool           `json:"async"`
	InSubst  *bool           `json:"in_subst"`
	Depth    *int            `json:"depth"`
	Sleep    *expectSleep    `json:"sleep"`
	Loop     *expectLoop     `json:"loop"`
	Git      *expectGit      `json:"git"`
	Writes   *[]expectTarget `json:"writes"`
	Reads    *[]expectTarget `json:"reads"`
}

type expectSleep struct {
	Seconds *float64 `json:"seconds"`
	Known   *bool    `json:"known"`
}

type expectLoop struct {
	Kind    *string `json:"kind"`
	N       *int    `json:"n"`
	Bounded *string `json:"bounded"`
	Depth   *int    `json:"depth"`
}

type expectGit struct {
	Sub     *string `json:"sub"`
	Cwd     *string `json:"cwd"`
	NoPager *bool   `json:"no_pager"`
}

type expectTarget struct {
	Path    string `json:"path"`
	Dynamic bool   `json:"dynamic"`
	Via     string `json:"via"`
}

func loadExpect(t *testing.T, path string) expectFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ef expectFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ef); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return ef
}

func eqTargets(got []model.WriteTarget, want []expectTarget) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Path != want[i].Path || got[i].Dynamic != want[i].Dynamic || got[i].Via != want[i].Via {
			return false
		}
	}
	return true
}

func checkNode(t *testing.T, src string, i int, got model.CommandNode, want expectNode) {
	t.Helper()
	fail := func(f string, a ...any) {
		t.Helper()
		t.Errorf("%q node[%d] %s: "+f, append([]any{src, i, got.Name}, a...)...)
	}
	if want.Name != nil && got.Name != *want.Name {
		fail("name = %q, want %q", got.Name, *want.Name)
	}
	if want.Wrappers != nil && !reflect.DeepEqual(append([]string{}, got.Wrappers...), *want.Wrappers) {
		fail("wrappers = %v, want %v", got.Wrappers, *want.Wrappers)
	}
	if want.Sudo != nil && got.Sudo != *want.Sudo {
		fail("sudo = %v", got.Sudo)
	}
	if want.Async != nil && got.Async != *want.Async {
		fail("async = %v", got.Async)
	}
	if want.InSubst != nil && got.InSubst != *want.InSubst {
		fail("in_subst = %v", got.InSubst)
	}
	if want.Depth != nil && got.Depth != *want.Depth {
		fail("depth = %d, want %d", got.Depth, *want.Depth)
	}
	if want.Sleep != nil {
		if got.Sleep == nil {
			fail("sleep missing")
		} else {
			if want.Sleep.Seconds != nil && got.Sleep.Seconds != *want.Sleep.Seconds {
				fail("sleep.seconds = %v, want %v", got.Sleep.Seconds, *want.Sleep.Seconds)
			}
			if want.Sleep.Known != nil && got.Sleep.Known != *want.Sleep.Known {
				fail("sleep.known = %v", got.Sleep.Known)
			}
		}
	}
	if want.Loop != nil {
		if got.Loop == nil {
			fail("loop missing")
		} else {
			if want.Loop.Kind != nil && string(got.Loop.Kind) != *want.Loop.Kind {
				fail("loop.kind = %q", got.Loop.Kind)
			}
			if want.Loop.N != nil && got.Loop.N != *want.Loop.N {
				fail("loop.n = %d", got.Loop.N)
			}
			if want.Loop.Bounded != nil && got.Loop.Bounded.String() != *want.Loop.Bounded {
				fail("loop.bounded = %v", got.Loop.Bounded)
			}
			if want.Loop.Depth != nil && got.Loop.Depth != *want.Loop.Depth {
				fail("loop.depth = %d", got.Loop.Depth)
			}
		}
	}
	if want.Git != nil {
		if got.Git == nil {
			fail("git missing")
		} else {
			if want.Git.Sub != nil && got.Git.Sub != *want.Git.Sub {
				fail("git.sub = %q", got.Git.Sub)
			}
			if want.Git.Cwd != nil && got.Git.Cwd != *want.Git.Cwd {
				fail("git.cwd = %q", got.Git.Cwd)
			}
			if want.Git.NoPager != nil && got.Git.NoPager != *want.Git.NoPager {
				fail("git.no_pager = %v", got.Git.NoPager)
			}
		}
	}
	if want.Writes != nil && !eqTargets(got.Writes, *want.Writes) {
		fail("writes = %+v, want %+v", got.Writes, *want.Writes)
	}
	if want.Reads != nil && !eqTargets(got.Reads, *want.Reads) {
		fail("reads = %+v, want %+v", got.Reads, *want.Reads)
	}
}

func TestExpectJSON(t *testing.T) {
	ef := loadExpect(t, filepath.Join("testdata", "expect.json"))
	if len(ef.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range ef.Cases {
		cl, err := Parse(c.Src, Options{})
		if c.Error {
			if err == nil {
				t.Errorf("%q: want parse error, got %+v", c.Src, cl)
			}
			if errors.Is(err, errInternal) {
				t.Errorf("%q: %v", c.Src, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.Src, err)
			continue
		}
		if cl.Shell != model.ShellBash {
			t.Errorf("%q: shell = %q", c.Src, cl.Shell)
		}
		if c.Nodes != nil {
			if len(cl.Nodes) != len(*c.Nodes) {
				t.Errorf("%q: %d nodes, want %d: %+v", c.Src, len(cl.Nodes), len(*c.Nodes), cl.Nodes)
				continue
			}
			for i, wn := range *c.Nodes {
				checkNode(t, c.Src, i, cl.Nodes[i], wn)
			}
		}
		if c.Pipes != nil && !reflect.DeepEqual(append([]model.PipeEdge{}, cl.Pipes...), *c.Pipes) {
			t.Errorf("%q: pipes = %v, want %v", c.Src, cl.Pipes, *c.Pipes)
		}
		if c.Unknown != nil && cl.Unknown != *c.Unknown {
			t.Errorf("%q: unknown = %v", c.Src, cl.Unknown)
		}
	}
}
