package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/libingtong/ballast/internal/config"
	"github.com/libingtong/ballast/internal/dsl"
	"github.com/libingtong/ballast/internal/install"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/snapshot"
)

// repo locates the git work tree of the working directory (nil, nil outside).
func (a *app) repo() (*model.RepoInfo, error) {
	repo, err := a.locator().Locate(a.cwd)
	if err != nil {
		if errors.Is(err, model.ErrNotRepo) {
			return nil, nil
		}
		return nil, err
	}
	return repo, nil
}

// projectRoot is the git toplevel, or the working directory outside git.
func (a *app) projectRoot() (string, error) {
	repo, err := a.repo()
	if err != nil {
		return "", err
	}
	if repo != nil {
		return repo.Toplevel, nil
	}
	return a.cwd, nil
}

// loadRequest builds the rule-load request for the working directory.
func (a *app) loadRequest(client model.ClientID) (model.LoadRequest, error) {
	repo, err := a.repo()
	if err != nil {
		return model.LoadRequest{}, err
	}
	return model.LoadRequest{Client: client, Cwd: a.cwd, Repo: repo, Now: a.clock.Now()}, nil
}

// manifestClients returns the clients recorded in the install manifest in
// support-tier order, plus claude-code (the reference client).
func (a *app) manifestClients() ([]model.ClientID, error) {
	m, err := install.LoadManifest(a.fs, a.lay.Manifest)
	if err != nil {
		return nil, err
	}
	set := map[model.ClientID]bool{model.ClientClaude: true}
	for _, e := range m.Entries {
		set[e.Client] = true
	}
	for _, f := range m.Files {
		set[f.Client] = true
	}
	for _, f := range m.Flags {
		set[f.Client] = true
	}
	var out []model.ClientID
	for _, c := range model.AllClients {
		if set[c] {
			out = append(out, c)
		}
	}
	return out, nil
}

// parseClient validates one client id.
func parseClient(s string) (model.ClientID, error) {
	c := model.ClientID(s)
	if !c.Valid() {
		return "", fmt.Errorf("unknown client %q (want one of %s)", s, clientList())
	}
	return c, nil
}

func clientList() string {
	names := make([]string, len(model.AllClients))
	for i, c := range model.AllClients {
		names[i] = string(c)
	}
	return strings.Join(names, ", ")
}

// readError marks a failure to read an input file (exit 5).
type readError struct{ err error }

func (e *readError) Error() string { return e.err.Error() }
func (e *readError) Unwrap() error { return e.err }

// ruleInputs expands files and directories (non-recursive *.json) into lint
// inputs, de-duplicated by cleaned path.
func (a *app) ruleInputs(args []string, layer model.Layer) ([]dsl.LintInput, error) {
	var files []string
	seen := map[string]bool{}
	add := func(p string) {
		p = filepath.Clean(p)
		if !seen[p] {
			seen[p] = true
			files = append(files, p)
		}
	}
	for _, arg := range args {
		fi, err := a.fs.Stat(arg)
		if err != nil {
			return nil, &readError{err}
		}
		if !fi.IsDir() {
			add(arg)
			continue
		}
		ents, err := a.fs.ReadDir(arg)
		if err != nil {
			return nil, &readError{err}
		}
		var names []string
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			add(filepath.Join(arg, n))
		}
	}
	out := make([]dsl.LintInput, 0, len(files))
	for _, p := range files {
		data, err := a.fs.ReadFile(p)
		if err != nil {
			return nil, &readError{err}
		}
		out = append(out, dsl.LintInput{Path: p, Layer: layer, Data: data})
	}
	return out, nil
}

// stackInputs returns the existing layer files of the rule stack in effect
// for the working directory (org → repo → user).
func (a *app) stackInputs() ([]dsl.LintInput, error) {
	req, err := a.loadRequest(model.ClientClaude)
	if err != nil {
		return nil, err
	}
	var out []dsl.LintInput
	for _, lp := range config.LayerPaths(a.lay, req) {
		data, err := a.fs.ReadFile(lp.Path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, &readError{err}
		}
		out = append(out, dsl.LintInput{Path: lp.Path, Layer: lp.Layer, Data: data})
	}
	return out, nil
}

// writeJSON prints v as indented JSON.
func (a *app) writeJSON(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// fileExists reports whether p exists.
func (a *app) fileExists(p string) bool {
	_, err := a.fs.Stat(p)
	return err == nil
}

// locator returns the repository locator.
func (a *app) locator() *snapshot.Locator { return &snapshot.Locator{FS: a.fs} }
