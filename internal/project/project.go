package project

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
	"github.com/libingtong/ballast/internal/sys"
)

// Limits and defaults (package doc).
const (
	// DefaultTimeoutS is the timeout of an entry without timeout_s.
	DefaultTimeoutS = 1800
	// MaxTimeoutS is the largest accepted timeout_s.
	MaxTimeoutS = 86400
	// FileVersion is the only accepted "version".
	FileVersion = 1
)

// Loader implements ports.ProjectLoader.
type Loader struct {
	FS sys.FS
}

var _ ports.ProjectLoader = (*Loader)(nil)

// LoadProject implements ports.ProjectLoader: <root>/.ballast/project.json.
// A missing file yields (nil, nil); unreadable or invalid content an error
// that names the file.
func (l *Loader) LoadProject(root string) (*model.ProjectConfig, error) {
	cfg, _, err := l.LoadProjectBytes(root)
	return cfg, err
}

// LoadProjectBytes is LoadProject that also returns the raw file bytes (for
// Hash). A missing file yields (nil, nil, nil).
func (l *Loader) LoadProjectBytes(root string) (*model.ProjectConfig, []byte, error) {
	fsys := l.FS
	if fsys == nil {
		fsys = sys.OSFS{}
	}
	p := filepath.Join(root, ".ballast", "project.json")
	data, err := fsys.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("project: read %s: %w", p, err)
	}
	cfg, err := Decode(data)
	if err != nil {
		return nil, nil, fmt.Errorf("project: %s: %w", p, err)
	}
	return cfg, data, nil
}

// rawSpec mirrors model.VerifySpec but keeps absent and explicit-zero
// timeout_s apart.
type rawSpec struct {
	Name     string   `json:"name"`
	Argv     []string `json:"argv"`
	TimeoutS *int     `json:"timeout_s"`
	Required bool     `json:"required"`
	Env      []string `json:"env"`
	Cwd      string   `json:"cwd"`
}

type rawConfig struct {
	Version *int      `json:"version"`
	Verify  []rawSpec `json:"verify"`
	Exempt  []string  `json:"exempt"`
}

// Decode strictly decodes and validates project.json bytes. Unknown fields,
// trailing data and every violation of the package-doc rules are errors.
// timeout_s stays 0 when absent (readers apply DefaultTimeoutS).
func Decode(data []byte) (*model.ProjectConfig, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var raw rawConfig
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("invalid project.json: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("invalid project.json: trailing data after the top-level object")
	}
	cfg := &model.ProjectConfig{}
	if raw.Version != nil {
		if *raw.Version != FileVersion {
			return nil, fmt.Errorf("invalid project.json: unsupported version %d (want %d)", *raw.Version, FileVersion)
		}
		cfg.Version = *raw.Version
	}
	seen := make(map[string]bool, len(raw.Verify))
	for i, rs := range raw.Verify {
		if err := validateName(rs.Name); err != nil {
			return nil, fmt.Errorf("invalid project.json: verify[%d]: %w", i, err)
		}
		if seen[rs.Name] {
			return nil, fmt.Errorf("invalid project.json: verify[%d]: duplicate name %q", i, rs.Name)
		}
		seen[rs.Name] = true
		if len(rs.Argv) == 0 {
			return nil, fmt.Errorf("invalid project.json: verify %q: argv is empty", rs.Name)
		}
		if rs.Argv[0] == "" {
			return nil, fmt.Errorf("invalid project.json: verify %q: argv[0] is empty", rs.Name)
		}
		for _, a := range rs.Argv {
			if strings.ContainsRune(a, 0) {
				return nil, fmt.Errorf("invalid project.json: verify %q: argv contains NUL", rs.Name)
			}
		}
		spec := model.VerifySpec{Name: rs.Name, Argv: rs.Argv, Required: rs.Required, Env: rs.Env, Cwd: rs.Cwd}
		if rs.TimeoutS != nil {
			t := *rs.TimeoutS
			if t < 1 || t > MaxTimeoutS {
				return nil, fmt.Errorf("invalid project.json: verify %q: timeout_s %d out of range 1..%d", rs.Name, t, MaxTimeoutS)
			}
			spec.TimeoutS = t
		}
		for _, kv := range rs.Env {
			k, _, ok := strings.Cut(kv, "=")
			if !ok || k == "" || strings.ContainsRune(kv, 0) {
				return nil, fmt.Errorf("invalid project.json: verify %q: env entry %q is not KEY=VALUE", rs.Name, kv)
			}
		}
		if err := validateCwd(rs.Cwd); err != nil {
			return nil, fmt.Errorf("invalid project.json: verify %q: %w", rs.Name, err)
		}
		cfg.Verify = append(cfg.Verify, spec)
	}
	for i, g := range raw.Exempt {
		if err := validateExempt(g); err != nil {
			return nil, fmt.Errorf("invalid project.json: exempt[%d]: %w", i, err)
		}
	}
	cfg.Exempt = raw.Exempt
	return cfg, nil
}

// validateName checks ^[a-z0-9][a-z0-9_-]{0,31}$ without regexp (hot path).
func validateName(n string) error {
	if n == "" || len(n) > 32 {
		return fmt.Errorf("name %q must be 1..32 characters", n)
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '_' || c == '-') && i > 0:
		default:
			return fmt.Errorf("name %q must match ^[a-z0-9][a-z0-9_-]{0,31}$", n)
		}
	}
	return nil
}

// validateCwd accepts "" (the repo root) or a relative path that stays
// inside the repository. Both separators count, whatever the platform.
func validateCwd(c string) error {
	if c == "" {
		return nil
	}
	if strings.ContainsRune(c, 0) {
		return errors.New("cwd contains NUL")
	}
	if filepath.IsAbs(c) || strings.HasPrefix(c, "/") || strings.HasPrefix(c, `\`) || (len(c) >= 2 && c[1] == ':') {
		return fmt.Errorf("cwd %q must be relative", c)
	}
	for _, part := range strings.FieldsFunc(c, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return fmt.Errorf("cwd %q must not contain \"..\"", c)
		}
	}
	return nil
}

// validateExempt rejects what snapshot.CompileExempt would reject (empty,
// NUL, absolute, escaping the repo) plus anything doublestar cannot parse.
func validateExempt(g string) error {
	if g == "" {
		return errors.New("empty glob")
	}
	if strings.ContainsRune(g, 0) {
		return fmt.Errorf("glob %q contains NUL", g)
	}
	if strings.HasPrefix(g, "/") {
		return fmt.Errorf("glob %q must be repo-relative", g)
	}
	for _, part := range strings.Split(g, "/") {
		if part == ".." {
			return fmt.Errorf("glob %q must not contain \"..\"", g)
		}
	}
	if !doublestar.ValidatePattern(g) {
		return fmt.Errorf("glob %q is not a valid doublestar pattern", g)
	}
	return nil
}

// Hash returns hex(sha256(data)) (VerifyRecord.ProjectJSONHash).
func Hash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
