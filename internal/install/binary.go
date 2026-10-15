package install

import (
	"fmt"
	"os"
	"strings"

	"github.com/libingtong/ballast/internal/sys"
)

// executablePath is os.Executable; tests replace it.
var executablePath = os.Executable

// isAbsPath reports whether p is absolute for the given target OS.
func isAbsPath(p, goos string) bool {
	if goos == "windows" {
		if strings.HasPrefix(p, `\\`) {
			return true
		}
		return len(p) >= 3 && (p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z') &&
			p[1] == ':' && (p[2] == '\\' || p[2] == '/')
	}
	return strings.HasPrefix(p, "/")
}

// unstableDirs are directory prefixes whose contents are transient.
var unstableDirs = []string{"/tmp", "/var/tmp", "/private/tmp", "/var/folders", "/private/var/folders"}

// unstableBinary reports whether an auto-resolved executable path lives in a
// temporary or build directory (go run / go test binaries), where a hook
// command would soon dangle.
func unstableBinary(p string, env sys.Env) bool {
	norm := strings.ReplaceAll(p, `\`, "/")
	lower := strings.ToLower(norm)
	if strings.Contains(norm, "/go-build") || strings.Contains(lower, "/appdata/local/temp/") {
		return true
	}
	dirs := append([]string{}, unstableDirs...)
	if env != nil {
		for _, k := range []string{"TMPDIR", "TEMP", "TMP"} {
			if v, ok := env.Getenv(k); ok && v != "" {
				dirs = append(dirs, v)
			}
		}
	}
	dirs = append(dirs, os.TempDir())
	for _, d := range dirs {
		d = strings.ToLower(strings.TrimRight(strings.ReplaceAll(d, `\`, "/"), "/"))
		if d != "" && strings.HasPrefix(lower, d+"/") {
			return true
		}
	}
	return false
}

// resolveBinary returns the absolute hook binary: Options.Binary when given,
// otherwise the (symlink-resolved) running executable.
func resolveBinary(opt Options, goos string) (string, error) {
	if opt.Binary != "" {
		if !isAbsPath(opt.Binary, goos) {
			return "", fmt.Errorf("%w: --binary must be an absolute path, got %q", ErrUsage, opt.Binary)
		}
		return opt.Binary, nil
	}
	exe, err := executablePath()
	if err != nil {
		return "", fmt.Errorf("%w: cannot determine the running executable: %v", ErrUsage, err)
	}
	if rp, err := opt.FS.EvalSymlinks(exe); err == nil {
		exe = rp
	}
	if unstableBinary(exe, opt.Env) {
		return "", fmt.Errorf("%w: %s is under a temporary or build directory; pass --binary <stable absolute path>", ErrBinaryUnstable, exe)
	}
	return exe, nil
}
