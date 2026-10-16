package cli

import (
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/libingtong/ballast/internal/sys"
)

// wireFS is a small in-memory sys.FS with ownership, modes and symlinks, so
// tests can fake "root-owned" files without root. Paths are slash-separated;
// filepath separators are normalized so the tests run on every platform.
type wireFS struct {
	nodes map[string]*wireNode
	calls atomic.Int64
	// hook, when set, runs at the start of every operation (counting FS,
	// blocking FS).
	hook func(op, name string)
}

type wireNode struct {
	mode   fs.FileMode // permission bits plus fs.ModeDir / fs.ModeSymlink
	uid    uint32
	data   []byte
	target string
}

func newWireFS() *wireFS {
	m := &wireFS{nodes: map[string]*wireNode{}}
	m.nodes["/"] = &wireNode{mode: fs.ModeDir | 0o755}
	return m
}

func mclean(p string) string { return path.Clean(filepath.ToSlash(p)) }

func (m *wireFS) mkdirAll(p string, uid uint32, perm fs.FileMode) {
	p = mclean(p)
	var parts []string
	for q := p; q != "/" && q != "."; q = path.Dir(q) {
		parts = append(parts, q)
	}
	for i := len(parts) - 1; i >= 0; i-- {
		if _, ok := m.nodes[parts[i]]; !ok {
			m.nodes[parts[i]] = &wireNode{mode: fs.ModeDir | perm, uid: uid}
		}
	}
}

func (m *wireFS) writeFile(p string, data string, uid uint32, perm fs.FileMode) {
	p = mclean(p)
	m.mkdirAll(path.Dir(p), 0, 0o755)
	m.nodes[p] = &wireNode{mode: perm, uid: uid, data: []byte(data)}
}

func (m *wireFS) symlink(p, target string, uid uint32) {
	p = mclean(p)
	m.mkdirAll(path.Dir(p), 0, 0o755)
	m.nodes[p] = &wireNode{mode: fs.ModeSymlink | 0o777, uid: uid, target: target}
}

func (m *wireFS) remove(p string) {
	p = mclean(p)
	for k := range m.nodes {
		if k == p || strings.HasPrefix(k, p+"/") {
			delete(m.nodes, k)
		}
	}
}

func (m *wireFS) chmod(p string, perm fs.FileMode) {
	n := m.nodes[mclean(p)]
	n.mode = n.mode&^fs.ModePerm | perm
}

func (m *wireFS) chown(p string, uid uint32) { m.nodes[mclean(p)].uid = uid }

// resolve walks p following symlinks in every component; the last one only
// when followLast.
func (m *wireFS) resolve(p string, followLast bool) (string, error) {
	p = mclean(p)
	hops := 0
	for {
		parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
		cur := "/"
		restart := false
		for i, part := range parts {
			if part == "" {
				continue
			}
			next := path.Join(cur, part)
			n, ok := m.nodes[next]
			if !ok {
				return "", &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrNotExist}
			}
			last := i == len(parts)-1
			if n.mode&fs.ModeSymlink != 0 && (!last || followLast) {
				hops++
				if hops > 40 {
					return "", &fs.PathError{Op: "lstat", Path: p, Err: errors.New("too many links")}
				}
				t := n.target
				if !path.IsAbs(t) {
					t = path.Join(cur, t)
				}
				p = path.Join(t, path.Join(parts[i+1:]...))
				restart = true
				break
			}
			if !last && n.mode&fs.ModeDir == 0 {
				return "", &fs.PathError{Op: "lstat", Path: p, Err: errors.New("not a directory")}
			}
			cur = next
		}
		if !restart {
			return cur, nil
		}
	}
}

type wireInfo struct {
	name string
	n    *wireNode
}

func (i wireInfo) Name() string       { return i.name }
func (i wireInfo) Size() int64        { return int64(len(i.n.data)) }
func (i wireInfo) Mode() fs.FileMode  { return i.n.mode }
func (i wireInfo) ModTime() time.Time { return time.Time{} }
func (i wireInfo) IsDir() bool        { return i.n.mode&fs.ModeDir != 0 }
func (i wireInfo) Sys() any           { return i.n }

// wireOwner is the Owner func for wireFS.
func wireOwner(fi fs.FileInfo) (uint32, bool) {
	mi, ok := fi.(wireInfo)
	if !ok {
		return 0, false
	}
	return mi.n.uid, true
}

func (m *wireFS) tick(op, name string) {
	m.calls.Add(1)
	if m.hook != nil {
		m.hook(op, name)
	}
}

func (m *wireFS) ReadFile(name string) ([]byte, error) {
	m.tick("ReadFile", name)
	r, err := m.resolve(name, true)
	if err != nil {
		return nil, err
	}
	n := m.nodes[r]
	if n.mode&fs.ModeDir != 0 {
		return nil, &fs.PathError{Op: "read", Path: name, Err: errors.New("is a directory")}
	}
	return append([]byte(nil), n.data...), nil
}

func (m *wireFS) Stat(name string) (fs.FileInfo, error) {
	m.tick("Stat", name)
	r, err := m.resolve(name, true)
	if err != nil {
		return nil, err
	}
	return wireInfo{path.Base(r), m.nodes[r]}, nil
}

func (m *wireFS) Lstat(name string) (fs.FileInfo, error) {
	m.tick("Lstat", name)
	r, err := m.resolve(name, false)
	if err != nil {
		return nil, err
	}
	return wireInfo{path.Base(r), m.nodes[r]}, nil
}

type wireEntry struct{ wireInfo }

func (e wireEntry) Type() fs.FileMode          { return e.n.mode.Type() }
func (e wireEntry) Info() (fs.FileInfo, error) { return e.wireInfo, nil }

func (m *wireFS) ReadDir(name string) ([]fs.DirEntry, error) {
	m.tick("ReadDir", name)
	r, err := m.resolve(name, true)
	if err != nil {
		return nil, err
	}
	if m.nodes[r].mode&fs.ModeDir == 0 {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: errors.New("not a directory")}
	}
	var names []string
	for k := range m.nodes {
		if k != r && path.Dir(k) == r {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	var out []fs.DirEntry
	for _, k := range names {
		out = append(out, wireEntry{wireInfo{path.Base(k), m.nodes[k]}})
	}
	return out, nil
}

func (m *wireFS) Readlink(name string) (string, error) {
	m.tick("Readlink", name)
	r, err := m.resolve(name, false)
	if err != nil {
		return "", err
	}
	n := m.nodes[r]
	if n.mode&fs.ModeSymlink == 0 {
		return "", errors.New("not a symlink")
	}
	return n.target, nil
}

func (m *wireFS) EvalSymlinks(name string) (string, error) {
	m.tick("EvalSymlinks", name)
	return m.resolve(name, true)
}

var errWireRO = errors.New("wireFS: read-only")

func (m *wireFS) OpenFile(string, int, fs.FileMode) (sys.File, error) { return nil, errWireRO }
func (m *wireFS) MkdirAll(string, fs.FileMode) error                  { return errWireRO }
func (m *wireFS) Rename(string, string) error                         { return errWireRO }
func (m *wireFS) Remove(string) error                                 { return errWireRO }
func (m *wireFS) RemoveAll(string) error                              { return errWireRO }

var _ sys.FS = (*wireFS)(nil)
