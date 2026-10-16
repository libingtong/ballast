package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/tier"
)

// geteuid is os.Geteuid; tests replace it.
var geteuid = os.Geteuid

type orgHook struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type orgGroup struct {
	Matcher string    `json:"matcher,omitempty"`
	Hooks   []orgHook `json:"hooks"`
}

type orgFS struct {
	DenyWrite []string `json:"denyWrite"`
}

type orgSandbox struct {
	Enabled                  bool     `json:"enabled"`
	AllowUnsandboxedCommands bool     `json:"allowUnsandboxedCommands"`
	FailIfUnavailable        bool     `json:"failIfUnavailable"`
	ExcludedCommands         []string `json:"excludedCommands"`
	Filesystem               orgFS    `json:"filesystem"`
}

type orgPermissions struct {
	Deny []string `json:"deny"`
}

type orgMCPServer struct {
	ServerName string `json:"serverName"`
}

// orgSettings is the T1 managed-settings fragment; field order is the key
// order of the rendered JSON.
type orgSettings struct {
	AllowManagedHooksOnly           bool                  `json:"allowManagedHooksOnly"`
	Hooks                           map[string][]orgGroup `json:"hooks"`
	Sandbox                         orgSandbox            `json:"sandbox"`
	Permissions                     orgPermissions        `json:"permissions"`
	AllowManagedPermissionRulesOnly bool                  `json:"allowManagedPermissionRulesOnly"`
	AllowManagedMCPServersOnly      bool                  `json:"allowManagedMcpServersOnly,omitempty"`
	AllowedMCPServers               []orgMCPServer        `json:"allowedMcpServers,omitempty"`
}

// dirOf is filepath.Dir for either separator style.
func dirOf(p, goos string) string {
	seps := "/"
	if goos == "windows" {
		seps = `/\`
	}
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == seps[0] || (len(seps) > 1 && p[i] == seps[1]) {
			if i == 0 {
				return p[:1]
			}
			return p[:i]
		}
	}
	return "."
}

// orgFile is the org.json written next to the managed settings
// (tier.OrgConfig wire format). The required keys have no omitempty so that
// "runner_allow_write": [] is spelled out for the administrator.
type orgFile struct {
	V                int                         `json:"v"`
	Tier             model.Tier                  `json:"tier"`
	RunnerSandbox    model.SandboxPolicy         `json:"runner_sandbox"`
	RunnerNetwork    model.NetworkPolicy         `json:"runner_network"`
	RunnerAllowWrite []string                    `json:"runner_allow_write"`
	HomeRoot         []string                    `json:"home_root,omitempty"`
	WeakenedProject  model.WeakenedProjectPolicy `json:"weakened_project"`
	ProtectedExtra   []string                    `json:"protected_extra,omitempty"`
}

func marshalIndent(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// homeRootFixes is the repair list of docs/p2-design.md §3.2 (enterprise
// fleets), in the frozen order. The org render --write preflight and the
// doctor check tier.locked_home print it.
const homeRootFixes = "修复方式(按推荐顺序):" +
	"(1) 在 org.json 写 home_root(org render --home-root /home,推荐);" +
	"(2) 使用启用 cgo 的构建(走 NSS,仅在 NSS 由本地缓存提供时可用:sssd 本地缓存或 nscd);" +
	"(3) 把该用户加入本地 /etc/passwd"

// memOrgFS serves one document to tier.LoadOrgConfig, which only reads.
type memOrgFS struct {
	sys.FS
	data []byte
}

func (m memOrgFS) ReadFile(string) ([]byte, error) { return m.data, nil }

// validateOrgJSON runs the rendered org.json through the decoder and
// validation Detect and Locked apply (tier.LoadOrgConfig), so that --write
// never deploys a document the hook would refuse (which would leave the
// host unlocked while the administrator believes it is T1). The path
// argument only decides the protected org directory used by the
// runner_allow_write rules.
func validateOrgJSON(b []byte, orgPath string) error {
	if _, err := tier.LoadOrgConfig(memOrgFS{data: b}, orgPath); err != nil {
		return fmt.Errorf("%w: 渲染出的 org.json 不能通过 tier 校验,拒绝输出:%v", ErrUsage, err)
	}
	return nil
}

// validateOrgBinary requires --binary to be a clean absolute path for the
// target OS (a drive-letter path on windows). A relative path or a bare name
// would make every managed hook command resolve through PATH (hijackable)
// and turn the binary-directory entries of denyWrite / permissions.deny into
// relative paths (a bare name becomes "." and denies the whole session
// directory), while org.json would still lock the host. Same rule as
// `ballast install --binary`, plus cleanliness since the strings are used
// verbatim by tier.ManagedForms.
func validateOrgBinary(binary, goos string) error {
	if !isAbsPath(binary, goos) || strings.ContainsAny(binary, "\x00\r\n") {
		return fmt.Errorf("%w: --binary must be an absolute path, got %q", ErrUsage, binary)
	}
	norm := binary
	if goos == "windows" {
		norm = strings.ReplaceAll(binary, `\`, "/")
	}
	if path.Clean(norm) != norm {
		return fmt.Errorf("%w: --binary %q must be a clean path (no ., .. or repeated or trailing separators)", ErrUsage, binary)
	}
	return nil
}

// validateOrgInputs checks the flag values. home_root uses path (not
// filepath) and the same character set as tier.LoadOrgConfig, so the result
// does not depend on the host OS (org.json is always a POSIX document).
func validateOrgInputs(goos string, protected, homeRoot []string) error {
	for _, p := range protected {
		if !(strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~/") || (goos == "windows" && filepath.IsAbs(p))) {
			return fmt.Errorf("%w: --protect %q must be an absolute path or start with ~/", ErrUsage, p)
		}
	}
	for _, h := range homeRoot {
		switch {
		case !strings.HasPrefix(h, "/") || path.Clean(h) != h:
			return fmt.Errorf("%w: --home-root %q must be an absolute, clean path", ErrUsage, h)
		case h == "/":
			return fmt.Errorf("%w: --home-root must not be \"/\"", ErrUsage)
		case strings.ContainsAny(h, "~*?[]{}"):
			return fmt.Errorf("%w: --home-root %q must not contain ~ or glob characters", ErrUsage, h)
		}
	}
	return nil
}

// OrgRendered is the full result of OrgRenderFull.
type OrgRendered struct {
	// Settings is the managed-settings fragment: pure JSON, byte for byte
	// what --write puts into managed-settings.d/ballast.json.
	Settings []byte
	// OrgJSON is the org.json document: pure JSON (no comments), byte for
	// byte what --write puts into <managed>/ballast/org.json. nil on
	// Windows, which is T0 only and has no org.json.
	OrgJSON []byte
	// OrgNotes are the risk notes of docs/p2-design.md §3.5 for the org.json
	// fields (one line each, no comment marker). They never go into stdout:
	// `ballast org render` prints exactly one pure JSON document per --part
	// (Settings by default, OrgJSON for --part org-json), so an administrator
	// can redirect either to a file; the CLI sends OrgNotes to stderr. Empty
	// on Windows, which has no org.json.
	OrgNotes []string
	// Written lists the files --write created or replaced.
	Written []string
	// Notes are one-line hints for stderr (for example the missing
	// pre-flight when --write runs without SUDO_UID).
	Notes []string
}

func orgNotes(o orgFile) []string {
	notes := []string{
		"runner_allow_write:每一项都等于把该目录交给 agent 可改写的测试代码,写入在 job 结束后仍然存在且不进入内容快照;共享构建缓存会被沙箱外的构建复用。runner 已把常见缓存变量指向每个 job 的临时目录,建议保持为空。",
		"runner_network:deny 在 macOS 上连回环也拒绝;loopback 让 macOS 上的测试能连主机回环上的任何服务,allow 放开出站 IP 但不放开本地 socket,两者都是残余风险。",
	}
	switch tier.DefaultWeakenedProject {
	case model.WeakenedProjectAudit:
		notes = append(notes,
			"weakened_project:deny 会让带削弱设置的项目里所有工具调用被拒,audit 只审计。当前取值 audit,依据:M0b 用例 managed-denywrite-anydepth 为 pass(`**/` 形式在托管 denyWrite 与 permissions.deny 中已证明有效)。")
	default:
		notes = append(notes,
			"weakened_project:deny 会让带削弱设置的项目里所有工具调用被拒,audit 只审计。当前取值 deny,依据:M0b 用例 managed-denywrite-anydepth 尚未证明 `**/` 形式有效(未证明 ⇒ deny);证明有效后缺省改为 audit。")
	}
	if len(o.HomeRoot) == 0 {
		notes = append(notes,
			"home_root:未设置。sssd/LDAP 用户在静态构建下查不到 passwd 条目,已锁定主机上他们的每次 hook 调用都会放行;"+homeRootFixes+"。")
	} else {
		notes = append(notes, "home_root:仅当 passwd 查不到当前 uid 时才采信 $HOME,且其父目录必须恰好是这里的某一项、整条链 root 所有且不可被组或其他用户写。")
	}
	return notes
}

func renderOrg(lay layout.Layout, fsys sys.FS, opt OrgRenderOptions) ([]byte, error) {
	r, err := OrgRenderFull(lay, fsys, opt)
	if r == nil {
		return nil, err
	}
	return r.Settings, err
}

// orgPreflight is the --write home-directory pre-flight (docs/p2-design.md
// §3.5). It returns a note (written to stderr) when the pre-flight cannot
// run, and an ErrUsage error (exit 2) when the user that invoked sudo has
// no passwd entry and the org.json to be written has no home_root.
func orgPreflight(opt OrgRenderOptions) (note string, err error) {
	env := opt.Env
	if env == nil {
		env = sys.OSEnv{}
	}
	const cannot = "无法替最终用户预检,部署后请让每个用户运行 `ballast doctor`"
	raw, _ := env.Getenv("SUDO_UID")
	if raw == "" {
		return cannot, nil
	}
	uid, perr := strconv.Atoi(raw)
	if perr != nil || uid < 0 {
		return cannot, nil
	}
	lookup := opt.LookupHome
	if lookup == nil {
		lookup = sys.LookupHome
	}
	_, lerr := lookup(uid)
	switch {
	case lerr == nil:
		return "", nil
	case errors.Is(lerr, sys.ErrNoPasswdEntry):
		if len(opt.HomeRoot) > 0 {
			return "", nil
		}
		return "", fmt.Errorf("%w: 调用 sudo 的用户(uid %d)在 passwd 数据库中没有条目,而将写入的 org.json 没有 home_root:"+
			"写入后该用户的每次 hook 调用都会因家目录不确定而放行。%s;未写入任何文件", ErrUsage, uid, homeRootFixes)
	default:
		return cannot + "(passwd 查询失败:" + lerr.Error() + ")", nil
	}
}

// OrgRenderFull renders the T1 managed deployment: the Claude
// managed-settings fragment and org.json. denyWrite and permissions.deny are
// exactly tier.ManagedForms(lay, Binary, Protected); Protected is also
// written to org.json "protected_extra", so tier.Detect recomputes the same
// strings. With Write+IAmAdmin and root it writes
// <ManagedDir>/managed-settings.d/ballast.json and then
// <ManagedDir>/ballast/org.json (0644; org.json last, because writing it is
// what locks the host), after the home-directory pre-flight.
func OrgRenderFull(lay layout.Layout, fsys sys.FS, opt OrgRenderOptions) (*OrgRendered, error) {
	goos := firstNonEmpty(opt.GOOS, lay.GOOS)
	if goos == "" {
		goos = "linux"
	}
	binary := opt.Binary
	if binary == "" {
		if goos == "windows" {
			binary = `C:\Program Files\Ballast\ballast.exe`
		} else {
			binary = "/usr/local/libexec/ballast/ballast"
		}
	}
	if err := validateOrgBinary(binary, goos); err != nil {
		return nil, err
	}
	extra := dedupe(opt.Protected)
	homeRoot := dedupe(opt.HomeRoot)
	if err := validateOrgInputs(goos, extra, homeRoot); err != nil {
		return nil, err
	}

	flay := lay
	flay.GOOS = goos
	if flay.OrgRules == "" {
		managed := firstNonEmpty(lay.ManagedDir, layout.ManagedDirFor(goos))
		sep := "/"
		if goos == "windows" {
			sep = `\`
		}
		flay.OrgRules = managed + sep + "ballast" + sep + "rules.json"
	}
	denyWrite, deny := tier.ManagedForms(flay, binary, extra)

	s := orgSettings{
		AllowManagedHooksOnly: true,
		Hooks:                 map[string][]orgGroup{},
		Sandbox: orgSandbox{
			Enabled: true, AllowUnsandboxedCommands: false, FailIfUnavailable: true,
			ExcludedCommands: []string{},
			Filesystem:       orgFS{DenyWrite: denyWrite},
		},
		Permissions:                     orgPermissions{Deny: deny},
		AllowManagedPermissionRulesOnly: true,
	}
	for _, ne := range model.NativeEventTable[model.ClientClaude] {
		if !ne.Install {
			continue
		}
		g := orgGroup{Hooks: []orgHook{{Type: "command", Command: binary, Args: HookArgv(binary, model.ClientClaude, ne.Native)[1:]}}}
		if ne.Event.IsToolEvent() {
			g.Matcher = "*"
		}
		s.Hooks[ne.Native] = []orgGroup{g}
	}
	if len(opt.MCPAllow) > 0 {
		s.AllowManagedMCPServersOnly = true
		for _, n := range opt.MCPAllow {
			s.AllowedMCPServers = append(s.AllowedMCPServers, orgMCPServer{ServerName: n})
		}
	}
	settings, err := marshalIndent(&s)
	if err != nil {
		return nil, err
	}
	res := &OrgRendered{Settings: settings}

	orgPath := lay.OrgConfig
	if orgPath == "" {
		sep := "/"
		if goos == "windows" {
			sep = `\`
		}
		orgPath = firstNonEmpty(lay.ManagedDir, layout.ManagedDirFor(goos)) + sep + "ballast" + sep + "org.json"
	}
	if goos != "windows" {
		of := orgFile{
			V: 1, Tier: model.TierT1,
			RunnerSandbox: model.SandboxRequire, RunnerNetwork: model.NetworkDeny,
			RunnerAllowWrite: []string{},
			HomeRoot:         homeRoot,
			WeakenedProject:  tier.DefaultWeakenedProject,
			ProtectedExtra:   extra,
		}
		if res.OrgJSON, err = marshalIndent(&of); err != nil {
			return nil, err
		}
		if err := validateOrgJSON(res.OrgJSON, orgPath); err != nil {
			return nil, err
		}
		res.OrgNotes = orgNotes(of)
	}

	if !opt.Write {
		return res, nil
	}
	if !opt.IAmAdmin {
		return res, fmt.Errorf("%w: --write requires --i-am-admin", ErrOrgWriteDenied)
	}
	if geteuid() != 0 {
		return res, fmt.Errorf("%w: --write requires root", ErrOrgWriteDenied)
	}
	note, perr := orgPreflight(opt)
	if perr != nil {
		return res, perr
	}
	if note != "" {
		res.Notes = append(res.Notes, note)
	}
	dst := filepath.Join(lay.ClaudeManagedSettingsD(), "ballast.json")
	if err := fsys.MkdirAll(lay.ClaudeManagedSettingsD(), 0o755); err != nil {
		return res, fmt.Errorf("create %s: %w", lay.ClaudeManagedSettingsD(), err)
	}
	if err := sys.WriteFileAtomic(fsys, sys.RandomID{}, dst, settings, 0o644, true); err != nil {
		return res, fmt.Errorf("write %s: %w", dst, err)
	}
	res.Written = append(res.Written, dst)
	if res.OrgJSON != nil {
		if err := fsys.MkdirAll(filepath.Dir(orgPath), 0o755); err != nil {
			return res, fmt.Errorf("create %s: %w", filepath.Dir(orgPath), err)
		}
		if err := sys.WriteFileAtomic(fsys, sys.RandomID{}, orgPath, res.OrgJSON, 0o644, true); err != nil {
			return res, fmt.Errorf("write %s: %w", orgPath, err)
		}
		res.Written = append(res.Written, orgPath)
	}
	return res, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
