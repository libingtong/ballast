package install

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
)

func withDefaults(opt Options) Options {
	if opt.FS == nil {
		opt.FS = sys.OSFS{}
	}
	if opt.Env == nil {
		opt.Env = sys.OSEnv{}
	}
	if opt.Clock == nil {
		opt.Clock = sys.RealClock{}
	}
	if opt.IDs == nil {
		opt.IDs = sys.RandomID{}
	}
	return opt
}

// resolveClients validates ids and returns the clients in AllClients order.
func resolveClients(ids []model.ClientID) ([]Client, error) {
	want := map[model.ClientID]bool{}
	for _, id := range ids {
		if !id.Valid() {
			return nil, fmt.Errorf("%w: %w: %q", ErrUsage, model.ErrUnknownClient, string(id))
		}
		want[id] = true
	}
	var out []Client
	for _, c := range Clients() {
		if want[c.ID()] {
			out = append(out, c)
		}
	}
	return out, nil
}

func readOrNil(fsys sys.FS, path string) ([]byte, error) {
	b, err := fsys.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if b == nil {
		b = []byte{}
	}
	return b, nil
}

type plannedFile struct {
	client model.ClientID
	plan   *PlanResult
}

// mergeRecords replaces the manifest records of every planned (client, file)
// with the records of its plan.
func mergeRecords(m *model.InstallManifest, planned []plannedFile) *model.InstallManifest {
	out := *m
	out.Entries = append([]model.ManagedEntry(nil), m.Entries...)
	out.Files = append([]model.ManagedFile(nil), m.Files...)
	out.Flags = append([]model.ManagedFlag(nil), m.Flags...)
	out.Backups = append([]model.BackupRecord(nil), m.Backups...)
	for _, pf := range planned {
		path := pf.plan.Path
		out.Entries = filterEntries(out.Entries, func(e model.ManagedEntry) bool { return !(e.Client == pf.client && e.File == path) })
		out.Files = filterFiles(out.Files, func(f model.ManagedFile) bool { return f.Path != path })
		out.Flags = filterFlags(out.Flags, func(f model.ManagedFlag) bool { return !(f.Client == pf.client && f.File == path) })
	}
	for _, pf := range planned {
		out.Entries = append(out.Entries, pf.plan.Entries...)
		out.Files = append(out.Files, pf.plan.Files...)
		out.Flags = append(out.Flags, pf.plan.Flags...)
	}
	sortManifest(&out)
	return &out
}

func filterEntries(in []model.ManagedEntry, keep func(model.ManagedEntry) bool) []model.ManagedEntry {
	var out []model.ManagedEntry
	for _, e := range in {
		if keep(e) {
			out = append(out, e)
		}
	}
	return out
}

func filterFiles(in []model.ManagedFile, keep func(model.ManagedFile) bool) []model.ManagedFile {
	var out []model.ManagedFile
	for _, e := range in {
		if keep(e) {
			out = append(out, e)
		}
	}
	return out
}

func filterFlags(in []model.ManagedFlag, keep func(model.ManagedFlag) bool) []model.ManagedFlag {
	var out []model.ManagedFlag
	for _, e := range in {
		if keep(e) {
			out = append(out, e)
		}
	}
	return out
}

// installPlan is everything computed (without writing) for an install.
type installPlan struct {
	rep  *Report
	old  *model.InstallManifest
	next *model.InstallManifest
}

func runInstall(opt Options) (*Report, error) {
	opt = withDefaults(opt)
	clients, err := resolveClients(opt.Clients)
	if err != nil {
		return nil, err
	}
	if len(clients) == 0 {
		return nil, fmt.Errorf("%w: no client given", ErrUsage)
	}
	// Phase 1 touches nothing: every refusal (usage, managed layer, parse
	// failure) happens before the first byte is written.
	pl, err := planInstall(opt, clients)
	if err != nil || opt.DryRun {
		if err != nil {
			return nil, err
		}
		return finish(opt, pl.rep, pl.old, pl.next, nil)
	}
	release, err := lockInstall(opt)
	if err != nil {
		return nil, err
	}
	defer release()
	// Phase 2 re-plans under the lock so a concurrent installer cannot be overwritten.
	if pl, err = planInstall(opt, clients); err != nil {
		return nil, err
	}
	return finish(opt, pl.rep, pl.old, pl.next, nil)
}

func planInstall(opt Options, clients []Client) (*installPlan, error) {
	lay := opt.Layout
	goos := envGOOS(lay, opt.Env)
	binary, err := resolveBinary(opt, goos)
	if err != nil {
		return nil, err
	}
	rep := &Report{}
	for _, c := range clients {
		if d := c.Detect(lay, opt.FS, opt.Env); !d.Installed && !opt.Force {
			return nil, fmt.Errorf("%w: %s (use --force to install anyway)", ErrClientNotDetected, c.ID())
		}
		if c.ID() == model.ClientClaude {
			managed, file, derr := DetectManaged(lay, opt.FS)
			if managed {
				return nil, fmt.Errorf("%w: Ballast hooks are already deployed in %s; a user-level install would run every hook twice", model.ErrManagedPresent, file)
			}
			if derr != nil {
				rep.Warnings = append(rep.Warnings, "managed settings could not be fully inspected: "+derr.Error())
			}
		}
	}
	old, err := LoadManifest(opt.FS, lay.Manifest)
	if err != nil {
		return nil, err
	}

	var planned []plannedFile
	for _, c := range clients {
		spec := EntrySpec{
			Client: c.ID(), Binary: binary, Events: defaultEvents(c.ID()), GOOS: goos,
			MDC: opt.MDC, CodexTrust: opt.CodexTrust,
		}
		if c.ID() == model.ClientCodex && lay.CodexDir != "" {
			if real, rerr := evalSymlinksDeepest(opt.FS, lay.CodexDir); rerr == nil {
				spec.CodexHomeReal = real
			}
		}
		var done []*PlanResult
		for _, t := range c.Targets(lay, spec) {
			cur, err := readOrNil(opt.FS, t.Path())
			if err != nil {
				return nil, err
			}
			var p *PlanResult
			if pp, ok := t.(peerPlanner); ok {
				p, err = pp.planWith(cur, spec, old, done)
			} else {
				p, err = t.Plan(cur, spec, old)
			}
			if err != nil {
				return nil, err
			}
			noteKeptDir(opt.FS, old, c.ID(), p, cur)
			done = append(done, p)
			planned = append(planned, plannedFile{c.ID(), p})
			rep.Plans = append(rep.Plans, p)
			rep.Warnings = append(rep.Warnings, p.Warnings...)
		}
		rep.Notes = append(rep.Notes, c.PostInstallNotes()...)
	}
	next := mergeRecords(old, planned)
	next.V = model.ManifestVersion
	next.Binary = binary
	return &installPlan{rep: rep, old: old, next: next}, nil
}

type uninstallPlan struct {
	installPlan
	created []string // files install created and uninstall deletes
}

func runUninstall(opt Options) (*Report, error) {
	opt = withDefaults(opt)
	if _, err := resolveClients(opt.Clients); err != nil {
		return nil, err
	}
	pl, err := planUninstall(opt)
	if err != nil {
		return nil, err
	}
	if opt.DryRun || manifestEmpty(pl.old) {
		return finish(opt, pl.rep, pl.old, pl.next, pl.created)
	}
	release, err := lockInstall(opt)
	if err != nil {
		return nil, err
	}
	defer release()
	if pl, err = planUninstall(opt); err != nil {
		return nil, err
	}
	return finish(opt, pl.rep, pl.old, pl.next, pl.created)
}

func planUninstall(opt Options) (*uninstallPlan, error) {
	lay := opt.Layout
	goos := envGOOS(lay, opt.Env)
	old, err := LoadManifest(opt.FS, lay.Manifest)
	if err != nil {
		return nil, err
	}
	rep := &Report{}
	if manifestEmpty(old) {
		rep.Notes = append(rep.Notes, "nothing to uninstall: no entries recorded in "+lay.Manifest)
		return &uninstallPlan{installPlan: installPlan{rep: rep, old: old, next: old}}, nil
	}
	inScope := map[model.ClientID]bool{}
	if len(opt.Clients) == 0 {
		for _, c := range Clients() {
			inScope[c.ID()] = true
		}
	} else {
		for _, id := range opt.Clients {
			inScope[id] = true
		}
	}

	pl := &uninstallPlan{}
	var planned []plannedFile
	for _, c := range Clients() {
		if !inScope[c.ID()] {
			continue
		}
		for _, path := range manifestPaths(old, c.ID()) {
			t := targetFor(c.ID(), path, goos)
			cur, err := readOrNil(opt.FS, path)
			if err != nil {
				return nil, err
			}
			p, err := t.Remove(cur, old)
			if err != nil {
				return nil, err
			}
			if kept := keptDir(old, path); kept != "" && p.After != nil && len(p.Files) > 0 && !hasFlagKey(p.Flags, flagDirKept) {
				// The file stays recorded (user-modified entries kept): so
				// does what a later uninstall needs to prune correctly. The
				// structural Remove may already have carried the flag over.
				p.Flags = append(p.Flags, model.ManagedFlag{Client: c.ID(), File: path, Key: flagDirKept, Value: kept})
			}
			planned = append(planned, plannedFile{c.ID(), p})
			rep.Plans = append(rep.Plans, p)
			rep.Warnings = append(rep.Warnings, p.Warnings...)
			if prior := fileRec(old, path); p.After == nil && cur != nil && prior != nil && prior.Created {
				pl.created = append(pl.created, path)
			}
		}
		for _, fl := range old.Flags {
			if fl.Client == c.ID() && fl.Key == flagFeaturesHooks && fl.SetByBallast && fl.Value != featuresFalse {
				rep.Notes = append(rep.Notes, fmt.Sprintf("Codex features.hooks was left enabled in %s; disable it manually if no longer wanted", fl.File))
			}
		}
	}
	next := mergeRecords(old, planned)
	if len(next.Entries) == 0 {
		next.Binary = ""
	}
	pl.rep, pl.old, pl.next = rep, old, next
	return pl, nil
}

// manifestPaths lists the distinct files recorded for a client.
func manifestPaths(m *model.InstallManifest, c model.ClientID) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, e := range m.Entries {
		if e.Client == c {
			add(e.File)
		}
	}
	for _, f := range m.Files {
		if f.Client == c {
			add(f.Path)
		}
	}
	for _, f := range m.Flags {
		if f.Client == c {
			add(f.File)
		}
	}
	return out
}

// finish computes the dry-run diff or applies the plans: back up, write the
// files, write the manifest last; a failure rolls the written files back.
func finish(opt Options, rep *Report, old, next *model.InstallManifest, createdDeleted []string) (*Report, error) {
	var changed []*PlanResult
	for _, p := range rep.Plans {
		if p.Changed() {
			changed = append(changed, p)
		}
	}
	if opt.DryRun {
		var b strings.Builder
		for _, p := range changed {
			b.WriteString(UnifiedDiff(p.Path, p.Before, p.After))
		}
		rep.Diff = b.String()
		return rep, nil
	}
	manifestChanged := !sameManifest(old, next)
	if len(changed) == 0 && !manifestChanged {
		return rep, nil
	}

	var backupFiles []string
	for _, p := range changed {
		if p.Before != nil {
			backupFiles = append(backupFiles, p.Path)
		}
	}
	if len(backupFiles) > 0 {
		rec, err := Backup(opt, backupFiles)
		if err != nil {
			return nil, err
		}
		rep.Backup = rec
		if rec != nil {
			next.Backups = append([]model.BackupRecord{*rec}, next.Backups...)
		}
	}
	next.Backups = pruneBackupRecords(opt.FS, next.Backups)

	var written []*PlanResult
	rollback := func() {
		for i := len(written) - 1; i >= 0; i-- {
			_ = restorePlan(opt, written[i])
		}
	}
	for _, p := range changed {
		if err := writePlan(opt, p); err != nil {
			rollback()
			return nil, fmt.Errorf("write %s: %w", p.Path, err)
		}
		written = append(written, p)
	}
	next.Updated = opt.Clock.Now().UTC()
	if err := SaveManifest(opt.FS, opt.IDs, opt.Layout.Manifest, next); err != nil {
		rollback()
		return nil, fmt.Errorf("write manifest: %w", err)
	}
	for _, path := range createdDeleted {
		pruneEmptyDirs(opt, path, keptDir(old, path))
	}
	return rep, nil
}

func writePlan(opt Options, p *PlanResult) error {
	fsys := opt.FS
	if p.After == nil {
		if err := fsys.Remove(p.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	target := p.Path
	if rp, err := fsys.EvalSymlinks(p.Path); err == nil {
		target = rp
	}
	perm := fs.FileMode(0o600)
	if fi, err := fsys.Stat(target); err == nil {
		perm = fi.Mode().Perm()
	}
	return sys.WriteFileAtomic(fsys, opt.IDs, target, p.After, perm, true)
}

// restorePlan puts the original content of a written plan back.
func restorePlan(opt Options, p *PlanResult) error {
	return writePlan(opt, &PlanResult{Path: p.Path, After: p.Before})
}

// pruneEmptyDirs removes the (now empty) directories above a deleted file,
// but only those strictly inside a client's config directory: the directory
// itself is how the client is detected and may have existed before install.
// kept (flagDirKept, contract v1.3) is the nearest directory that existed
// before install created the file; it and its ancestors are never removed.
func pruneEmptyDirs(opt Options, file, kept string) {
	lay := opt.Layout
	var roots []string
	for _, c := range model.AllClients {
		if d := lay.ClientDir(c); d != "" {
			roots = append(roots, filepath.Clean(d))
		}
	}
	kept = filepath.Clean(kept)
	dir := filepath.Dir(file)
	for {
		within := false
		for _, r := range roots {
			if strings.HasPrefix(dir, r+string(filepath.Separator)) {
				within = true
			}
			// A client root that install itself created (--force; the
			// nearest pre-existing directory lies above it) goes too.
			if dir == r && kept != "." && strings.HasPrefix(r, kept+string(filepath.Separator)) {
				within = true
			}
		}
		if !within {
			return
		}
		if kept != "." && len(dir) <= len(kept) {
			return
		}
		if err := opt.FS.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// evalSymlinksDeepest resolves every symbolic link of p. When p does not exist
// yet (a CODEX_HOME that the install is about to create), it resolves the
// deepest existing ancestor and appends the remaining, not yet existing
// elements, so a symlinked HOME still yields the canonical path that Codex
// will compute once the directory exists. Errors other than "does not exist"
// are returned unchanged.
func evalSymlinksDeepest(fsys sys.FS, p string) (string, error) {
	p = filepath.Clean(p)
	real, err := fsys.EvalSymlinks(p)
	if err == nil {
		return real, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(p)
	if parent == p {
		return "", err
	}
	base, perr := evalSymlinksDeepest(fsys, parent)
	if perr != nil {
		return "", perr
	}
	return filepath.Join(base, filepath.Base(p)), nil
}
