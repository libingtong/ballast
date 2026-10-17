package install

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/libingtong/ballast/internal/layout"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/sys"
	"github.com/libingtong/ballast/internal/tier"
)

// cgoEnabled reports whether this binary was built with cgo (build setting
// CGO_ENABLED=1); tests replace it.
var cgoEnabled = func() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "CGO_ENABLED" {
			return s.Value == "1"
		}
	}
	return false
}

// slowLookupDarwin is the threshold above which a darwin passwd lookup gets
// the directory-account note (docs/p2-design.md §3.2 option ④).
const slowLookupDarwin = 5 * time.Millisecond

// missingText explains each TierInfo.Missing id (doctor tier.level).
var missingText = map[string]string{
	model.TierMissingOS:           "仅 darwin 与 linux(含 WSL2)可为 T1",
	model.TierMissingEnvOverride:  "BALLAST_HOME / BALLAST_MANAGED_DIR / BALLAST_INSTALL_ROOT 在未锁定的主机上生效",
	model.TierMissingBinaryOwner:  "ballast 二进制或其某级父目录不是 root 所有,或组/其他用户可写",
	model.TierMissingOrgConfig:    "托管目录下的 ballast/org.json 不存在、不是 root 所有、校验失败,或其父目录链可被非 root 写入,或未声明 \"tier\":\"T1\"",
	model.TierMissingManagedFile:  "没有 root 所有且组/其他用户不可写的 Claude 托管设置文件",
	model.TierMissingManagedHooks: "托管设置中没有命令为绝对路径且指向本二进制的 ballast hook",
	model.TierMissingManagedOnly:  "allowManagedHooksOnly 不是 true(低层 hook 不会失效)",
	model.TierMissingSandbox:      "托管 sandbox 未启用,或允许无沙箱命令、不要求沙箱可用,或带有 excludedCommands / allowUnixSockets / enableWeakerNestedSandbox / allowAllUnixSockets",
	model.TierMissingDenyWrite:    "sandbox.filesystem.denyWrite 未逐字覆盖受保护路径(ballast org render 的输出)",
	model.TierMissingPermDeny:     "permissions.deny 未逐字覆盖受保护路径(ballast org render 的输出)",
	model.TierMissingManagedPerms: "allowManagedPermissionRulesOnly 不是 true",
	model.TierMissingUnreadable:   "某个输入读取或解析失败(判定取保守一侧)",
	model.TierMissingLowerSandbox: "用户层 settings*.json 含可能削弱托管沙箱的键(excludedCommands、allowUnixSockets 等)",
}

// claudeConfigDirRedirected reports whether CLAUDE_CONFIG_DIR points away
// from <home>/.claude (v1.5: then the user-layer files Claude loads are not
// covered by the literal "~/.claude/settings*.json" forms).
func claudeConfigDirRedirected(lay layout.Layout, env sys.Env) (string, bool) {
	if env == nil {
		return "", false
	}
	v, _ := env.Getenv(layout.EnvClaudeDir)
	if v == "" {
		return "", false
	}
	home := lay.InstallRoot
	if home == "" {
		home, _ = env.HomeDir()
	}
	if home == "" {
		return v, true
	}
	return v, filepath.Clean(v) != filepath.Join(home, ".claude")
}

func describeMissing(id string, lay layout.Layout, env sys.Env) string {
	text := missingText[id]
	if id == model.TierMissingDenyWrite {
		if v, redirected := claudeConfigDirRedirected(lay, env); redirected {
			text += ";CLAUDE_CONFIG_DIR 已设置为 " + v + " 且不等于 <home>/.claude:Claude 实际加载的用户层设置不在字面形式 ~/.claude/settings*.json 之内,托管配置挡不住 agent 改写它"
		}
	}
	if text == "" {
		return id
	}
	return id + "(" + text + ")"
}

func redirectVars(env sys.Env) []string {
	if env == nil {
		return nil
	}
	var out []string
	for _, k := range []string{layout.EnvHome, layout.EnvManagedDir, layout.EnvInstallRoot} {
		if v, _ := env.Getenv(k); v != "" {
			out = append(out, k)
		}
	}
	return out
}

// tierChecks implements the tier.* doctor checks (docs/p2-design.md §3.5):
// tier.level (tier.Detect, every Missing id listed), tier.declared_mismatch,
// tier.runner_sandbox, tier.redirect_ignored, tier.locked_home,
// tier.repo_sandbox_weakened and tier.binary_hardening.
//
// v0.6 positioning: T0 is the product guarantee and the default, T1 is an
// experimental feature. On a T0 host that has not opted into T1 (no org.json
// declaring T1, no locked host, no `doctor --t1`) tier.level is a plain OK
// "T0(默认模式)" and no other tier.* row is emitted. Native Windows is T0
// only and always takes that shape.
func tierChecks(opt DoctorOptions, fsys sys.FS, lay layout.Layout, goos, self string) []Check {
	if goos == "windows" {
		return []Check{t0DefaultLevel()}
	}
	var info model.TierInfo
	if opt.Tier != nil {
		info = opt.Tier.Probe()
	} else {
		info = tier.Detect(tier.Options{
			FS: fsys, Env: opt.Env, Layout: lay, Self: self, GOOS: goos,
			Owner: opt.TierOwner, ManagedDir: opt.TierManagedDir,
		})
	}
	t1 := info.Tier == model.TierT1
	org, locked := lockState(opt, fsys, goos)

	// T1 detail is shown only when the user opted in: --t1, a trusted
	// org.json declaring T1, a locked host, or a host already judged T1.
	if !(opt.T1 || t1 || info.Declared == model.TierT1 || locked) {
		return []Check{t0DefaultLevel()}
	}

	out := []Check{tierLevel(info, lay, opt.Env)}
	out = append(out,
		tierDeclared(info),
		tierRunnerSandbox(t1, opt.SandboxAvailable),
		tierRedirectIgnored(locked, opt.Env),
		tierLockedHome(opt, fsys, goos, locked, org),
		tierRepoSandbox(opt, fsys, t1),
		tierBinaryHardening(fsys, goos, self, t1),
	)
	return out
}

// t0DefaultLevel is the tier.level row of a host that has not opted into the
// experimental T1: T0 is the default guarantee, nothing is "missing".
func t0DefaultLevel() Check {
	return Check{ID: "tier.level", Status: CheckOK, Message: "T0(默认模式)"}
}

func lockState(opt DoctorOptions, fsys sys.FS, goos string) (tier.OrgConfig, bool) {
	if opt.TierLock != nil {
		return opt.TierLock()
	}
	return tier.LockState(fsys, goos, opt.TierOwner)
}

func tierLevel(info model.TierInfo, lay layout.Layout, env sys.Env) Check {
	if info.Tier == model.TierT1 && len(info.Missing) == 0 {
		return Check{ID: "tier.level", Status: CheckOK, Message: "T1: managed deployment complete"}
	}
	var parts []string
	for _, id := range info.Missing {
		parts = append(parts, describeMissing(id, lay, env))
	}
	msg := "T0 (best effort): tampering is detectable but not preventable"
	if len(parts) > 0 {
		msg += "; missing T1 components: " + strings.Join(parts, "; ")
	}
	return Check{ID: "tier.level", Status: CheckWarn, Message: msg,
		Fix: "run: ballast org render (an administrator deploys the output as managed settings)"}
}

func tierDeclared(info model.TierInfo) Check {
	c := Check{ID: "tier.declared_mismatch"}
	if info.Declared == model.TierT1 && info.Tier != model.TierT1 {
		c.Status = CheckFail
		c.Message = "org.json 声明 T1,但本机判定为 T0(缺少: " + strings.Join(info.Missing, ", ") + ")"
		c.Fix = "补齐 tier.level 列出的组件,或重新运行 ballast org render 并重新部署;hook 遇到规范 verify 时会审计 tier_mismatch"
		return c
	}
	c.Status = CheckOK
	switch info.Declared {
	case model.TierT1:
		c.Message = "org.json 声明 T1,与判定一致"
	case "":
		c.Message = "没有可信的 org.json 声明"
	default:
		c.Message = "org.json 声明 " + string(info.Declared)
	}
	return c
}

func tierRunnerSandbox(t1 bool, avail func() error) Check {
	c := Check{ID: "tier.runner_sandbox"}
	var err error
	if avail != nil {
		err = avail()
	}
	switch {
	case !t1:
		c.Status = CheckOK
		c.Message = "T0:runner 沙箱不是必需的"
		if err != nil {
			c.Message += "(当前后端不可用: " + err.Error() + ")"
		}
	case avail == nil:
		c.Status, c.Message = CheckWarn, "not evaluated: 未提供沙箱探测函数"
	case err != nil:
		c.Status = CheckFail
		c.Message = "T1 主机上 runner 沙箱后端不可用(" + err.Error() + "):hook 代起的 verify 将全部记为 infra(sandbox_unavailable)"
		c.Fix = "macOS 需要 /usr/bin/sandbox-exec;Linux 需要 bubblewrap(bwrap)并允许非特权用户命名空间"
	default:
		c.Status, c.Message = CheckOK, "runner 沙箱后端可用"
	}
	return c
}

func tierRedirectIgnored(locked bool, env sys.Env) Check {
	c := Check{ID: "tier.redirect_ignored"}
	vars := redirectVars(env)
	switch {
	case locked && len(vars) > 0:
		c.Status = CheckWarn
		c.Message = "主机已锁定,以下测试重定向变量被忽略(只列名字): " + strings.Join(vars, ", ")
		c.Fix = "取消设置这些变量;生产构建在已锁定主机上忽略它们,org 规则、状态与证据始终在真实位置"
	case locked:
		c.Status, c.Message = CheckOK, "主机已锁定,没有设置重定向变量"
	case len(vars) > 0:
		c.Status, c.Message = CheckOK, "主机未锁定,重定向变量生效(仅用于测试): "+strings.Join(vars, ", ")
	default:
		c.Status, c.Message = CheckOK, "没有设置重定向变量"
	}
	return c
}

func tierLockedHome(opt DoctorOptions, fsys sys.FS, goos string, locked bool, org tier.OrgConfig) Check {
	c := Check{ID: "tier.locked_home"}
	if !locked {
		c.Status = CheckOK
		c.Message = "主机未锁定(没有声明 T1 的 root 所有 org.json):家目录取自环境,无需 passwd 查询"
		return c
	}
	// The duration of the lookup call itself is measured (not the goroutine
	// hand-off of LockedHome), so a loaded machine does not produce the
	// directory-account note for an instant lookup.
	lookup := opt.HomeLookup
	if lookup == nil {
		lookup = sys.LookupHome
	}
	var elapsedNS atomic.Int64 // the lookup goroutine is abandoned on timeout
	hopt := tier.HomeOptions{
		FS: fsys, Env: opt.Env, Owner: opt.TierOwner, Euid: sys.Euid(),
		Lookup: func(uid int) (string, error) {
			start := time.Now()
			h, err := lookup(uid)
			elapsedNS.Store(int64(time.Since(start)))
			return h, err
		},
		HomeRoot: org.HomeRoot, Timeout: opt.HomeTimeout,
	}
	home, source, err := tier.LockedHome(hopt)
	elapsed := time.Duration(elapsedNS.Load())
	timeout := opt.HomeTimeout
	if timeout <= 0 {
		timeout = tier.DefaultLockedHomeTimeout
	}

	var notes []string
	switch goos {
	case "linux":
		if cgoEnabled() {
			notes = append(notes, "本构建启用 cgo:passwd 查询走 NSS,只有 NSS 由本地缓存(sssd 本地缓存或 nscd)提供时才符合热路径不联网;"+
				"nss_ldap 或未缓存的 sssd 会让每次 hook 调用可能联网或阻塞,推荐改用 home_root")
		} else if err != nil {
			notes = append(notes, "本构建为静态构建,只读 /etc/passwd")
		}
	case "darwin":
		// On timeout the lookup goroutine is abandoned and elapsed stays 0,
		// yet the lookup certainly took longer than the threshold.
		if elapsed > slowLookupDarwin || errors.Is(err, tier.ErrLockedHomeTimeout) {
			notes = append(notes, "目录账户(AD/LDAP 绑定、未缓存的移动账户)的 passwd 查询经 opendirectoryd 可能联网或阻塞,违反热路径不联网;请启用移动账户或目录服务本地缓存")
		}
	}
	suffix := ""
	if len(notes) > 0 {
		suffix = " 注意:" + strings.Join(notes, ";") + "。"
	}
	switch {
	case err == nil:
		c.Status = CheckOK
		c.Message = fmt.Sprintf("家目录 %s(来源 %s)", home, source) + suffix
	case errors.Is(err, tier.ErrLockedHomeTimeout):
		c.Status = CheckFail
		c.Message = fmt.Sprintf("passwd 查询超过 %s 时限:该账户的每次 hook 调用都会因超时而放行(B7,exit 1,无审计)。", timeout) + suffix
		c.Fix = homeRootFixes
	default:
		c.Status = CheckFail
		c.Message = "已锁定主机上无法确定家目录:" + err.Error() + "。该用户的每次 hook 调用都会因 layout 失败而放行(B7)。" + suffix
		c.Fix = homeRootFixes
	}
	return c
}

// repoTop returns the nearest ancestor of dir (dir included) that contains a
// ".git" entry, or "" when there is none. It never runs git.
func repoTop(fsys sys.FS, dir string) string {
	for d := filepath.Clean(dir); ; {
		if _, err := fsys.Lstat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

func tierRepoSandbox(opt DoctorOptions, fsys sys.FS, t1 bool) Check {
	c := Check{ID: "tier.repo_sandbox_weakened"}
	if opt.Cwd == "" {
		c.Status, c.Message = CheckWarn, "not evaluated: 未提供当前目录"
		return c
	}
	dirs := []string{filepath.Clean(opt.Cwd)}
	if top := repoTop(fsys, opt.Cwd); top != "" && top != dirs[0] {
		dirs = append(dirs, top)
	}
	var found []string
	for _, d := range dirs {
		if w := tier.RepoSandboxWeakened(fsys, d); len(w) > 0 {
			found = append(found, fmt.Sprintf("%s: %s", d, strings.Join(w, ", ")))
		}
	}
	if len(found) == 0 {
		c.Status, c.Message = CheckOK, "当前目录及其仓库顶层的项目层设置不削弱托管沙箱"
		return c
	}
	c.Status = CheckWarn
	if t1 {
		c.Status = CheckFail
	}
	c.Message = "项目层设置削弱托管沙箱(<文件>:<键>): " + strings.Join(found, "; ")
	c.Fix = "删除或修正这些键。在此目录启动的 Claude 会话可以绕过沙箱直接写证据(全机前提,trust-model §2.3);该仓库的 hook 代起 verify 只会记为 T0"
	return c
}

func tierBinaryHardening(fsys sys.FS, goos, self string, t1 bool) Check {
	c := Check{ID: "tier.binary_hardening"}
	if goos != "darwin" && goos != "linux" {
		c.Status, c.Message = CheckOK, "不适用于 "+goos
		return c
	}
	if self == "" {
		c.Status, c.Message = CheckWarn, "not evaluated: 没有 ballast 二进制路径"
		return c
	}
	b, err := fsys.ReadFile(self)
	if err != nil {
		c.Status, c.Message = CheckWarn, "not evaluated: 无法读取 "+self+": "+err.Error()
		return c
	}
	switch goos {
	case "darwin":
		st, err := machoRuntime(b)
		switch {
		case err != nil:
			c.Status, c.Message = CheckWarn, "not evaluated: 无法解析 "+self+" 的 Mach-O 代码签名: "+err.Error()
		case st == machoHardened:
			c.Status, c.Message = CheckOK, describeMacho(st)
		default:
			c.Status = CheckWarn
			if t1 {
				c.Status = CheckFail
			}
			c.Message = "ballast 二进制未启用 hardened runtime(" + describeMacho(st) + "):项目层 env 里的 DYLD_INSERT_LIBRARIES 会被托管 hook 加载"
			c.Fix = "codesign --force -s - -o runtime " + self
		}
	case "linux":
		dyn, err := elfDynamic(b)
		switch {
		case err != nil:
			c.Status, c.Message = CheckWarn, "not evaluated: 无法解析 "+self+" 的 ELF 头: "+err.Error()
		case dyn:
			c.Status = CheckWarn
			c.Message = "ballast 二进制是动态链接的(存在 PT_INTERP,例如自建的 cgo 构建):LD_PRELOAD 会被托管 hook 加载"
			c.Fix = "使用静态构建(CGO_ENABLED=0)"
		default:
			c.Status, c.Message = CheckOK, "静态链接(无 PT_INTERP)"
		}
	}
	return c
}
