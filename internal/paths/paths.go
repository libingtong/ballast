package paths

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"syscall"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// maxSymlinkHops bounds the dangling-symlink chase in RealPath.
const maxSymlinkHops = 40

// IsCaseInsensitive reports whether path comparison folds case on goos
// (windows and darwin: yes; others: no).
func IsCaseInsensitive(goos string) bool {
	return goos == "windows" || goos == "darwin"
}

// Normalize converts p to the internal form (see package doc). Relative p is
// joined to base (itself normalized). Empty p returns "". An empty base
// leaves a relative p relative (cleaned).
//
// On windows a rooted path without a drive ("\\foo") takes the drive of base
// when base has one, so it compares equal to its drive-qualified spelling.
func Normalize(p, base string, ctx model.PathContext) string {
	return normalize(p, base, ctx.Home, ctx.GOOS)
}

func normalize(p, base, home, goos string) string {
	if p == "" {
		return ""
	}
	win := goos == "windows"
	s := ToSlashForm(p, goos)
	if s == "~" || strings.HasPrefix(s, "~/") {
		if home != "" {
			if h := normalize(home, "", "", goos); isAbs(h, win) {
				s = ExpandHome(s, h)
			}
		}
	}
	if s == "" {
		return ""
	}

	if win && isDriveRel(s) {
		// "c:foo" / "c:": relative to base when base is on the same drive,
		// else to the drive root.
		drive := s[:2]
		rest := s[2:]
		if base != "" {
			if b := normalize(base, "", home, goos); len(b) >= 2 && b[:2] == drive {
				return cleanDrive(joinSlash(b, rest))
			}
		}
		return cleanDrive(drive + "/" + rest)
	}

	switch {
	case isDriveAbs(s, win):
		return cleanDrive(s)
	case win && isUNC(s):
		return cleanUNC(s)
	case s[0] == '/':
		return cleanRooted(s, base, home, win)
	}

	if base != "" {
		if b := normalize(base, "", home, goos); b != "" {
			s = joinSlash(b, s)
			if !win && isDriveAbs(s, false) {
				// A Windows-client cwd on a POSIX host: backslashes in a
				// relative path are separators too.
				s = strings.ReplaceAll(s, `\`, "/")
			}
			switch {
			case isDriveAbs(s, win):
				return cleanDrive(s)
			case win && isUNC(s):
				return cleanUNC(s)
			case s[0] == '/':
				return cleanRooted(s, "", home, win)
			}
		}
	}
	c := path.Clean(s)
	if len(c) < len(s) {
		// Cleaning may expose a "~" or drive prefix ("a/../~/x"): classify again.
		return normalize(c, "", home, goos)
	}
	return c
}

// joinSlash joins an internal-form absolute dir and a relative remainder.
func joinSlash(dir, rest string) string {
	dir = strings.TrimSuffix(dir, "/")
	if rest == "" {
		if dir == "" {
			return "/"
		}
		return dir + "/"
	}
	return dir + "/" + rest
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// isAbs reports whether a normalized path is absolute.
func isAbs(s string, win bool) bool {
	return s != "" && (s[0] == '/' || isDriveAbs(s, win))
}

// isDriveAbs reports whether s starts with "X:/" (any GOOS) or is exactly "X:" (windows).
func isDriveAbs(s string, win bool) bool {
	if len(s) < 2 || !isLetter(s[0]) || s[1] != ':' {
		return false
	}
	if len(s) == 2 {
		return win
	}
	return s[2] == '/'
}

// isDriveRel reports whether s is a drive-relative path "X:rest" (rest not
// starting with '/'; windows only).
func isDriveRel(s string) bool {
	return len(s) >= 2 && isLetter(s[0]) && s[1] == ':' && (len(s) == 2 || s[2] != '/')
}

// isUNC reports whether s is "//server...". The device prefixes "//./" and
// "//?/" are not servers (ToSlashForm strips them).
func isUNC(s string) bool {
	if len(s) <= 2 || s[0] != '/' || s[1] != '/' || s[2] == '/' {
		return false
	}
	server, _, _ := strings.Cut(s[2:], "/")
	return server != "." && server != "?"
}

// cleanDrive cleans "x:/..." and lower-cases the drive letter.
func cleanDrive(s string) string {
	rest := s[2:]
	if rest == "" {
		rest = "/"
	}
	return string(lowerASCII(s[0])) + ":" + path.Clean(rest)
}

// cleanUNC cleans "//server/share/..."; ".." never climbs above the share.
func cleanUNC(s string) string {
	server, rest, _ := strings.Cut(s[2:], "/")
	rest = strings.TrimLeft(rest, "/")
	share, rest, _ := strings.Cut(rest, "/")
	var b strings.Builder
	b.Grow(len(s))
	b.WriteString("//")
	b.WriteString(server)
	if share != "" {
		b.WriteByte('/')
		b.WriteString(share)
		if rest != "" {
			if c := path.Clean("/" + rest); c != "/" {
				b.WriteString(c)
			}
		}
	}
	return b.String()
}

// cleanRooted cleans a path starting with a single '/'. On windows it maps
// Git-Bash/Cygwin drive spellings, strips the NT "/??/" prefix and
// attaches the drive of base to a drive-less rooted path.
func cleanRooted(s, base, home string, win bool) string {
	c := path.Clean(s)
	if !win {
		return c
	}
	if strings.HasPrefix(c, "/??/") {
		return normalize(c[4:], base, home, "windows")
	}
	if d, ok := gitBashDrive(c); ok {
		return cleanDrive(d)
	}
	if base != "" {
		if b := normalize(base, "", home, "windows"); isDriveAbs(b, true) {
			return b[:2] + c
		}
	}
	return c
}

// gitBashDrive maps "/c/x", "/cygdrive/c/x" and "/c:/x" to "c:/x".
func gitBashDrive(s string) (string, bool) {
	if len(s) >= 2 && s[0] == '/' && isLetter(s[1]) && (len(s) == 2 || s[2] == '/') {
		return string(lowerASCII(s[1])) + ":" + orRoot(s[2:]), true
	}
	if len(s) >= 11 && strings.HasPrefix(s, "/cygdrive/") && isLetter(s[10]) && (len(s) == 11 || s[11] == '/') {
		return string(lowerASCII(s[10])) + ":" + orRoot(s[11:]), true
	}
	if len(s) >= 3 && s[0] == '/' && isLetter(s[1]) && s[2] == ':' && (len(s) == 3 || s[3] == '/') {
		return s[1:], true
	}
	return "", false
}

func orRoot(s string) string {
	if s == "" {
		return "/"
	}
	return s
}

// ToSlashForm converts separators and Windows/UNC/Git-Bash prefixes without
// making the path absolute ("C:\\a\\b" → "c:/a/b", "\\\\srv\\sh\\x" → "//srv/sh/x",
// "/c/Users" → "c:/Users" when goos == "windows").
//
// On windows every backslash is a separator and the "\\?\" / "\\.\" / "\??\"
// prefixes are dropped ("\\?\UNC\srv\sh" → "//srv/sh"). On other systems a
// backslash is an ordinary file-name character, except in a Windows
// drive-absolute path ("C:\x"), which is converted so that paths produced by
// a Windows client still compare correctly.
func ToSlashForm(p, goos string) string {
	if goos != "windows" {
		if len(p) >= 3 && isLetter(p[0]) && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
			p = strings.ReplaceAll(p, `\`, "/")
			return string(lowerASCII(p[0])) + p[1:]
		}
		return p
	}
	p = strings.ReplaceAll(p, `\`, "/")
	for {
		n := stripVerbatim(p)
		if n == p {
			break
		}
		p = n
	}
	if d, ok := gitBashDrive(p); ok {
		p = d
	}
	if len(p) >= 2 && isLetter(p[0]) && p[1] == ':' {
		p = string(lowerASCII(p[0])) + p[1:]
	}
	return p
}

func stripVerbatim(p string) string {
	switch {
	case strings.HasPrefix(p, "//?/") || strings.HasPrefix(p, "//./"):
		rest := p[4:]
		if len(rest) >= 4 && strings.EqualFold(rest[:4], "unc/") {
			return "//" + rest[4:]
		}
		return rest
	case strings.HasPrefix(p, "/??/"):
		return p[4:]
	}
	return p
}

// ToOS converts an internal-form path back to an OS path for goos.
func ToOS(norm, goos string) string {
	if goos == "windows" {
		return strings.ReplaceAll(norm, "/", `\`)
	}
	return norm
}

// ExpandHome expands a leading "~" or "~/" using home (internal form).
// Anything else (including "~user" and an empty home) is returned unchanged.
func ExpandHome(p, home string) string {
	if home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return strings.TrimSuffix(home, "/") + p[1:]
	}
	return p
}

func fold(s string, ci bool) string {
	if ci {
		return strings.ToLower(s)
	}
	return s
}

// Equal compares two internal-form paths, folding case when goos is case-insensitive.
func Equal(a, b, goos string) bool {
	if a == b {
		return true
	}
	if !IsCaseInsensitive(goos) {
		return false
	}
	return strings.ToLower(a) == strings.ToLower(b)
}

// Within reports whether p equals dir or is inside it (component-wise, case
// folding per goos). Both arguments are internal form.
func Within(p, dir, goos string) bool {
	_, ok := Rel(dir, p, goos)
	return ok
}

// Rel returns p relative to root ("a/b.go") when p is within root. When p
// equals root the result is ".". The result keeps the case of p.
func Rel(root, p, goos string) (string, bool) {
	if root == "" || p == "" {
		return "", false
	}
	ci := IsCaseInsensitive(goos)
	trimmed := strings.TrimRight(root, "/") // "" for "/", "c:" for "c:/"
	lp, lr := fold(p, ci), fold(trimmed, ci)
	if lp == lr || lp == fold(root, ci) {
		return ".", true
	}
	if !strings.HasPrefix(lp, lr+"/") {
		return "", false
	}
	if len(lp) == len(p) {
		return p[len(trimmed)+1:], true
	}
	// Case folding changed the byte length: skip the same number of
	// components in the original string.
	skip := strings.Count(lr, "/") + 1
	i := 0
	for ; skip > 0; skip-- {
		j := strings.IndexByte(p[i:], '/')
		if j < 0 {
			return "", false
		}
		i += j + 1
	}
	return p[i:], true
}

func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// RealPath resolves symlinks/junctions of an internal-form path. For a
// non-existent path it resolves the nearest existing ancestor and re-appends
// the remaining components; a dangling symlink among those components is
// followed to its (non-existent) target. Errors other than not-exist
// (permission, symlink loops) are returned. A nil fsys returns norm.
func RealPath(fsys sys.FS, norm, goos string) (string, error) {
	if fsys == nil || norm == "" {
		return norm, nil
	}
	r := resolver{fsys: fsys, goos: goos}
	return r.resolve(norm)
}

type resolver struct {
	fsys sys.FS
	goos string
	hops int
}

func (r *resolver) fromOS(p string) string { return normalize(p, "", "", r.goos) }

func (r *resolver) resolve(p string) (string, error) {
	out, err := r.fsys.EvalSymlinks(ToOS(p, r.goos))
	if err == nil {
		if n := r.fromOS(out); n != "" {
			return n, nil
		}
		return p, nil
	}
	if !isNotExist(err) {
		return "", err
	}
	parent, name, ok := splitParent(p)
	if !ok {
		return p, nil
	}
	parent, err = r.resolve(parent)
	if err != nil {
		return "", err
	}
	cand := joinSlash(parent, name)
	if fi, lerr := r.fsys.Lstat(ToOS(cand, r.goos)); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
		target, rerr := r.fsys.Readlink(ToOS(cand, r.goos))
		if rerr == nil {
			r.hops++
			if r.hops > maxSymlinkHops {
				return "", fmt.Errorf("paths: too many symbolic links resolving %q", p)
			}
			return r.resolve(normalize(target, parent, "", r.goos))
		}
		if !isNotExist(rerr) {
			return "", rerr
		}
	}
	return cand, nil
}

// splitParent splits an internal-form path into its parent and last
// component; ok is false for roots ("/", "c:/", "//srv/share") and relative
// single-component paths.
func splitParent(p string) (parent, name string, ok bool) {
	i := strings.LastIndexByte(p, '/')
	if i < 0 || i == len(p)-1 {
		return "", "", false
	}
	if isUNC(p) && strings.Count(p[2:], "/") < 2 {
		return "", "", false
	}
	parent, name = p[:i], p[i+1:]
	if parent == "" || len(parent) == 2 && parent[1] == ':' {
		parent += "/"
	}
	return parent, name, true
}

// Candidates implements ports.PathService.Candidates using fsys for EvalSymlinks.
// The order is: normalized path, its ProjectRoot-relative form, the resolved
// path and its relative form(s). A nil fsys (or a resolution error other than
// not-exist) yields only the unresolved forms.
func Candidates(fsys sys.FS, raw string, ctx model.PathContext) []string {
	norm := normalize(raw, ctx.Cwd, ctx.Home, ctx.GOOS)
	if norm == "" {
		return nil
	}
	goos := ctx.GOOS
	root := ctx.ProjectRoot
	if root == "" {
		root = ctx.Cwd
	}
	root = normalize(root, "", ctx.Home, goos)

	forms := []string{norm}
	realRoot := ""
	if fsys != nil {
		if real, err := RealPath(fsys, norm, goos); err == nil && real != norm {
			forms = append(forms, real)
		}
		if root != "" {
			if rr, err := RealPath(fsys, root, goos); err == nil && rr != root {
				realRoot = rr
			}
		}
	}

	out := make([]string, 0, 6)
	add := func(s string) {
		if s == "" {
			return
		}
		for _, o := range out {
			if o == s {
				return
			}
		}
		out = append(out, s)
	}
	for _, f := range forms {
		add(f)
		if rel, ok := Rel(root, f, goos); ok && rel != "." {
			add(rel)
		}
	}
	if realRoot != "" {
		for _, f := range forms {
			if rel, ok := Rel(realRoot, f, goos); ok && rel != "." {
				add(rel)
			}
		}
	}
	return out
}

// Service is the ports.PathService implementation.
type Service struct {
	FS sys.FS
}

var _ ports.PathService = (*Service)(nil)

// NewService returns a Service over fsys.
func NewService(fsys sys.FS) *Service { return &Service{FS: fsys} }

// Normalize implements ports.PathService.
func (s *Service) Normalize(p, base string, ctx model.PathContext) string {
	return Normalize(p, base, ctx)
}

// Candidates implements ports.PathService.
func (s *Service) Candidates(raw string, ctx model.PathContext) []string {
	return Candidates(s.FS, raw, ctx)
}

// CompileGlobs implements ports.PathService.
func (s *Service) CompileGlobs(patterns []string, ctx model.PathContext) (ports.GlobMatcher, error) {
	home := ctx.Home
	if home != "" {
		home = normalize(home, "", "", ctx.GOOS)
	}
	g, err := CompileGlobs(patterns, GlobOptions{Home: home, CaseInsensitive: IsCaseInsensitive(ctx.GOOS)})
	if err != nil {
		return nil, err // never return a typed-nil *GlobSet inside the interface
	}
	return g, nil
}
