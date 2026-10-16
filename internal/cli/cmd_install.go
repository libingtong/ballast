package cli

import (
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/libingtong/ballast/internal/dsl"
	"github.com/libingtong/ballast/internal/inject"
	"github.com/libingtong/ballast/internal/install"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sandbox"
)

// parseClients parses repeated/comma-separated --client values; "all"
// expands to every client.
func parseClients(vals []string) ([]model.ClientID, error) {
	var out []model.ClientID
	seen := map[model.ClientID]bool{}
	for _, v := range vals {
		if v == "all" {
			for _, c := range model.AllClients {
				if !seen[c] {
					seen[c] = true
					out = append(out, c)
				}
			}
			continue
		}
		c, err := parseClient(v)
		if err != nil {
			return nil, err
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out, nil
}

// cursorMDC renders the managed Cursor rules file from the rules currently
// in effect for the working directory. empty reports that there are none:
// the file would hold nothing but its front matter.
func (a *app) cursorMDC() (mdc []byte, empty bool, err error) {
	req, err := a.loadRequest(model.ClientCursor)
	if err != nil {
		return nil, false, err
	}
	rs, err := a.loader(true, a.errw).Load(req)
	if err != nil {
		return nil, false, err
	}
	return inject.RenderMDC(rs), !inject.MDCHasRules(rs), nil
}

// cursorMDCEmptyWarning: ballast.mdc is rendered when install runs, so rules
// added later reach Cursor through the hook channel only until install is
// run again.
const cursorMDCEmptyWarning = "Cursor 的 ballast.mdc 不含任何规则(当前生效的规则为空)。它在安装时按当时的规则生成、不会自动更新:" +
	"放入规则(~/.ballast/rules.json 或仓库的 .ballast/rules.json)后,请重新运行 `ballast install --client cursor` 刷新它"

// installExit maps install/uninstall errors to the architecture §7 codes.
func (a *app) installExit(err error) int {
	switch {
	case errors.Is(err, model.ErrManagedPresent):
		return a.errorf(ExitFindings, "%v", err)
	case errors.Is(err, model.ErrSettingsParse):
		return a.errorf(ExitParse, "%v", err)
	case errors.Is(err, install.ErrUsage), errors.Is(err, install.ErrClientNotDetected),
		errors.Is(err, install.ErrBinaryUnstable), errors.Is(err, model.ErrUnknownClient):
		return a.errorf(ExitUsage, "%v", err)
	}
	return a.errorf(ExitInfra, "%v", err)
}

func (a *app) printInstallReport(rep *install.Report, verb string) {
	if rep.Diff != "" {
		fmt.Fprint(a.out, rep.Diff)
		if !strings.HasSuffix(rep.Diff, "\n") {
			fmt.Fprintln(a.out)
		}
	}
	changed := 0
	for _, p := range rep.Plans {
		if p.Changed() {
			changed++
			if rep.Diff == "" {
				fmt.Fprintf(a.out, "%s %s\n", verb, p.Path)
			}
		}
	}
	if changed == 0 {
		fmt.Fprintln(a.out, "无变化")
	}
	if rep.Backup != nil {
		fmt.Fprintf(a.out, "备份: %s\n", rep.Backup.Dir)
	}
	for _, w := range rep.Warnings {
		fmt.Fprintf(a.errw, "ballast: warning: %s\n", w)
	}
	for _, n := range rep.Notes {
		fmt.Fprintf(a.out, "提示: %s\n", n)
	}
}

func runInstall(a *app, c *command, args []string) int {
	fs := newFlagSet("install")
	var clientVals multiFlag
	var binary string
	var dry, codexTrust, force bool
	fs.Var(&clientVals, "client", "")
	fs.StringVar(&binary, "binary", "", "")
	fs.BoolVar(&dry, "dry-run", false, "")
	fs.BoolVar(&codexTrust, "codex-trust", false, "")
	fs.BoolVar(&force, "force", false, "")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return a.usageErr(c, "%v", err)
	}
	if len(pos) > 0 {
		return a.usageErr(c, "unexpected argument %q", pos[0])
	}
	if len(clientVals) == 0 {
		return a.usageErr(c, "--client is required (%s, or all)", clientList())
	}
	clients, err := parseClients(clientVals)
	if err != nil {
		return a.usageErr(c, "%v", err)
	}
	opt := install.Options{
		Layout: a.lay, FS: a.fs, Env: a.env, Clock: a.clock, IDs: a.ids,
		Clients: clients, Binary: binary, DryRun: dry, CodexTrust: codexTrust, Force: force,
	}
	noRules := false
	for _, cl := range clients {
		if cl == model.ClientCursor {
			mdc, empty, err := a.cursorMDC()
			if err != nil {
				return a.errorf(ExitInfra, "cannot render the Cursor rules file: %v", err)
			}
			opt.MDC, noRules = mdc, empty
		}
	}
	rep, err := install.Install(opt)
	if err != nil {
		return a.installExit(err)
	}
	if noRules {
		rep.Warnings = append(rep.Warnings, cursorMDCEmptyWarning)
	}
	a.printInstallReport(rep, "已写入")
	return ExitOK
}

func runUninstall(a *app, c *command, args []string) int {
	fs := newFlagSet("uninstall")
	var clientVals multiFlag
	var all, dry bool
	fs.Var(&clientVals, "client", "")
	fs.BoolVar(&all, "all", false, "")
	fs.BoolVar(&dry, "dry-run", false, "")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return a.usageErr(c, "%v", err)
	}
	if len(pos) > 0 {
		return a.usageErr(c, "unexpected argument %q", pos[0])
	}
	if all && len(clientVals) > 0 {
		return a.usageErr(c, "--all and --client are mutually exclusive")
	}
	var clients []model.ClientID
	if !all && len(clientVals) > 0 {
		clients, err = parseClients(clientVals)
		if err != nil {
			return a.usageErr(c, "%v", err)
		}
	}
	rep, err := install.Uninstall(install.Options{
		Layout: a.lay, FS: a.fs, Env: a.env, Clock: a.clock, IDs: a.ids, Clients: clients, DryRun: dry,
	})
	if err != nil {
		return a.installExit(err)
	}
	a.printInstallReport(rep, "已更新")
	if len(rep.Warnings) > 0 {
		return ExitFindings
	}
	return ExitOK
}

// clientShort maps doctor check id prefixes to clients.
func clientOfCheck(ck install.Check) model.ClientID {
	if ck.Client != "" {
		return ck.Client
	}
	switch {
	case strings.HasPrefix(ck.ID, "claude."):
		return model.ClientClaude
	case strings.HasPrefix(ck.ID, "codex."):
		return model.ClientCodex
	case strings.HasPrefix(ck.ID, "cursor."):
		return model.ClientCursor
	case strings.HasPrefix(ck.ID, "hermes."):
		return model.ClientHermes
	}
	return ""
}

func runDoctor(a *app, c *command, args []string) int {
	fs := newFlagSet("doctor")
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return a.usageErr(c, "%v", err)
	}
	if len(pos) > 0 {
		return a.usageErr(c, "unexpected argument %q", pos[0])
	}

	manifest, merr := install.LoadManifest(a.fs, a.lay.Manifest)
	if manifest == nil {
		manifest = &model.InstallManifest{V: model.ManifestVersion}
	}
	lintClients := []model.ClientID{model.ClientClaude}
	if merr == nil {
		if cl, err := a.manifestClients(); err == nil {
			lintClients = cl
		}
	}
	self, serr := resolvedSelf()
	if serr != nil {
		self = selfPath()
	}
	dopt := install.DoctorOptions{
		Layout: a.lay, FS: a.fs, Env: a.env,
		Self: self, Cwd: a.cwd,
		SandboxAvailable: a.sandboxAvailable,
		LintRules: func() []model.Diagnostic {
			inputs, err := a.stackInputs()
			if err != nil {
				return []model.Diagnostic{{Severity: model.SevError, Code: "read-failed", Message: err.Error()}}
			}
			return a.engine.Lint(inputs, dsl.LintOptions{Clients: lintClients, Stack: true})
		},
	}
	for _, e := range manifest.Entries {
		if e.Client == model.ClientCursor {
			if mdc, _, err := a.cursorMDC(); err == nil {
				dopt.MDC = mdc
			}
			break
		}
	}
	checks := install.Doctor(dopt)

	tier, tierMsg := "T0", ""
	failed := false
	worst := map[model.ClientID]install.CheckStatus{}
	for _, ck := range checks {
		if ck.Status == install.CheckFail {
			failed = true
		}
		if ck.ID == "tier.level" {
			tierMsg = ck.Message
			if strings.HasPrefix(ck.Message, "T1") {
				tier = "T1"
			}
		}
		if cl := clientOfCheck(ck); cl != "" {
			worst[cl] = worseStatus(worst[cl], ck.Status)
		}
	}

	type clientRow struct {
		ID        model.ClientID `json:"id"`
		Detected  bool           `json:"detected"`
		Installed bool           `json:"installed"`
		Status    string         `json:"status"`
		Note      string         `json:"note,omitempty"`
	}
	var rows []clientRow
	recorded := map[model.ClientID]int{}
	for _, e := range manifest.Entries {
		recorded[e.Client]++
	}
	for _, f := range manifest.Files {
		recorded[f.Client]++
	}
	for _, cl := range install.Clients() {
		det := cl.Detect(a.lay, a.fs, a.env)
		row := clientRow{ID: cl.ID(), Detected: det.Installed, Installed: recorded[cl.ID()] > 0, Status: "not-installed"}
		if row.Installed {
			row.Status = string(worst[cl.ID()])
			if row.Status == "" {
				row.Status = string(install.CheckOK)
			}
		}
		if det.Managed {
			row.Note = "托管层已部署"
		}
		rows = append(rows, row)
	}

	const arbiter = "CI 是最终裁判:会话内规则只负责引导 agent,不保证交付质量;合并前以服务端 CI 为准。"
	if asJSON {
		out := map[string]any{
			"tier":             tier,
			"tier_detail":      tierMsg,
			"clients":          rows,
			"checks":           checks,
			"ok":               !failed,
			"ci_final_arbiter": true,
			"note":             arbiter,
		}
		if err := a.writeJSON(out); err != nil {
			return a.errorf(ExitInfra, "%v", err)
		}
	} else {
		fmt.Fprintf(a.out, "层级: %s\n  %s\n\n", tier, tierMsg)
		fmt.Fprintln(a.out, "客户端:")
		tw := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
		for _, r := range rows {
			det := "未检测到"
			if r.Detected {
				det = "已检测到"
			}
			inst := "未安装 Ballast"
			if r.Installed {
				inst = "已安装 Ballast"
			}
			note := ""
			if r.Note != "" {
				note = "  " + r.Note
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t[%s]%s\n", r.ID, det, inst, r.Status, note)
		}
		_ = tw.Flush()
		fmt.Fprintln(a.out, "\n检查:")
		for _, ck := range checks {
			fmt.Fprintf(a.out, "  [%s] %s: %s\n", ck.Status, ck.ID, ck.Message)
			if ck.Fix != "" && ck.Status != install.CheckOK {
				fmt.Fprintf(a.out, "        修复: %s\n", ck.Fix)
			}
		}
		fmt.Fprintf(a.out, "\n%s\n", arbiter)
	}
	if failed {
		return ExitFindings
	}
	return ExitOK
}

// sandboxAvailable is install.DoctorOptions.SandboxAvailable: nil ⇒ the
// runner's OS sandbox backend (sandbox-exec / bwrap) works on this host.
// install never imports internal/sandbox; the CLI injects this probe.
func (a *app) sandboxAvailable() error {
	kind, err := sandbox.Probe(sandbox.Options{FS: a.fs, Env: a.env, GOOS: a.env.GOOS()})
	if err != nil {
		return err
	}
	if kind == sandbox.KindNone {
		return errors.New("no sandbox backend available")
	}
	return nil
}

func worseStatus(a, b install.CheckStatus) install.CheckStatus {
	rank := func(s install.CheckStatus) int {
		switch s {
		case install.CheckFail:
			return 3
		case install.CheckWarn:
			return 2
		case install.CheckOK:
			return 1
		}
		return 0
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// The two documents `ballast org render` can print (--part).
const (
	orgPartManagedSettings = "managed-settings"
	orgPartOrgJSON         = "org-json"
)

func runOrg(a *app, c *command, args []string) int {
	if len(args) == 0 || args[0] != "render" {
		return a.usageErr(c, "expected the subcommand \"render\"")
	}
	fs := newFlagSet("org render")
	var client, binary, part string
	var write, admin bool
	var homeRoots, protect multiFlag
	fs.StringVar(&client, "client", string(model.ClientClaude), "")
	fs.StringVar(&part, "part", orgPartManagedSettings, "")
	fs.StringVar(&binary, "binary", "", "")
	fs.BoolVar(&write, "write", false, "")
	fs.BoolVar(&admin, "i-am-admin", false, "")
	fs.Var(&homeRoots, "home-root", "")
	fs.Var(&protect, "protect", "")
	pos, err := parseArgs(fs, args[1:])
	if err != nil {
		return a.usageErr(c, "%v", err)
	}
	if len(pos) > 0 {
		return a.usageErr(c, "unexpected argument %q", pos[0])
	}
	if client != string(model.ClientClaude) {
		return a.usageErr(c, "only --client claude-code has a managed template, got %q", client)
	}
	if part != orgPartManagedSettings && part != orgPartOrgJSON {
		return a.usageErr(c, "--part must be %q or %q, got %q", orgPartManagedSettings, orgPartOrgJSON, part)
	}
	r, err := install.OrgRenderFull(a.lay, a.fs, install.OrgRenderOptions{
		GOOS: a.env.GOOS(), Binary: binary, Write: write, IAmAdmin: admin,
		HomeRoot: homeRoots, Protected: protect, Env: a.env,
	})
	if err != nil {
		switch {
		case errors.Is(err, install.ErrOrgWriteDenied):
			return a.errorf(ExitRefused, "%v", err)
		case errors.Is(err, install.ErrUsage):
			return a.errorf(ExitUsage, "%v", err)
		}
		return a.errorf(ExitInfra, "%v", err)
	}
	// stdout is exactly one pure JSON document, so an administrator can
	// redirect it to a file: the managed-settings fragment by default,
	// org.json for --part org-json. --write puts both files down whatever
	// the part is. The org.json risk notes go to stderr.
	out := r.Settings
	if part == orgPartOrgJSON {
		if r.OrgJSON == nil {
			return a.usageErr(c, "--part org-json: this platform is T0 only and has no org.json")
		}
		out = r.OrgJSON
	}
	_, _ = a.out.Write(out)
	for _, n := range r.OrgNotes {
		fmt.Fprintf(a.errw, "ballast: org.json 注意:%s\n", n)
	}
	for _, w := range r.Written {
		fmt.Fprintf(a.errw, "ballast: 已写入 %s\n", w)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(a.errw, "ballast: %s\n", n)
	}
	return ExitOK
}
