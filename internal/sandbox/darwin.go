package sandbox

import (
	"path/filepath"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// sbplString renders s as an SBPL string literal.
func sbplString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// reQuote escapes the regular-expression metacharacters of s.
func reQuote(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`.^$*+?()[]{}|\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// reGlob converts a glob (only '*' and '?', neither crossing '/') to a regex
// fragment.
func reGlob(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '*':
			b.WriteString(`[^/]*`)
		case '?':
			b.WriteString(`[^/]`)
		default:
			b.WriteString(reQuote(string(r)))
		}
	}
	return b.String()
}

// rePattern renders pattern p (see MatchMandatory) below root as an anchored
// regex. Repo patterns match at any depth below root.
func rePattern(root string, p pattern, isRepo bool) string {
	var b strings.Builder
	b.WriteString("^")
	b.WriteString(reQuote(root))
	b.WriteString("/")
	if isRepo {
		b.WriteString("(.+/)?")
	}
	for i, e := range p.elems {
		if i > 0 {
			b.WriteString("/")
		}
		switch e.kind {
		case elemLit:
			b.WriteString(reQuote(e.lit))
		case elemStar:
			b.WriteString("[^/]+")
		default:
			b.WriteString("[^/]+(/[^/]+)*")
		}
	}
	if p.subtree {
		b.WriteString("(/.*)?")
	}
	b.WriteString("$")
	return b.String()
}

// darwinDevices are the only device nodes the child may write: no /dev/tty*,
// no /dev/dtracehelper (package doc "Terminal").
var darwinDevices = []string{
	`(literal "/dev/null")`,
	`(literal "/dev/zero")`,
	`(regex "^/dev/fd/[0-9]+$")`,
}

// darwinProcExec is the process-exec denial of the standard paths of the
// three launcher binaries (the IPC denials must hold without it, see the
// integration tests).
const darwinProcExec = `(deny process-exec (literal "/usr/bin/open") (literal "/usr/bin/osascript")
                   (literal "/bin/launchctl"))`

// DarwinEscapeDenials is the SBPL IPC fragment that DarwinProfile appends
// last (v1.5: IPC DEFAULT-DENY; v1.4's "deny a few named services under
// (allow default)" is replaced). Frozen content, in this order:
//
//	(deny mach-lookup)
//	(allow mach-lookup (global-name "<each DarwinMachAllow entry>"))
//	(deny mach-register)
//	(deny job-creation)
//	(deny user-preference-write)
//	(deny lsopen)
//	(deny appleevent-send)
//	(deny signal) (allow signal (target same-sandbox))
//	(deny process-exec (literal "/usr/bin/open") (literal "/usr/bin/osascript")
//	                   (literal "/bin/launchctl"))
//
// (deny user-preference-write) is explicit because cfprefsd writes
// preferences on the client's behalf outside the sandbox (a `defaults write`
// into a terminal's start-up command would run outside every sandbox the next
// time the human opens it); (deny job-creation) is explicit because launchd
// job submission travels over the bootstrap port, not a named mach-lookup.
// File reads, sysctl, IOKit and POSIX/SysV IPC stay under (allow default) —
// registered residual surface, docs/p2-design.md §11. Whether every
// operation name above is accepted by sandbox-exec on the supported macOS
// releases, and whether cfprefsd honours user-preference-write for an
// allowed lookup, is 【未核验】: the golden pins the text, the integration
// cases (package doc) prove the effect; if cfprefsd does not honour it, the
// two cfprefsd names leave DarwinMachAllow (preference reads then return
// defaults) — an internal change of this fragment only.
//
// The process-exec rules only match the standard paths, so they alone
// prove nothing (v1.4): the integration tests must ALSO run each channel
// in a way the process-exec rules cannot match — copies of open,
// osascript and launchctl placed in Spec.TmpDir, and a python3 ctypes
// call into LaunchServices / AppleEvents (LSOpenCFURLRef, AESendMessage)
// and launchd job submission — and must pass with a test-only profile
// variant that has the process-exec denials removed, so that the IPC
// denials are shown to work on their own.
func DarwinEscapeDenials() string {
	return darwinEscapeDenials(true)
}

func darwinEscapeDenials(procExec bool) string {
	var b strings.Builder
	b.WriteString("(deny mach-lookup)\n")
	for _, n := range DarwinMachAllow {
		b.WriteString("(allow mach-lookup (global-name " + sbplString(n) + "))\n")
	}
	b.WriteString("(deny mach-register)\n")
	b.WriteString("(deny job-creation)\n")
	b.WriteString("(deny user-preference-write)\n")
	b.WriteString("(deny lsopen)\n")
	b.WriteString("(deny appleevent-send)\n")
	b.WriteString("(deny signal)\n")
	b.WriteString("(allow signal (target same-sandbox))\n")
	if procExec {
		b.WriteString(darwinProcExec + "\n")
	}
	return b.String()
}

// DarwinProfile renders the SBPL profile for spec (pure; golden-tested).
func DarwinProfile(spec Spec) string {
	return darwinProfile(spec, true)
}

// darwinProfile is DarwinProfile with the process-exec denials optional (the
// integration tests prove the IPC denials alone with procExec == false).
func darwinProfile(spec Spec, procExec bool) string {
	clean := func(ps []string) []string {
		out := make([]string, 0, len(ps))
		for _, p := range ps {
			if p != "" {
				out = append(out, filepath.Clean(p))
			}
		}
		return dedupe(out)
	}
	tree := ""
	if spec.Tree != "" {
		tree = filepath.Clean(spec.Tree)
	}
	// W: AllowWrite then TmpDir.
	w := clean(append(append([]string(nil), spec.AllowWrite...), spec.TmpDir))
	var wNoTree []string
	for _, r := range w {
		if r != tree {
			wNoTree = append(wNoTree, r)
		}
	}
	deny := clean(spec.DenyWrite)
	globs := clean(spec.DenyWriteGlobs)

	// D split around the Tree carve-out.
	var dBefore, dAfter []string
	for _, d := range deny {
		if tree != "" && within(d, tree) {
			dAfter = append(dAfter, d)
			continue
		}
		dBefore = append(dBefore, d)
		if withinAny(d, w) {
			dAfter = append(dAfter, d)
		}
	}
	var gBefore, gAfter []string
	for _, g := range globs {
		if tree != "" && within(g, tree) {
			gAfter = append(gAfter, g)
			continue
		}
		gBefore = append(gBefore, g)
		if withinAny(g, w) {
			gAfter = append(gAfter, g)
		}
	}

	var b strings.Builder
	line := func(s string) { b.WriteString(s + "\n") }
	line("(version 1)")
	line("(allow default)")
	line("(deny file-write*)")

	// (allow file-write* W devices)
	b.WriteString("(allow file-write*")
	for _, r := range wNoTree {
		b.WriteString("\n  (subpath " + sbplString(r) + ")")
	}
	for _, d := range darwinDevices {
		b.WriteString("\n  " + d)
	}
	line(")")

	writeDeny := func(subs, globRes []string, mand bool) {
		var parts []string
		for _, d := range subs {
			parts = append(parts, "(subpath "+sbplString(d)+")")
		}
		for _, g := range globRes {
			parts = append(parts, "(regex "+sbplString("^"+reGlob(g)+"(/.*)?$")+")")
		}
		if mand {
			repoPats := parsePatterns(RepoMandatoryDeny)
			gitPats := parsePatterns(GitDirMandatoryDeny)
			for _, r := range clean(spec.RepoRoots) {
				for _, p := range repoPats {
					parts = append(parts, "(regex "+sbplString(rePattern(r, p, true))+")")
				}
			}
			for _, g := range clean(spec.GitDirs) {
				for _, p := range gitPats {
					parts = append(parts, "(regex "+sbplString(rePattern(g, p, false))+")")
				}
			}
		}
		if len(parts) == 0 {
			return
		}
		b.WriteString("(deny file-write*")
		for _, p := range parts {
			b.WriteString("\n  " + p)
		}
		line(")")
	}
	writeDeny(dBefore, gBefore, false)
	if tree != "" {
		line("(allow file-write* (subpath " + sbplString(tree) + "))")
	}
	writeDeny(dAfter, gAfter, true)

	// Ancestor pins.
	if pins := AncestorPins(spec); len(pins) > 0 {
		b.WriteString("(deny file-write*")
		for _, a := range pins {
			b.WriteString("\n  (literal " + sbplString(a) + ")")
		}
		line(")")
	}

	// Network.
	line("(deny network-outbound)")
	switch spec.Network {
	case model.NetworkLoopback:
		line(`(allow network-outbound (remote ip "localhost:*"))`)
	case model.NetworkAllow:
		line(`(allow network-outbound (remote ip "*:*"))`)
		line(`(allow network-outbound (remote unix-socket (path-literal "/private/var/run/mDNSResponder")))`)
	}
	for _, r := range w {
		line("(allow network-outbound (remote unix-socket (subpath " + sbplString(r) + ")))")
	}
	line("(deny network-bind)")
	line("(deny network-inbound)")
	for _, r := range w {
		line("(allow network-bind (local unix-socket (subpath " + sbplString(r) + ")))")
	}
	if spec.Network == model.NetworkLoopback || spec.Network == model.NetworkAllow {
		line(`(allow network-bind (local ip "localhost:*"))`)
		line(`(allow network-inbound (local ip "localhost:*"))`)
	}

	b.WriteString(darwinEscapeDenials(procExec))
	return b.String()
}
