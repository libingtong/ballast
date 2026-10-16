package tier

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

// memFS is a small in-memory sys.FS with ownership, modes and symlinks, so
// tests can fake "root-owned" files without root. Paths are slash-separated;
// filepath separators are normalized so the tests run on every platform.
type memFS struct {
	nodes map[string]*memNode
	calls atomic.Int64
	// hook, when set, runs at the start of every operation (counting FS,
	// blocking FS).
	hook func(op, name string)
}

type memNode struct {
	mode   fs.FileMode // permission bits plus fs.ModeDir / fs.ModeSymlink
	uid    uint32
	data   []byte
	target string
}

func newMemFS() *memFS {
	m := &memFS{nodes: map[string]*memNode{}}
	m.nodes["/"] = &memNode{mode: fs.ModeDir | 0o755}
	return m
}

func clean(p string) string { return path.Clean(filepath.ToSlash(p)) }

func (m *memFS) mkdirAll(p string, uid uint32, perm fs.FileMode) {
	p = clean(p)
	var parts []string
	for q := p; q != "/" && q != "."; q = path.Dir(q) {
		parts = append(parts, q)
	}
	for i := len(parts) - 1; i >= 0; i-- {
		if _, ok := m.nodes[parts[i]]; !ok {
			m.nodes[parts[i]] = &memNode{mode: fs.ModeDir | perm, uid: uid}
		}
	}
}

func (m *memFS) writeFile(p string, data string, uid uint32, perm fs.FileMode) {
	p = clean(p)
	m.mkdirAll(path.Dir(p), 0, 0o755)
	m.nodes[p] = &memNode{mode: perm, uid: uid, data: []byte(data)}
}

func (m *memFS) symlink(p, target string, uid uint32) {
	p = clean(p)
	m.mkdirAll(path.Dir(p), 0, 0o755)
	m.nodes[p] = &memNode{mode: fs.ModeSymlink | 0o777, uid: uid, target: target}
}

func (m *memFS) remove(p string) {
	p = clean(p)
	for k := range m.nodes {
		if k == p || strings.HasPrefix(k, p+"/") {
			delete(m.nodes, k)
		}
	}
}

func (m *memFS) chmod(p string, perm fs.FileMode) {
	n := m.nodes[clean(p)]
	n.mode = n.mode&^fs.ModePerm | perm
}

func (m *memFS) chown(p string, uid uint32) { m.nodes[clean(p)].uid = uid }

// resolve walks p following symlinks in every component; the last one only
// when followLast.
func (m *memFS) resolve(p string, followLast bool) (string, error) {
	p = clean(p)
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

type memInfo struct {
	name string
	n    *memNode
}

func (i memInfo) Name() string       { return i.name }
func (i memInfo) Size() int64        { return int64(len(i.n.data)) }
func (i memInfo) Mode() fs.FileMode  { return i.n.mode }
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return i.n.mode&fs.ModeDir != 0 }
func (i memInfo) Sys() any           { return i.n }

// memOwner is the Owner func for memFS.
func memOwner(fi fs.FileInfo) (uint32, bool) {
	mi, ok := fi.(memInfo)
	if !ok {
		return 0, false
	}
	return mi.n.uid, true
}

func (m *memFS) tick(op, name string) {
	m.calls.Add(1)
	if m.hook != nil {
		m.hook(op, name)
	}
}

func (m *memFS) ReadFile(name string) ([]byte, error) {
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

func (m *memFS) Stat(name string) (fs.FileInfo, error) {
	m.tick("Stat", name)
	r, err := m.resolve(name, true)
	if err != nil {
		return nil, err
	}
	return memInfo{path.Base(r), m.nodes[r]}, nil
}

func (m *memFS) Lstat(name string) (fs.FileInfo, error) {
	m.tick("Lstat", name)
	r, err := m.resolve(name, false)
	if err != nil {
		return nil, err
	}
	return memInfo{path.Base(r), m.nodes[r]}, nil
}

type memEntry struct{ memInfo }

func (e memEntry) Type() fs.FileMode          { return e.n.mode.Type() }
func (e memEntry) Info() (fs.FileInfo, error) { return e.memInfo, nil }

func (m *memFS) ReadDir(name string) ([]fs.DirEntry, error) {
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
		out = append(out, memEntry{memInfo{path.Base(k), m.nodes[k]}})
	}
	return out, nil
}

func (m *memFS) Readlink(name string) (string, error) {
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

func (m *memFS) EvalSymlinks(name string) (string, error) {
	m.tick("EvalSymlinks", name)
	return m.resolve(name, true)
}

var errMemRO = errors.New("memFS: read-only")

func (m *memFS) OpenFile(string, int, fs.FileMode) (sys.File, error) { return nil, errMemRO }
func (m *memFS) MkdirAll(string, fs.FileMode) error                  { return errMemRO }
func (m *memFS) Rename(string, string) error                         { return errMemRO }
func (m *memFS) Remove(string) error                                 { return errMemRO }
func (m *memFS) RemoveAll(string) error                              { return errMemRO }

var _ sys.FS = (*memFS)(nil)
