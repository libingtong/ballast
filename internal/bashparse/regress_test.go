package bashparse

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/libingtong/ballast/internal/model"
)

type regressRow struct {
	Cmd             string `json:"cmd"`
	RunInBackground bool   `json:"run_in_background"`
	Want            string `json:"want"`
	GitSub          string `json:"git_sub"`
}

// noLongSleep is the explicit-deny regression rule of plan B1: a node sleeping
// at least 60s that is neither shell-async nor run in the background.
func noLongSleep(cl *model.CommandList, runInBackground bool) bool {
	if runInBackground {
		return false
	}
	for _, n := range cl.Nodes {
		if n.Sleep != nil && n.Sleep.Known && n.Sleep.Seconds >= 60 && !n.Async {
			return true
		}
	}
	return false
}

func TestRegressSet(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "regress.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Comment string       `json:"_comment"`
		Rows    []regressRow `json:"rows"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		t.Fatal(err)
	}
	if len(f.Rows) < 14 {
		t.Fatalf("only %d rows; the plan B1 set has 13 bash rows + 1 PowerShell row", len(f.Rows))
	}
	for _, r := range f.Rows {
		cl, err := Parse(r.Cmd, Options{})
		if err != nil {
			t.Errorf("%q: %v", r.Cmd, err)
			continue
		}
		switch {
		case r.GitSub != "":
			found := false
			for _, n := range cl.Nodes {
				if n.Git != nil && n.Git.Sub == r.GitSub {
					found = true
				}
			}
			if !found {
				t.Errorf("%q: no git node with sub %q: %+v", r.Cmd, r.GitSub, cl.Nodes)
			}
		default:
			if got := noLongSleep(cl, r.RunInBackground); got != (r.Want == "hit") {
				t.Errorf("%q (bg=%v): hit=%v, want %s", r.Cmd, r.RunInBackground, got, r.Want)
			}
		}
	}
}
