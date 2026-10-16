package cli

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/libingtong/ballast/internal/report"
	"github.com/libingtong/ballast/internal/stats"
	"github.com/libingtong/ballast/internal/version"
)

// shippedRuleIDs are the rule ids of the packs that ship with Ballast
// (rules/default.json and rules/optional/*.json). `ballast report` prints
// them verbatim; every other rule id appears hashed ("h:<12 hex>") so custom
// rule names do not leak intent (docs/p2-design.md §4.4). The binary does not
// embed the packs, so the list lives here; TestShippedRuleIDsMatchPacks keeps
// it identical to the files.
var shippedRuleIDs = []string{
	"read-before-edit", "no-long-sleep", "evidence-before-commit", "no-skip-verify", "no-tamper",
	"read-after-test-fail",
	"no-todo-marker", "no-hardcoded-list", "no-patch-intent", "no-git-force-skip", "no-hack-commit-message",
	"keep-pushing",
	"no-foreground-long-task", "no-foreground-install", "no-foreground-pip-module", "no-foreground-docker",
	"no-shell-wait", "no-lang-sleep",
	"claim-needs-evidence",
}

// reportDefaultSince is the default window of `ballast report`.
const reportDefaultSince = "30d"

// reportWriteExit maps a report.Write error to the exit codes of
// `ballast report`: 1 privacy self-check failed (nothing written), 4 the file
// exists and --force was not given, 5 any other I/O error.
func (a *app) reportWriteExit(err error) int {
	switch {
	case errors.Is(err, report.ErrPrivacy):
		return a.errorf(ExitFindings, "report: the privacy self-check failed, nothing was written (this is a bug, please report it): %v", err)
	case errors.Is(err, report.ErrExists):
		return a.errorf(ExitRefused, "report: %v (use --force to overwrite)", err)
	}
	return a.errorf(ExitInfra, "report: %v", err)
}

// runReport implements `ballast report --out <file> [--since 30d] [--force]`
// (docs/p2-design.md §4.4): a LOCAL file of redacted, aggregated statistics
// the user may choose to share. Nothing is sent anywhere. Exit codes: 0
// written; 1 the privacy self-check failed (nothing written; a bug); 2 usage;
// 4 the file exists and --force was not given; 5 I/O.
func runReport(a *app, c *command, args []string) int {
	fs := newFlagSet("report")
	var out, since string
	var force bool
	fs.StringVar(&out, "out", "", "")
	fs.StringVar(&since, "since", reportDefaultSince, "")
	fs.BoolVar(&force, "force", false, "")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return a.usageErr(c, "%v", err)
	}
	if len(pos) > 0 {
		return a.usageErr(c, "unexpected argument %q", pos[0])
	}
	if out == "" {
		return a.usageErr(c, "--out <file> is required")
	}
	now := a.clock.Now()
	from, err := stats.ParseSince(since, now)
	if err != nil {
		return a.usageErr(c, "%v", err)
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(a.cwd, out)
	}
	rep, err := report.Build(report.Options{
		Audit:          a.auditReader(),
		Clock:          a.clock,
		Since:          from,
		ShippedRuleIDs: shippedRuleIDs,
		BallastVersion: version.Version,
		GOOS:           a.env.GOOS(),
	})
	if err != nil {
		return a.errorf(ExitInfra, "report: %v", err)
	}
	if err := report.Write(a.fs, out, rep, force); err != nil {
		return a.reportWriteExit(err)
	}
	fmt.Fprintf(a.out, "已写入 %s(仅本地文件,未发送任何数据;分享前请自行检查)\n", out)
	return ExitOK
}
