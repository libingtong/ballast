package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"syscall"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

// isNotExist reports a missing file, including a parent that is not a directory.
func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// loadMode tunes readLayers.
type loadMode struct {
	writeLKG bool // refresh LKG files for valid layers
	report   bool // print config_invalid diagnostics to stderr
}

// statLayers resolves the layer paths and stats them. A repo layer that is
// the same file as the org or user layer (cwd == $HOME outside git) is
// reported as missing so that file is not loaded twice.
func (l *Loader) statLayers(req model.LoadRequest) ([]LayerPath, []model.StatKey, error) {
	fsys := l.fs()
	paths := LayerPaths(l.opt.Layout, req)
	stats := make([]model.StatKey, len(paths))
	for i, p := range paths {
		if p.Layer == model.LayerRepo && p.Path != "" {
			c := filepath.Clean(p.Path)
			if c == filepath.Clean(paths[0].Path) || c == filepath.Clean(paths[2].Path) {
				paths[i].Path = ""
			}
		}
		st, err := StatFile(fsys, paths[i].Path)
		if err != nil {
			return nil, nil, err
		}
		stats[i] = st
	}
	return paths, stats, nil
}

func (l *Loader) fs() sys.FS {
	if l.opt.FS != nil {
		return l.opt.FS
	}
	return sys.OSFS{}
}

func (l *Loader) ids() sys.IDGen {
	if l.opt.IDs != nil {
		return l.opt.IDs
	}
	return sys.RandomID{}
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// lkgPath is cache/lkg/<layer>-<sha256(abs path)[:12]>.json.
func (l *Loader) lkgPath(layer model.Layer, path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(l.opt.Layout.LKGDir, fmt.Sprintf("%s-%s.json", layer, hex.EncodeToString(sum[:])[:12]))
}

func hasError(diags []model.Diagnostic) (model.Diagnostic, bool) {
	for _, d := range diags {
		if d.Severity == model.SevError {
			return d, true
		}
	}
	return model.Diagnostic{}, false
}

// readLayers reads and validates every layer, applying LKG substitution.
// Only unexpected I/O errors are returned; bad rule files never are.
func (l *Loader) readLayers(paths []LayerPath, stats []model.StatKey, mode loadMode) ([]LayerRules, error) {
	fsys := l.fs()
	out := make([]LayerRules, len(paths))
	for i, p := range paths {
		src := model.LayerSource{Layer: p.Layer, Path: p.Path, Stat: stats[i], Status: model.LayerMissing}
		lr := LayerRules{Source: src}
		if !stats[i].Exists {
			out[i] = lr
			continue
		}
		data, err := fsys.ReadFile(p.Path)
		if err != nil {
			if isNotExist(err) {
				out[i] = lr
				continue
			}
			return nil, fmt.Errorf("config: read %s: %w", p.Path, err)
		}
		file, diags, perr := l.opt.Compiler.ParseFile(p.Layer, p.Path, data)
		cause := validationError(file, diags, perr)
		if cause == "" {
			lr.Source.Status = model.LayerOK
			lr.Source.Hash = hashHex(data)
			lr.File, lr.Diags = file, diags
			if mode.writeLKG {
				l.refreshLKG(p, data, lr.Source.Hash)
			}
			out[i] = lr
			continue
		}

		lr.Diags = diags
		lr.Source.Error = cause
		lr.Source.Status = model.LayerInvalid
		if lkg, ok := l.readLKG(p); ok {
			lr.Source.Status = model.LayerLKG
			lr.Source.Hash = hashHex(lkg.data)
			lr.File = lkg.file
		}
		if mode.report {
			if lr.Source.Status == model.LayerLKG {
				l.diag("%s rule file %s is invalid (%s); using last-known-good", p.Layer, p.Path, cause)
			} else {
				l.diag("%s rule file %s is invalid (%s); layer ignored, no last-known-good", p.Layer, p.Path, cause)
			}
		}
		out[i] = lr
	}
	return out, nil
}

// validationError returns "" for a valid layer, else a short cause.
func validationError(file *model.RuleFile, diags []model.Diagnostic, err error) string {
	if err != nil {
		return err.Error()
	}
	if file == nil {
		return "empty parse result"
	}
	if d, bad := hasError(diags); bad {
		if d.Code != "" {
			return d.Code + ": " + d.Message
		}
		return d.Message
	}
	return ""
}

type lkgEntry struct {
	data []byte
	file *model.RuleFile
}

// readLKG loads and re-validates the layer's last-known-good bytes.
func (l *Loader) readLKG(p LayerPath) (lkgEntry, bool) {
	data, err := l.fs().ReadFile(l.lkgPath(p.Layer, p.Path))
	if err != nil {
		return lkgEntry{}, false
	}
	file, diags, perr := l.opt.Compiler.ParseFile(p.Layer, p.Path, data)
	if validationError(file, diags, perr) != "" {
		return lkgEntry{}, false
	}
	return lkgEntry{data: data, file: file}, true
}

// refreshLKG rewrites the LKG file when the valid layer's hash changed.
// Failures are diagnostics, never errors.
func (l *Loader) refreshLKG(p LayerPath, data []byte, hash string) {
	path := l.lkgPath(p.Layer, p.Path)
	if old, err := l.fs().ReadFile(path); err == nil && hashHex(old) == hash {
		return
	}
	if err := sys.WriteFileAtomic(l.fs(), l.ids(), path, data, 0o600, false); err != nil {
		l.diag("cannot write last-known-good for %s: %v", p.Path, err)
	}
}
