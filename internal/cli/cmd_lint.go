package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/libingtong/ballast/internal/config"
	"github.com/libingtong/ballast/internal/dsl"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/project"
)

func runLint(a *app, c *command, args []string) int {
	fs := newFlagSet("lint")
	var layer, client string
	var stack, examples, asJSON bool
	fs.StringVar(&layer, "layer", "", "")
	fs.StringVar(&client, "client", "", "")
	fs.BoolVar(&stack, "stack", false, "")
	fs.BoolVar(&examples, "examples", false, "")
	fs.BoolVar(&asJSON, "json", false, "")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return a.usageErr(c, "%v", err)
	}
	switch model.Layer(layer) {
	case "", model.LayerOrg, model.LayerRepo, model.LayerUser:
	default:
		return a.usageErr(c, "--layer must be org, repo or user, got %q", layer)
	}
	clients, err := a.lintClients(client)
	if err != nil {
		if errors.Is(err, errUsage) {
			return a.usageErr(c, "%v", err)
		}
		return a.errorf(ExitInfra, "%v", err)
	}

	var inputs []dsl.LintInput
	if len(pos) == 0 {
		inputs, err = a.stackInputs()
		stack = true
	} else {
		inputs, err = a.ruleInputs(pos, model.Layer(layer))
	}
	if err != nil {
		return a.errorf(ExitInfra, "%v", err)
	}
	a.inferLayers(inputs)

	opt := dsl.LintOptions{
		Clients:     clients,
		Layer:       model.Layer(layer),
		Stack:       stack,
		RunExamples: examples,
		Project:     a.projectOrNil(),
	}
	ds := a.engine.Lint(inputs, opt)
	errs, warns := countSeverities(ds)
	if asJSON {
		if ds == nil {
			ds = []model.Diagnostic{}
		}
		if err := a.writeJSON(map[string]any{"diagnostics": ds, "errors": errs, "warnings": warns, "files": len(inputs)}); err != nil {
			return a.errorf(ExitInfra, "%v", err)
		}
	} else {
		for _, d := range ds {
			fmt.Fprintln(a.out, formatDiagnostic(d))
		}
		fmt.Fprintf(a.out, "lint: %d 个文件,%d 个错误,%d 个警告\n", len(inputs), errs, warns)
	}
	if errs > 0 {
		return ExitFindings
	}
	return ExitOK
}

// errUsage marks argument problems found outside flag parsing.
var errUsage = errors.New("usage")

// lintClients resolves --client: all, one or several ids, or (default) the
// clients in the install manifest plus claude-code.
func (a *app) lintClients(flagVal string) ([]model.ClientID, error) {
	switch flagVal {
	case "":
		return a.manifestClients()
	case "all":
		return append([]model.ClientID(nil), model.AllClients...), nil
	}
	var out []model.ClientID
	for _, p := range strings.Split(flagVal, ",") {
		cl, err := parseClient(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errUsage, err)
		}
		out = append(out, cl)
	}
	return out, nil
}

// inferLayers marks inputs that are exactly one of the stack's layer files
// with that layer (so a stack lint of explicit paths keeps org/repo/user
// precedence).
func (a *app) inferLayers(inputs []dsl.LintInput) {
	req, err := a.loadRequest(model.ClientClaude)
	if err != nil {
		return
	}
	byPath := map[string]model.Layer{}
	for _, lp := range config.LayerPaths(a.lay, req) {
		byPath[abs(lp.Path)] = lp.Layer
	}
	for i := range inputs {
		if inputs[i].Layer != "" && inputs[i].Layer != model.LayerFile {
			continue
		}
		if l, ok := byPath[abs(inputs[i].Path)]; ok {
			inputs[i].Layer = l
		}
	}
}

func abs(p string) string {
	if ap, err := filepath.Abs(p); err == nil {
		return ap
	}
	return filepath.Clean(p)
}

// projectOrNil loads the working directory's project.json for the
// verify-name-unknown lint check; any problem means "no project".
func (a *app) projectOrNil() *model.ProjectConfig {
	root, err := a.projectRoot()
	if err != nil {
		return nil
	}
	cfg, err := (&project.Loader{FS: a.fs}).LoadProject(root)
	if err != nil {
		return nil
	}
	return cfg
}

func countSeverities(ds []model.Diagnostic) (errs, warns int) {
	for _, d := range ds {
		switch d.Severity {
		case model.SevError:
			errs++
		case model.SevWarning:
			warns++
		}
	}
	return errs, warns
}

func formatDiagnostic(d model.Diagnostic) string {
	var b strings.Builder
	if d.File != "" {
		b.WriteString(d.File)
		b.WriteString(": ")
	}
	b.WriteString(string(d.Severity))
	b.WriteString(" [")
	b.WriteString(d.Code)
	b.WriteString("]")
	if d.RuleID != "" {
		b.WriteString(" rule=")
		b.WriteString(d.RuleID)
	}
	if d.Client != "" {
		b.WriteString(" client=")
		b.WriteString(string(d.Client))
	}
	b.WriteString(": ")
	b.WriteString(d.Message)
	if d.Pointer != "" {
		b.WriteString(" (")
		b.WriteString(d.Pointer)
		b.WriteString(")")
	}
	return b.String()
}

func runTestRules(a *app, c *command, args []string) int {
	fs := newFlagSet("test-rules")
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return a.usageErr(c, "%v", err)
	}
	var inputs []dsl.LintInput
	if len(pos) == 0 {
		inputs, err = a.stackInputs()
	} else {
		inputs, err = a.ruleInputs(pos, model.LayerFile)
	}
	if err != nil {
		return a.errorf(ExitUsage, "%v", err)
	}
	for _, in := range inputs {
		if _, _, perr := a.engine.ParseFile(model.LayerFile, in.Path, in.Data); perr != nil {
			return a.errorf(ExitUsage, "%s: cannot decode: %v", in.Path, perr)
		}
	}
	rep, err := a.engine.TestRules(inputs)
	if err != nil {
		return a.errorf(ExitUsage, "%v", err)
	}
	if asJSON {
		if err := a.writeJSON(rep); err != nil {
			return a.errorf(ExitInfra, "%v", err)
		}
	} else {
		for _, tc := range rep.Cases {
			switch {
			case tc.Skipped:
				fmt.Fprintf(a.out, "SKIP %s %s/%s (%s)\n", tc.File, tc.RuleID, tc.Example, tc.Client)
			case !tc.Pass:
				fmt.Fprintf(a.out, "FAIL %s %s/%s (%s): expected %s, got %s", tc.File, tc.RuleID, tc.Example, tc.Client, tc.Expect, tc.Got)
				if tc.Detail != "" {
					fmt.Fprintf(a.out, " [%s]", tc.Detail)
				}
				fmt.Fprintln(a.out)
			}
		}
		fmt.Fprintf(a.out, "test-rules: %d 通过,%d 失败,%d 跳过(%d 个文件)\n", rep.Passed, rep.Failed, rep.Skipped, len(inputs))
	}
	if rep.Failed > 0 {
		return ExitFindings
	}
	return ExitOK
}
