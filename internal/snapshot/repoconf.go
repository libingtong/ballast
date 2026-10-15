package snapshot

import (
	"bytes"
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// repoSettings are the git settings that decide whether a native computation
// is bit-identical to git. They are read from system, global, local and
// per-worktree config (later files win); unconditional [include] paths are
// followed, [includeIf] files are only probed for relevant keys.
type repoSettings struct {
	objectFormat string // "sha1" (default), "sha256", ...
	fileMode     bool   // core.filemode
	symlinks     bool   // core.symlinks
	// convRisk: core.autocrlf is true/input or core.eol is set. Such a
	// setting can only alter content that contains CRLF.
	convRisk  bool
	safeCRLF  bool     // core.safecrlf=true (git may die instead of converting)
	attrFiles []string // extra attributes files (core.attributesFile or the XDG default, system)
	// uncertain is non-empty when some config could not be evaluated (a
	// conditional include or an environment override that sets a relevant
	// key): native computation cannot be trusted.
	uncertain string
}

// relevantConfigKey reports whether a section.key influences native
// equivalence; used to probe configuration we cannot fully evaluate.
func relevantConfigKey(section, key string) bool {
	switch section {
	case "core":
		switch key {
		case "autocrlf", "eol", "safecrlf", "symlinks", "filemode", "attributesfile":
			return true
		}
	case "extensions":
		return key == "objectformat"
	}
	return false
}

func osIsWindows(env sys.Env) bool {
	if env == nil {
		return false
	}
	return env.GOOS() == "windows"
}

// systemConfigPaths lists the places a system-wide gitconfig may live.
func systemConfigPaths(env sys.Env) []string {
	if v, ok := env.Getenv("GIT_CONFIG_NOSYSTEM"); ok {
		if b, ok2 := cfgBool(v, true); ok2 && b {
			return nil
		}
	}
	if v, ok := env.Getenv("GIT_CONFIG_SYSTEM"); ok {
		if v == "" {
			return nil
		}
		return []string{v}
	}
	if osIsWindows(env) {
		var out []string
		for _, k := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
			if v, ok := env.Getenv(k); ok && v != "" {
				out = append(out, filepath.Join(v, "Git", "etc", "gitconfig"))
			}
		}
		if v, ok := env.Getenv("ProgramData"); ok && v != "" {
			out = append(out, filepath.Join(v, "Git", "config"))
		}
		return out
	}
	return []string{
		"/etc/gitconfig",
		"/usr/local/etc/gitconfig",
		"/opt/homebrew/etc/gitconfig",
		"/Library/Developer/CommandLineTools/usr/share/git-core/gitconfig",
		"/Applications/Xcode.app/Contents/Developer/usr/share/git-core/gitconfig",
	}
}

// globalConfigPaths lists the user-level gitconfig files in precedence order.
func globalConfigPaths(env sys.Env) []string {
	if v, ok := env.Getenv("GIT_CONFIG_GLOBAL"); ok {
		if v == "" {
			return nil
		}
		return []string{v}
	}
	var out []string
	home, herr := env.HomeDir()
	if x, ok := env.Getenv("XDG_CONFIG_HOME"); ok && x != "" {
		out = append(out, filepath.Join(x, "git", "config"))
	} else if herr == nil {
		out = append(out, filepath.Join(home, ".config", "git", "config"))
	}
	if herr == nil {
		out = append(out, filepath.Join(home, ".gitconfig"))
	}
	return out
}

func expandHome(env sys.Env, p string) string {
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		if home, err := env.HomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// loadSettings gathers the settings for repo. env == nil restricts the
// lookup to the repository's own config (no system/global files, no
// environment overrides).
func loadSettings(fsys sys.FS, env sys.Env, repo *model.RepoInfo) *repoSettings {
	s := &repoSettings{objectFormat: "sha1", fileMode: !osIsWindows(env), symlinks: true}
	if repo.ObjectFormat != "" {
		s.objectFormat = strings.ToLower(repo.ObjectFormat)
	}
	var files []string
	if env != nil {
		files = append(files, systemConfigPaths(env)...)
		files = append(files, globalConfigPaths(env)...)
	}
	files = append(files, filepath.Join(repo.CommonDir, "config"))
	if repo.GitDir != "" {
		files = append(files, filepath.Join(repo.GitDir, "config.worktree"))
	}
	for _, f := range files {
		s.applyFile(fsys, env, f, 0)
	}
	if env != nil {
		s.checkEnvConfig(env)
		if s.uncertain == "" {
			s.addDefaultAttrFiles(env)
		}
	}
	return s
}

// checkEnvConfig flags configuration injected through the environment
// (`git -c`, GIT_CONFIG_COUNT) that touches a setting we depend on.
func (s *repoSettings) checkEnvConfig(env sys.Env) {
	if v, ok := env.Getenv("GIT_CONFIG_PARAMETERS"); ok && v != "" {
		lv := strings.ToLower(v)
		for _, w := range []string{"autocrlf", "core.eol", "safecrlf", "symlinks", "filemode", "attributesfile", "include", "objectformat"} {
			if strings.Contains(lv, w) {
				s.uncertain = "environment config override (GIT_CONFIG_PARAMETERS)"
				return
			}
		}
	}
	if v, ok := env.Getenv("GIT_CONFIG_COUNT"); ok && v != "" {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 || n > 256 {
			s.uncertain = "unparsable GIT_CONFIG_COUNT"
			return
		}
		for i := 0; i < n; i++ {
			key, _ := env.Getenv("GIT_CONFIG_KEY_" + strconv.Itoa(i))
			key = strings.ToLower(key)
			section, name := key, ""
			if j := strings.LastIndexByte(key, '.'); j >= 0 {
				section, name = key[:j], key[j+1:]
			}
			if relevantConfigKey(section, name) || strings.HasPrefix(section, "include") {
				s.uncertain = "environment config override (GIT_CONFIG_KEY_" + strconv.Itoa(i) + ")"
				return
			}
		}
	}
}

func (s *repoSettings) addDefaultAttrFiles(env sys.Env) {
	if len(s.attrFiles) == 0 { // core.attributesFile replaces the XDG default
		if x, ok := env.Getenv("XDG_CONFIG_HOME"); ok && x != "" {
			s.attrFiles = []string{filepath.Join(x, "git", "attributes")}
		} else if home, err := env.HomeDir(); err == nil {
			s.attrFiles = []string{filepath.Join(home, ".config", "git", "attributes")}
		}
	}
	if osIsWindows(env) {
		return
	}
	if v, ok := env.Getenv("GIT_ATTR_SYSTEM"); ok {
		if v != "" {
			s.attrFiles = append(s.attrFiles, v)
		}
		return
	}
	s.attrFiles = append(s.attrFiles, "/etc/gitattributes", "/usr/local/etc/gitattributes", "/opt/homebrew/etc/gitattributes")
}

func (s *repoSettings) applyFile(fsys sys.FS, env sys.Env, file string, depth int) {
	data, err := fsys.ReadFile(file)
	if err != nil {
		return
	}
	dir := filepath.Dir(file)
	scanConfig(data, func(section, sub, key, raw string, hasEq bool) {
		switch {
		case section == "include" && key == "path" && hasEq:
			if depth >= 8 {
				return
			}
			p := cfgString(raw)
			if env != nil {
				p = expandHome(env, p)
			}
			if !filepath.IsAbs(p) {
				p = filepath.Join(dir, p)
			}
			s.applyFile(fsys, env, p, depth+1)
		case section == "includeif" && key == "path" && hasEq:
			p := cfgString(raw)
			if env != nil {
				p = expandHome(env, p)
			}
			if !filepath.IsAbs(p) {
				p = filepath.Join(dir, p)
			}
			if inc, err := fsys.ReadFile(p); err == nil {
				scanConfig(inc, func(sec, _, k, _ string, _ bool) {
					if relevantConfigKey(sec, k) {
						s.uncertain = "conditional include sets " + sec + "." + k
					}
				})
			}
		case section == "core" && sub == "":
			s.applyCore(env, key, raw, hasEq)
		case section == "extensions" && sub == "" && key == "objectformat" && hasEq:
			s.objectFormat = strings.ToLower(cfgString(raw))
		}
	})
}

func (s *repoSettings) applyCore(env sys.Env, key, raw string, hasEq bool) {
	switch key {
	case "filemode":
		if v, ok := cfgBool(raw, hasEq); ok {
			s.fileMode = v
		} else {
			s.uncertain = "unparsable core.filemode"
		}
	case "symlinks":
		if v, ok := cfgBool(raw, hasEq); ok {
			s.symlinks = v
		} else {
			s.uncertain = "unparsable core.symlinks"
		}
	case "autocrlf":
		if hasEq && strings.EqualFold(cfgString(raw), "input") {
			s.convRisk = true
			return
		}
		v, ok := cfgBool(raw, hasEq)
		if !ok {
			s.uncertain = "unparsable core.autocrlf"
			return
		}
		s.convRisk = v
	case "eol":
		if hasEq && cfgString(raw) != "" {
			s.convRisk = true
		}
	case "safecrlf":
		if hasEq && strings.EqualFold(cfgString(raw), "warn") {
			s.safeCRLF = false
			return
		}
		v, ok := cfgBool(raw, hasEq)
		if !ok {
			s.uncertain = "unparsable core.safecrlf"
			return
		}
		s.safeCRLF = v
	case "attributesfile":
		if hasEq {
			p := cfgString(raw)
			if env != nil {
				p = expandHome(env, p)
			}
			s.attrFiles = []string{p}
		}
	}
}

// attrFlags summarises the conversion-relevant attributes found in
// .gitattributes style files.
type attrFlags struct {
	hard bool // filter / ident / working-tree-encoding: arbitrary content rewrite
	soft bool // text / eol / crlf: only CRLF content can change
}

func (a attrFlags) or(b attrFlags) attrFlags {
	return attrFlags{hard: a.hard || b.hard, soft: a.soft || b.soft}
}

// scanAttributes inspects every attribute token of an attributes file. It
// does not match patterns: any line (or macro definition) that mentions a
// conversion attribute flags the whole file, which is conservative.
func scanAttributes(data []byte) attrFlags {
	var fl attrFlags
	for len(data) > 0 {
		var line []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			line, data = data, nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		var rest []byte
		if line[0] == '"' {
			// Quoted pattern: skip to the closing quote.
			i := 1
			for i < len(line) && line[i] != '"' {
				if line[i] == '\\' {
					i++
				}
				i++
			}
			if i >= len(line) {
				continue
			}
			rest = line[i+1:]
		} else {
			i := bytes.IndexAny(line, " \t")
			if i < 0 {
				continue
			}
			rest = line[i:]
		}
		for _, tok := range bytes.Fields(rest) {
			name := string(tok)
			name = strings.TrimLeft(name, "-!")
			if i := strings.IndexByte(name, '='); i >= 0 {
				name = name[:i]
			}
			switch name {
			case "filter", "ident", "working-tree-encoding":
				fl.hard = true
			case "text", "eol", "crlf":
				fl.soft = true
			}
		}
	}
	return fl
}

// attrResolver answers, per directory, which conversion attributes may apply
// to the files below it: repository-level sources plus every .gitattributes
// on the way from the work tree root. Results are cached per directory.
type attrResolver struct {
	fs   sys.FS
	root string
	base attrFlags
	dirs map[string]attrFlags
}

func newAttrResolver(fsys sys.FS, repo *model.RepoInfo, s *repoSettings) *attrResolver {
	r := &attrResolver{fs: fsys, root: repo.Toplevel, dirs: map[string]attrFlags{}}
	files := append([]string{filepath.Join(repo.CommonDir, "info", "attributes")}, s.attrFiles...)
	for _, f := range files {
		if f == "" {
			continue
		}
		if data, err := fsys.ReadFile(f); err == nil {
			r.base = r.base.or(scanAttributes(data))
		}
	}
	return r
}

// flagsFor returns the flags that apply to a file inside dir (slash form,
// "" for the root).
func (r *attrResolver) flagsFor(dir string) (attrFlags, error) {
	if fl, ok := r.dirs[dir]; ok {
		return fl, nil
	}
	parent := r.base
	if dir != "" {
		pdir := path.Dir(dir)
		if pdir == "." {
			pdir = ""
		}
		var err error
		if parent, err = r.flagsFor(pdir); err != nil {
			return attrFlags{}, err
		}
	}
	fl := parent
	data, err := r.fs.ReadFile(filepath.Join(r.root, filepath.FromSlash(dir), ".gitattributes"))
	switch {
	case err == nil:
		fl = fl.or(scanAttributes(data))
	case errors.Is(err, fs.ErrNotExist), isNotDir(err):
	default:
		return attrFlags{}, err
	}
	r.dirs[dir] = fl
	return fl, nil
}
