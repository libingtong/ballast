package hook

import (
	"errors"
	"io/fs"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// world implements model.WorldView over the injected Env, FS and RepoLocator.
type world struct {
	env  sys.Env
	fsys sys.FS
	loc  ports.RepoLocator
	repo *model.RepoInfo

	once   sync.Once
	branch string
	tri    model.Tri
}

func newWorld(env sys.Env, fsys sys.FS, loc ports.RepoLocator, repo *model.RepoInfo) *world {
	return &world{env: env, fsys: fsys, loc: loc, repo: repo}
}

// PathExists reports existence via Lstat: found → true, ENOENT/ENOTDIR →
// false, anything else → unknown.
func (w *world) PathExists(norm string) model.Tri {
	if norm == "" {
		return model.TriUnknown
	}
	_, err := w.fsys.Lstat(filepath.FromSlash(norm))
	switch {
	case err == nil:
		return model.TriTrue
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return model.TriFalse
	}
	return model.TriUnknown
}

func (w *world) Getenv(name string) (string, bool) { return w.env.Getenv(name) }

// Branch reads HEAD once per invocation; outside a repo it is unknown.
func (w *world) Branch() (string, model.Tri) {
	w.once.Do(func() {
		if w.repo == nil {
			w.tri = model.TriUnknown
			return
		}
		w.branch, w.tri = w.loc.Branch(w.repo)
	})
	return w.branch, w.tri
}
