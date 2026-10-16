package runner

import (
	"io/fs"
	"strings"

	"github.com/libingtong/ballast/internal/sys"
)

// hookFS wraps sys.OSFS: onOpen runs before every OpenFile (to play the
// agent between the walk's Lstat and the open), wrapFile may replace the
// returned file.
type hookFS struct {
	sys.OSFS
	onOpen   func(name string, flag int)
	wrapFile func(name string, f sys.File) sys.File
}

func (h hookFS) OpenFile(name string, flag int, perm fs.FileMode) (sys.File, error) {
	if h.onOpen != nil {
		h.onOpen(name, flag)
	}
	f, err := h.OSFS.OpenFile(name, flag, perm)
	if err == nil && h.wrapFile != nil {
		f = h.wrapFile(name, f)
	}
	return f, err
}

// growingFile reports the real size but never reaches EOF: it keeps
// producing bytes after the real content, like a file that grows while it is
// copied.
type growingFile struct {
	sys.File
	done bool
}

func (g *growingFile) Read(p []byte) (int, error) {
	if !g.done {
		n, err := g.File.Read(p)
		if n > 0 {
			return n, nil
		}
		if err != nil {
			g.done = true
		}
	}
	for i := range p {
		p[i] = 'g'
	}
	return len(p), nil
}

func hasSuffix(name, suffix string) bool { return strings.HasSuffix(name, suffix) }
