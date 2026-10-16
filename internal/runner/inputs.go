package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/libingtong/ballast/internal/sys"
)

// This file is step 3 of Materialize (inputs_untracked). It runs OUTSIDE the
// sandbox with the right to write ~/.ballast, so every write goes through one
// os.Root opened on the private tree: no path can resolve outside it, even if
// a rule below were implemented wrongly (docs/p2-design.md §3.4, v1.5.1).
// There is deliberately no path-based os.Create / os.OpenFile / os.MkdirAll
// / os.WriteFile in this file or in tree.go (TestTreeSourcesHaveNoAbsoluteWrites).

type inputLine struct {
	path string
	mode string
	sum  string
}

func sortInputLines(l []inputLine) {
	sort.Slice(l, func(i, j int) bool { return l[i].path < l[j].path })
}

type inputCandidate struct {
	rel  string
	abs  string
	info fs.FileInfo // from the walk's Lstat
}

// copyInputs copies the untracked / ignored files of the real work tree that
// match globs (doublestar) and are not entries of T_full into dir, and
// returns the InputsHash and the number of files.
func copyInputs(opt Options, toplevel, dir string, tracked map[string]string, globs []string) (string, int, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", 0, fmt.Errorf("%w: open private tree: %v", ErrMaterialize, err)
	}
	defer root.Close()

	bases := make([]string, 0, len(globs))
	for _, g := range globs {
		b, _ := doublestar.SplitPattern(g)
		bases = append(bases, b)
	}
	mayContain := func(rel string) bool {
		for _, b := range bases {
			if b == "." || rel == b || strings.HasPrefix(rel, b+"/") || strings.HasPrefix(b, rel+"/") {
				return true
			}
		}
		return false
	}
	matches := func(rel string) bool {
		for _, g := range globs {
			if ok, _ := doublestar.Match(g, rel); ok {
				return true
			}
		}
		return false
	}

	c := &inputCopier{opt: opt, root: root, tracked: tracked, dirs: map[string]bool{}}
	var walk func(abs, rel string, want fs.FileInfo) error
	walk = func(abs, rel string, want fs.FileInfo) error {
		ents, err := opt.FS.ReadDir(abs)
		if err != nil {
			return fmt.Errorf("%w: read %s: %v", ErrMaterialize, abs, err)
		}
		// The directory was a real directory when it was Lstat'ed; if the
		// path now names something else (swapped for a symlink between the
		// Lstat and the ReadDir) the listing may come from outside the tree.
		recheck := opt.FS.Lstat
		if rel == "" {
			recheck = opt.FS.Stat // the top level itself may legitimately be reached through a link
		}
		if now, lerr := recheck(abs); lerr != nil || !now.IsDir() || (want != nil && !os.SameFile(want, now)) {
			return fmt.Errorf("%w: %s changed while it was being read", ErrMaterialize, abs)
		}
		for _, de := range ents {
			name := de.Name()
			if rel == "" && name == ".git" {
				continue // the real git dir is never entered
			}
			crel := name
			if rel != "" {
				crel = rel + "/" + name
			}
			cabs := filepath.Join(abs, name)
			fi, err := opt.FS.Lstat(cabs)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue // vanished since ReadDir
				}
				return fmt.Errorf("%w: %s: %v", ErrMaterialize, cabs, err)
			}
			switch {
			case fi.IsDir():
				if mayContain(crel) {
					if err := walk(cabs, crel, fi); err != nil {
						return err
					}
				}
			case !matches(crel):
			case func() bool { _, ok := tracked[crel]; return ok }():
			case fi.Mode()&fs.ModeSymlink != 0 || fi.Mode().IsRegular():
				if err := c.copy(inputCandidate{rel: crel, abs: cabs, info: fi}); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%w: %s is neither a regular file nor a symlink", ErrInputsConflict, crel)
			}
		}
		return nil
	}
	top, terr := opt.FS.Stat(toplevel)
	if terr != nil || !top.IsDir() {
		return "", 0, fmt.Errorf("%w: %s is not a directory", ErrMaterialize, toplevel)
	}
	if err := walk(toplevel, "", top); err != nil {
		return "", 0, err
	}
	return hashLines(c.lines), len(c.lines), nil
}

type inputCopier struct {
	opt     Options
	root    *os.Root
	tracked map[string]string
	dirs    map[string]bool // directories already verified / created
	lines   []inputLine
	bytes   int64
}

func (c *inputCopier) conflict(rel, format string, a ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrInputsConflict, rel, fmt.Sprintf(format, a...))
}

// place makes sure every parent of rel is a real directory inside the root
// (rules (b) and (c)).
func (c *inputCopier) place(rel string) error {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		dir := strings.Join(parts[:i], "/")
		if c.dirs[dir] {
			continue
		}
		if _, ok := c.tracked[dir]; ok { // (b): a blob, symlink or gitlink of T_full
			return c.conflict(rel, "%s is a non-directory entry of the tracked tree", dir)
		}
		fi, err := c.root.Lstat(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := c.root.Mkdir(dir, 0o755); err != nil {
				return c.conflict(rel, "mkdir %s: %v", dir, err)
			}
		case err != nil:
			return c.conflict(rel, "lstat %s: %v", dir, err)
		case !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0: // (c)
			return c.conflict(rel, "%s is %s in the private tree", dir, fi.Mode().Type())
		}
		c.dirs[dir] = true
	}
	return nil
}

func (c *inputCopier) account(n int64) error {
	c.bytes += n
	if c.bytes > maxInputBytes {
		return fmt.Errorf("%w: more than %d bytes", ErrInputsTooLarge, maxInputBytes)
	}
	return nil
}

func (c *inputCopier) copy(cand inputCandidate) error {
	if len(c.lines) >= maxInputFiles {
		return fmt.Errorf("%w: more than %d files", ErrInputsTooLarge, maxInputFiles)
	}
	if err := c.place(cand.rel); err != nil {
		return err
	}
	if cand.info.Mode()&fs.ModeSymlink != 0 {
		target, err := c.opt.FS.Readlink(cand.abs)
		if err != nil {
			return c.conflict(cand.rel, "readlink: %v", err)
		}
		if err := c.account(int64(len(target))); err != nil {
			return err
		}
		if err := c.root.Symlink(target, cand.rel); err != nil { // (d)
			return c.conflict(cand.rel, "symlink: %v", err)
		}
		sum := sha256.Sum256([]byte(target))
		c.lines = append(c.lines, inputLine{path: cand.rel, mode: "120000", sum: hex.EncodeToString(sum[:])})
		return nil
	}

	src, err := c.opt.FS.OpenFile(cand.abs, os.O_RDONLY|openFlagsSource, 0)
	if err != nil {
		return c.conflict(cand.rel, "open source: %v", err)
	}
	defer src.Close()
	fi, err := src.Stat()
	if err != nil {
		return c.conflict(cand.rel, "stat source: %v", err)
	}
	if !fi.Mode().IsRegular() || !os.SameFile(cand.info, fi) {
		return c.conflict(cand.rel, "source changed between the walk and the open")
	}
	perm, mode := fs.FileMode(0o644), "100644"
	if fi.Mode()&0o100 != 0 {
		perm, mode = 0o755, "100755"
	}
	dst, err := c.root.OpenFile(cand.rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY|openFlagsLeaf, perm) // (d)
	if err != nil {
		return c.conflict(cand.rel, "create: %v", err)
	}
	h := sha256.New()
	werr := copyCounted(io.MultiWriter(dst, h), src, c.account)
	if cerr := dst.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		if errors.Is(werr, ErrInputsTooLarge) {
			return werr
		}
		return c.conflict(cand.rel, "copy: %v", werr)
	}
	c.lines = append(c.lines, inputLine{path: cand.rel, mode: mode, sum: hex.EncodeToString(h.Sum(nil))})
	return nil
}

// copyCounted copies src to dst, calling account with every chunk size
// before it is written (a file that grows while it is copied stops at the
// limit).
func copyCounted(dst io.Writer, src sys.File, account func(int64) error) error {
	buf := make([]byte, 64<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if aerr := account(int64(n)); aerr != nil {
				return aerr
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
