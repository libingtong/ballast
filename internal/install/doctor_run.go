package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

const (
	diskWarnBytes = 100 << 20
	diskFailBytes = 10 << 20
)

func runDoctor(opt DoctorOptions) []Check {
	fsys := opt.FS
	if fsys == nil {
		fsys = sys.OSFS{}
	}
	lay := opt.Layout
	goos := envGOOS(lay, opt.Env)
	var out []Check
	add := func(c ...Check) { out = append(out, c...) }

	m, merr := LoadManifest(fsys, lay.Manifest)
	if m == nil {
		m = &model.InstallManifest{V: model.ManifestVersion}
	}
	binary := firstNonEmpty(opt.Binary, m.Binary)

	add(binaryExists(fsys, binary, goos), binaryStable(binary, opt.Env))
	add(
		writableCheck("home.writable", fsys, lay.Home, "~/.ballast"),
		writableCheck("audit.writable", fsys, lay.AuditDir, "the audit directory"),
		statePermissions(fsys, lay, goos),
	)
	switch {
	case merr != nil:
		add(Check{ID: "manifest.valid", Status: CheckFail, Message: merr.Error(),
			Fix: "repair or delete " + lay.Manifest + " (uninstall reads it)"})
	case manifestEmpty(m):
		add(Check{ID: "manifest.valid", Status: CheckOK, Message: "no install manifest entries (nothing installed)"})
	default:
		add(Check{ID: "manifest.valid", Status: CheckOK,
			Message: fmt.Sprintf("%d entries, %d files, %d flags recorded", len(m.Entries), len(m.Files), len(m.Flags))})
	}
	add(rulesLint(opt))
	managedFiles, _ := readManagedSettings(lay, fsys)
	add(tierChecks(opt, fsys, lay, goos, firstNonEmpty(opt.Self, binary))...)
	add(Check{ID: "ci.final_arbiter", Status: CheckOK,
		Message: "server-side CI is the final arbiter; in-session rules steer the agent but do not guarantee delivery quality"})

	managed := false
	for _, f := range managedFiles {
		if managedHasBallast(f.doc, goos) {
			managed = true
		}
	}
	add(managedDuplicate(managed, m))

	for _, c := range Clients() {
		if !hasRecords(m, c.ID()) {
			continue
		}
		add(c.Doctor(lay, fsys, m)...)
		if c.ID() == model.ClientCursor {
			add(mdcCheck(lay, fsys, m, opt.MDC))
		}
	}
	add(diskCheck(fsys, lay))
	return out
}

func binaryExists(fsys sys.FS, binary, goos string) Check {
	c := Check{ID: "binary.exists"}
	if binary == "" {
		c.Status, c.Message, c.Fix = CheckWarn, "no ballast binary path known (not installed yet)", "run: ballast install --client <id>"
		return c
	}
	fi, err := fsys.Stat(binary)
	switch {
	case err != nil:
		c.Status, c.Message = CheckFail, fmt.Sprintf("%s does not exist: hooks would fail", binary)
		c.Fix = "reinstall with: ballast install --client <id> --binary <path>"
	case fi.IsDir():
		c.Status, c.Message = CheckFail, binary+" is a directory"
	case goos != "windows" && fi.Mode().Perm()&0o111 == 0:
		c.Status, c.Message = CheckFail, binary+" is not executable"
		c.Fix = "chmod +x " + binary
	default:
		c.Status, c.Message = CheckOK, binary
	}
	return c
}

func binaryStable(binary string, env sys.Env) Check {
	c := Check{ID: "binary.stable_path"}
	switch {
	case binary == "":
		c.Status, c.Message = CheckWarn, "not evaluated: no ballast binary path known"
	case unstableBinary(binary, env):
		c.Status, c.Message = CheckWarn, binary+" is under a temporary or build directory"
		c.Fix = "install the binary to a stable path and rerun: ballast install --client <id> --binary <path>"
	default:
		c.Status, c.Message = CheckOK, "binary path is not in a temporary directory"
	}
	return c
}

// writableCheck probes dir (or its nearest existing ancestor) by creating
// and removing a file.
func writableCheck(id string, fsys sys.FS, dir, label string) Check {
	c := Check{ID: id}
	probeDir, missing := dir, false
	for {
		fi, err := fsys.Stat(probeDir)
		if err == nil && fi.IsDir() {
			break
		}
		missing = true
		parent := filepath.Dir(probeDir)
		if parent == probeDir {
			c.Status, c.Message = CheckFail, "cannot find an existing parent directory of "+dir
			return c
		}
		probeDir = parent
	}
	probe := filepath.Join(probeDir, fmt.Sprintf(".ballast-doctor-%d.tmp", os.Getpid()))
	f, err := fsys.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		c.Status, c.Message = CheckFail, fmt.Sprintf("%s is not writable: %v", probeDir, err)
		c.Fix = "fix the permissions of " + probeDir
		return c
	}
	_ = f.Close()
	_ = fsys.Remove(probe)
	c.Status = CheckOK
	c.Message = label + " is writable"
	if missing {
		c.Message = fmt.Sprintf("%s does not exist yet; %s is writable", dir, probeDir)
	}
	return c
}

func statePermissions(fsys sys.FS, lay layout.Layout, goos string) Check {
	c := Check{ID: "state.permissions"}
	if goos == "windows" {
		c.Status, c.Message = CheckOK, "permission bits are not evaluated on Windows"
		return c
	}
	var bad []string
	for _, d := range []string{lay.Home, lay.StateDir, lay.AuditDir, lay.BackupsDir} {
		fi, err := fsys.Stat(d)
		if err != nil || !fi.IsDir() {
			continue
		}
		if fi.Mode().Perm()&0o077 != 0 {
			bad = append(bad, fmt.Sprintf("%s (%04o)", d, fi.Mode().Perm()))
		}
	}
	if len(bad) > 0 {
		c.Status = CheckWarn
		c.Message = "directories readable by other users: " + strings.Join(bad, ", ")
		c.Fix = "chmod 700 on those directories"
		return c
	}
	c.Status, c.Message = CheckOK, "state directories are private (0700)"
	return c
}

func rulesLint(opt DoctorOptions) Check {
	c := Check{ID: "rules.lint"}
	if opt.LintRules == nil {
		c.Status, c.Message = CheckWarn, "not evaluated: no rule stack supplied"
		return c
	}
	var errs, warns int
	first := ""
	for _, d := range opt.LintRules() {
		switch d.Severity {
		case model.SevError:
			errs++
			if first == "" {
				first = fmt.Sprintf("%s: %s", d.Code, d.Message)
			}
		case model.SevWarning:
			warns++
		}
	}
	switch {
	case errs > 0:
		c.Status, c.Message = CheckFail, fmt.Sprintf("%d lint errors, %d warnings (first: %s)", errs, warns, first)
		c.Fix = "run: ballast lint"
	case warns > 0:
		c.Status, c.Message = CheckWarn, fmt.Sprintf("%d lint warnings", warns)
		c.Fix = "run: ballast lint"
	default:
		c.Status, c.Message = CheckOK, "rules pass lint"
	}
	return c
}

func jget(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func managedDuplicate(managed bool, m *model.InstallManifest) Check {
	c := Check{ID: "claude.managed_duplicate", Client: model.ClientClaude}
	userLevel := false
	for _, e := range m.Entries {
		if e.Client == model.ClientClaude {
			userLevel = true
		}
	}
	switch {
	case managed && userLevel:
		c.Status = CheckFail
		c.Message = "Ballast hooks are installed both in managed settings and in the user settings; every hook runs twice"
		c.Fix = "run: ballast uninstall --client claude-code"
	case managed:
		c.Status, c.Message = CheckOK, "managed deployment present; no user-level duplicate"
	default:
		c.Status, c.Message = CheckOK, "no managed deployment"
	}
	return c
}

func diskCheck(fsys sys.FS, lay layout.Layout) Check {
	c := Check{ID: "disk.space"}
	dir := lay.Home
	for dir != "" {
		if fi, err := fsys.Stat(dir); err == nil && fi.IsDir() {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	free, ok := diskFree(dir)
	switch {
	case !ok:
		c.Status, c.Message = CheckWarn, "not evaluated: free disk space unavailable on this platform"
	case free < diskFailBytes:
		c.Status = CheckFail
		c.Message = fmt.Sprintf("only %d MiB free on the volume of %s: audit and state writes will fail and enforcement may degrade", free>>20, dir)
		c.Fix = "free disk space"
	case free < diskWarnBytes:
		c.Status = CheckWarn
		c.Message = fmt.Sprintf("only %d MiB free on the volume of %s", free>>20, dir)
		c.Fix = "free disk space"
	default:
		c.Status, c.Message = CheckOK, fmt.Sprintf("%d MiB free", free>>20)
	}
	return c
}
