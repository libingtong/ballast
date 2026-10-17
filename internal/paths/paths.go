package paths

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"
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
	case win && isDevice(s):
		return cleanDevice(s)
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
			case win && isDevice(s):
				return cleanDevice(s)
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

// isDevice reports whether s is a Win32 device-namespace path that cannot be
// mapped to a drive letter or UNC share ("//?/Volume{GUID}/x",
// "//?/GLOBALROOT/Device/...", "//./pipe/x"). Such a path is already
// absolute and must never be joined to a base.
func isDevice(s string) bool {
	return len(s) >= 4 && s[0] == '/' && s[1] == '/' && (s[2] == '?' || s[2] == '.') && s[3] == '/'
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
// "//?/" are not servers (see isDevice).
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

// cleanDevice cleans "//?/dev/..." and "//./dev/...": the first component
// after the prefix is the device and ".." never climbs above it. It returns
// "" for a bare prefix.
func cleanDevice(s string) string {
	prefix, rest := s[:4], strings.TrimLeft(s[4:], "/")
	if rest == "" {
		return ""
	}
	dev, tail, _ := strings.Cut(rest, "/")
	if tail = path.Clean("/" + tail); tail == "/" {
		tail = ""
	}
	return prefix + dev + tail
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
		return normalize(c, base, home, "windows") // ToSlashForm rewrites the prefix
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

// stripVerbatim drops a "//?/", "//./" or NT "/??/" prefix when what follows
// is a drive or a UNC share ("//?/UNC/srv/sh" becomes "//srv/sh"). Any other
// device path (Volume{GUID}, GLOBALROOT, pipe, ...) has no drive-letter or
// UNC spelling and stays absolute in "//?/dev/..." form; stripping it would
// turn it into a relative path.
func stripVerbatim(p string) string {
	if !strings.HasPrefix(p, "//?/") && !strings.HasPrefix(p, "//./") && !strings.HasPrefix(p, "/??/") {
		return p
	}
	rest := strings.TrimLeft(p[4:], "/")
	switch {
	case len(rest) >= 4 && strings.EqualFold(rest[:4], "unc/"):
		return "//" + rest[4:]
	case isDriveAbs(rest, true):
		return rest
	case p[1] == '?':
		return "//?/" + rest // NT namespace spelling of the same device path
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

// Equal compares two paths, folding case when goos is case-insensitive. On
// windows both sides are first converted with ToSlashForm, so the native
// spelling ("C:\\Users\\X") equals its internal form ("c:/users/x").
func Equal(a, b, goos string) bool {
	if a == b {
		return true
	}
	if goos == "windows" {
		if a, b = ToSlashForm(a, goos), ToSlashForm(b, goos); a == b {
			return true
		}
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
//
// On windows (goos) every component is inspected with Lstat: Go's
// EvalSymlinks does not follow directory junctions, which only carry
// fs.ModeIrregular.
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
	if r.goos == "windows" {
		w, err := r.walk(p)
		if err != nil {
			return "", err
		}
		p = w
	}
	return r.eval(p)
}

// link reports where the symlink or junction at p (internal form) points.
// ok is false when p is not a link. On windows an fs.ModeIrregular entry is a
// candidate junction; a failing Readlink then just means "some other reparse
// point".
func (r *resolver) link(p string, fi fs.FileInfo) (target string, ok bool, err error) {
	m := fi.Mode()
	irregular := r.goos == "windows" && m&fs.ModeIrregular != 0 && m&fs.ModeSymlink == 0
	if m&fs.ModeSymlink == 0 && !irregular {
		return "", false, nil
	}
	t, err := r.fsys.Readlink(ToOS(p, r.goos))
	if err != nil {
		if irregular || isNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return t, true, nil
}

func (r *resolver) hop(p string) error {
	if r.hops++; r.hops > maxSymlinkHops {
		return fmt.Errorf("paths: too many symbolic links resolving %q", p)
	}
	return nil
}

// walk follows every symlink and junction component of p with Lstat and
// Readlink (windows only). Components that do not exist, and everything
// after them, are kept as is. A junction target that has no drive-letter
// spelling ("//?/Volume{GUID}/...") stays in its device form.
func (r *resolver) walk(p string) (string, error) {
	n := rootLen(p)
	if n == 0 || n >= len(p) {
		return p, nil
	}
	cur, rest := p[:n], strings.TrimLeft(p[n:], "/")
	for rest != "" {
		name, tail, _ := strings.Cut(rest, "/")
		next := joinSlash(cur, name)
		fi, err := r.fsys.Lstat(ToOS(next, r.goos))
		if err != nil {
			if isNotExist(err) {
				return joinSlash(cur, rest), nil
			}
			return "", err
		}
		target, ok, err := r.link(next, fi)
		if err != nil {
			return "", err
		}
		if ok {
			if err := r.hop(p); err != nil {
				return "", err
			}
			np := normalize(target, cur, "", r.goos)
			if np == "" {
				cur, rest = next, tail
				continue
			}
			if tail != "" {
				np = joinSlash(np, tail)
			}
			return r.walk(np)
		}
		cur, rest = next, tail
	}
	return cur, nil
}

// rootLen returns the length of the root prefix of an internal-form windows
// path ("c:/", "//srv/share", "//?/Volume{GUID}", "/"), or 0 for a relative one.
func rootLen(p string) int {
	switch {
	case isDriveAbs(p, true):
		return min(3, len(p))
	case isUNC(p):
		_, rest, _ := strings.Cut(p[2:], "/")
		share, _, _ := strings.Cut(rest, "/")
		if rest == "" {
			return len(p)
		}
		return len(p) - len(rest) + len(share)
	case isDevice(p):
		dev, _, _ := strings.Cut(p[4:], "/")
		return 4 + len(dev)
	case p != "" && p[0] == '/':
		return 1
	}
	return 0
}

// eval resolves p with EvalSymlinks. For a path that does not exist it
// Lstats upwards (a dangling symlink exists for Lstat) to the nearest
// existing ancestor, resolves that one component chain once and re-appends the
// missing remainder.
func (r *resolver) eval(p string) (string, error) {
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
	cur := p
	var tail []string // missing components, deepest first
	for {
		fi, lerr := r.fsys.Lstat(ToOS(cur, r.goos))
		if lerr == nil {
			return r.settle(p, cur, fi, tail)
		}
		if !isNotExist(lerr) {
			return "", lerr
		}
		parent, name, ok := splitParent(cur)
		if !ok {
			return p, nil
		}
		tail = append(tail, name)
		cur = parent
	}
}

// settle resolves the existing ancestor cur of p and re-appends tail.
func (r *resolver) settle(p, cur string, fi fs.FileInfo, tail []string) (string, error) {
	base := cur
	if cur != p { // EvalSymlinks(p) is already known to have failed
		out, err := r.fsys.EvalSymlinks(ToOS(cur, r.goos))
		switch {
		case err == nil:
			if n := r.fromOS(out); n != "" {
				base = n
			}
			return appendTail(base, tail), nil
		case !isNotExist(err):
			return "", err
		}
	}
	// cur exists but does not evaluate: a dangling link, or a plain race.
	target, ok, err := r.link(cur, fi)
	if err != nil {
		return "", err
	}
	if ok {
		if err := r.hop(p); err != nil {
			return "", err
		}
		parent, _, ok := splitParent(cur)
		if ok {
			// A relative target is interpreted by the OS against the physical
			// parent directory, which may itself sit behind a symlink.
			rp, err := r.eval(parent)
			if err != nil && !isNotExist(err) {
				return "", err
			}
			if err == nil {
				parent = rp
			}
		}
		return r.resolve(appendTail(normalize(target, parent, "", r.goos), tail))
	}
	return appendTail(base, tail), nil
}

func appendTail(base string, tail []string) string {
	for i := len(tail) - 1; i >= 0; i-- {
		base = joinSlash(base, tail[i])
	}
	return base
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
	if isDevice(p) && !strings.Contains(p[4:], "/") {
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
	return candidates(fsys, raw, ctx, func(root string) (string, bool) {
		rr, err := RealPath(fsys, root, ctx.GOOS)
		return rr, err == nil
	})
}

// candidates is Candidates with a pluggable (cacheable) project-root resolver.
func candidates(fsys sys.FS, raw string, ctx model.PathContext, realRoot func(root string) (string, bool)) []string {
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
	rootReal := ""
	if fsys != nil {
		if real, err := RealPath(fsys, norm, goos); err == nil && real != norm {
			forms = append(forms, real)
		}
		if phys, ok := physicalDotDot(fsys, raw, ctx); ok && !slices.Contains(forms, phys) {
			forms = append(forms, phys)
		}
		if root != "" {
			if rr, ok := realRoot(root); ok && rr != root {
				rootReal = rr
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
	if rootReal != "" {
		for _, f := range forms {
			if rel, ok := Rel(rootReal, f, goos); ok && rel != "." {
				add(rel)
			}
		}
	}
	return out
}

// physicalDotDot resolves raw the way a POSIX kernel (or a shell) does: ".."
// applies to the physical directory reached so far, not to the preceding
// spelled component, so "link/../x" is "<target of link>/../x". normalize
// folds ".." lexically, which is right for Win32 but would let a symlinked
// directory hide a write from the double check on POSIX. ok is false when
// goos is windows, raw has no ".." component, raw is not a POSIX absolute
// path after home/cwd expansion (drive paths such as "c:/x" are never
// joined to cwd), or resolution failed; otherwise real is the
// fully resolved path.
func physicalDotDot(fsys sys.FS, raw string, ctx model.PathContext) (real string, ok bool) {
	goos := ctx.GOOS
	if goos == "windows" || !strings.Contains(raw, "..") {
		return "", false
	}
	s := ToSlashForm(raw, goos)
	if s == "~" || strings.HasPrefix(s, "~/") {
		if ctx.Home == "" {
			return "", false
		}
		s = ExpandHome(s, normalize(ctx.Home, "", "", goos))
	}
	if s == "" || isDriveAbs(s, false) {
		// A Windows-client drive path ("c:/x/../y") is absolute for normalize
		// on every GOOS; it must not be joined to a POSIX cwd.
		return "", false
	}
	if s[0] != '/' {
		cwd := normalize(ctx.Cwd, "", ctx.Home, goos)
		if cwd == "" || cwd[0] != '/' {
			return "", false
		}
		s = joinSlash(cwd, s)
	}
	hasDots := false
	for c := range strings.SplitSeq(s, "/") {
		if c == ".." {
			hasDots = true
			break
		}
	}
	if !hasDots {
		return "", false
	}
	cur := "/"
	for c := range strings.SplitSeq(s, "/") {
		switch c {
		case "", ".":
		case "..":
			rp, err := RealPath(fsys, cur, goos)
			if err != nil {
				return "", false
			}
			if parent, _, pok := splitParent(rp); pok {
				cur = parent
			} else {
				cur = "/"
			}
		default:
			cur = joinSlash(cur, c)
		}
	}
	real, err := RealPath(fsys, cur, goos)
	if err != nil {
		return "", false
	}
	return real, true
}

type rootKey struct{ root, goos string }

// Service is the ports.PathService implementation. It caches the resolved
// ProjectRoot and Home per (root, GOOS) for its lifetime, so the symlink layout of the
// project root is assumed stable while a Service lives (one hook process).
type Service struct {
	FS sys.FS

	roots sync.Map // rootKey → string (resolved root)
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
	return candidates(s.FS, raw, ctx, func(root string) (string, bool) {
		return s.realRoot(root, ctx.GOOS)
	})
}

// realRoot resolves root (a ProjectRoot or Home) once per Service.
func (s *Service) realRoot(root, goos string) (string, bool) {
	k := rootKey{root, goos}
	if v, ok := s.roots.Load(k); ok {
		return v.(string), true
	}
	rr, err := RealPath(s.FS, root, goos)
	if err != nil {
		return "", false
	}
	s.roots.Store(k, rr)
	return rr, true
}

// CompileGlobs implements ports.PathService. When the home directory is
// itself reached through a symlink, every "~" pattern is also compiled
// against the resolved home, so a resolved candidate such as
// /data/u/.ballast/x still matches "~/.ballast/**".
func (s *Service) CompileGlobs(patterns []string, ctx model.PathContext) (ports.GlobMatcher, error) {
	home := ctx.Home
	if home != "" {
		home = normalize(home, "", "", ctx.GOOS)
	}
	opt := GlobOptions{Home: home, CaseInsensitive: IsCaseInsensitive(ctx.GOOS)}
	list := patterns
	if s.FS != nil && home != "" && len(patterns) > 0 {
		if rh, ok := s.realRoot(home, ctx.GOOS); ok && rh != home {
			list = append([]string(nil), patterns...)
			for _, p := range patterns {
				neg := strings.HasPrefix(p, "!")
				if body := strings.TrimPrefix(p, "!"); body == "~" || strings.HasPrefix(body, "~/") {
					alt := escapeGlob(strings.TrimSuffix(rh, "/")) + body[1:]
					if neg {
						alt = "!" + alt
					}
					list = append(list, alt)
				}
			}
		}
	}
	list = s.expandClientRoots(list, home, ctx)
	g, err := CompileGlobs(list, opt)
	if err != nil {
		return nil, err // never return a typed-nil *GlobSet inside the interface
	}
	g.patterns = append([]string(nil), patterns...)
	return g, nil
}

// clientRootMacros pairs the literal home-relative client config directories
// with the redirected root the client really loads (CLAUDE_CONFIG_DIR,
// CODEX_HOME, HERMES_HOME).
func clientRootMacros(r model.ClientRoots) [3]struct{ prefix, root string } {
	return [3]struct{ prefix, root string }{
		{"~/.claude", r.Claude}, {"~/.codex", r.Codex}, {"~/.hermes", r.Hermes},
	}
}

// expandClientRoots appends, for every "~/.claude…", "~/.codex…" or
// "~/.hermes…" pattern, the same pattern rooted at the redirected client config
// root (and at its symlink-resolved form), so the literal and the redirected
// location are both protected. A root equal to the default adds nothing.
func (s *Service) expandClientRoots(list []string, home string, ctx model.PathContext) []string {
	// out starts as list itself; the first append copies it (slices.Clip
	// forces a reallocation), so the caller's backing array is never written.
	out := slices.Clip(list)
	for _, m := range clientRootMacros(ctx.ClientRoots) {
		if m.root == "" {
			continue
		}
		root := normalize(m.root, "", "", ctx.GOOS)
		if !isAbs(root, ctx.GOOS == "windows") {
			continue
		}
		roots := []string{root}
		if s.FS != nil {
			if rr, ok := s.realRoot(root, ctx.GOOS); ok && rr != root {
				roots = append(roots, rr)
			}
		}
		for _, p := range list {
			neg := strings.HasPrefix(p, "!")
			body := strings.TrimPrefix(p, "!")
			if body != m.prefix && !strings.HasPrefix(body, m.prefix+"/") && !strings.HasPrefix(body, m.prefix+"*") {
				continue
			}
			rest := body[len(m.prefix):]
			for _, r := range roots {
				if home != "" && strings.TrimSuffix(r, "/") == strings.TrimSuffix(home, "/")+m.prefix[1:] {
					continue
				}
				alt := escapeGlob(strings.TrimSuffix(r, "/")) + rest
				if neg {
					alt = "!" + alt
				}
				out = append(out, alt)
			}
		}
	}
	return out
}
