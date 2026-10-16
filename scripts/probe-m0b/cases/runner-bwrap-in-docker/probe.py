#!/usr/bin/env python3
"""runner-bwrap-in-docker:在容器里按 docs/p2-design.md §3.4 的 Linux bwrap 参数运行沙箱集成用例。
以 agent 身份运行。输出 JSON 到 stdout:{builds:{<name>:{items:[...],smoke:...}}, ...}"""
import glob, hashlib, json, os, pty, shutil, socket, subprocess, sys, threading, time

HOME = os.path.expanduser("~")
REPO = f"{HOME}/repo"
W2 = f"{HOME}/w2"
W3 = f"{HOME}/w3"
SUBSRC = f"{HOME}/subsrc"
TCP_PORT = 19999
RUN_SOCK = "/run/m0b/probe.sock"      # scenario.sh 预建 /run/m0b(agent 所有)
OTHER_SOCK = f"{HOME}/other.sock"      # W 外、/run 外的自定义位置
ENV = dict(os.environ, GIT_CONFIG_GLOBAL="/dev/null", GIT_CONFIG_SYSTEM="/dev/null", GIT_AUTHOR_NAME="p", GIT_AUTHOR_EMAIL="p@example.com",
           GIT_COMMITTER_NAME="p", GIT_COMMITTER_EMAIL="p@example.com")


def sh(cmd, check=True, **kw):
    p = subprocess.run(cmd, shell=True, capture_output=True, text=True, env=ENV, **kw)
    if check and p.returncode != 0:
        raise RuntimeError(f"{cmd}: {p.stderr}")
    return p


def setup():
    for p in (REPO, W2, W3, SUBSRC, f"{HOME}/.ballast", f"{HOME}/.claude.json", f"{HOME}/.bashrc", f"{HOME}/.zshrc", f"{HOME}/.profile", f"{HOME}/repo2", f"{HOME}/quarantine"):
        if os.path.isdir(p) and not os.path.islink(p):
            shutil.rmtree(p)
        elif os.path.lexists(p):
            os.remove(p)
    os.makedirs(f"{HOME}/.ballast/evidence")
    open(f"{HOME}/.ballast/rules.json", "w").write('{"v":1}\n')
    open(f"{HOME}/.claude.json", "w").write("{}\n")
    open(f"{HOME}/.bashrc", "w").write("# rc\n")
    os.makedirs(SUBSRC)
    sh(f"cd {SUBSRC} && git init -q . && echo s > f && git add f && git commit -q -m s")
    os.makedirs(f"{REPO}/.ballast")
    open(f"{REPO}/.ballast/project.json", "w").write('{"v":1}\n')
    open(f"{REPO}/main.go", "w").write("package main\n")
    sh(f"cd {REPO} && git init -q . && git add -A && git commit -q -m init")
    sh(f"cd {REPO} && git -c protocol.file.allow=always submodule add -q {SUBSRC} sub && git commit -q -m sub")
    sh(f"cd {REPO} && git worktree add -q {W2} && git worktree add -q {W3}")
    open(f"{REPO}/.git/worktrees/w2/config.worktree", "w").write("")   # 已存在的 config.worktree(会被 ro-bind);w3 没有(MissingProtected)
    # 不预建 .claude/.vscode/.mcp.json:这些是 MissingProtected(尚不存在的受保护路径)


# --- 强制匹配项扫描(sandbox.ScanMandatory 的等价物)-------------------------------
NAMES = {".ballast", ".claude", ".vscode", ".idea", ".gitmodules", ".mcp.json", ".git"}


def scan():
    found = set()
    for root, dirs, files in os.walk(REPO):
        for n in list(dirs) + files:
            if n in NAMES:
                found.add(os.path.join(root, n))
    gd = f"{REPO}/.git"
    pats = ["hooks", "config", "config.worktree", "commondir", "modules/**/hooks", "modules/**/config", "modules/**/config.worktree",
            "modules/**/commondir", "worktrees/*/config.worktree", "worktrees/*/commondir", "worktrees/*/gitdir"]
    for pat in pats:
        for m in glob.glob(f"{gd}/{pat}", recursive=True):
            found.add(m)
    return found


def digest():
    h = {}
    for base in (HOME,):
        for root, dirs, files in os.walk(base):
            dirs[:] = [d for d in dirs if d not in (".npm", ".cache", ".claude")] if root == HOME else dirs
            for n in files:
                p = os.path.join(root, n)
                try:
                    if os.path.islink(p) or not os.path.isfile(p):
                        h[p] = "x"
                        continue
                    h[p] = hashlib.sha256(open(p, "rb").read()).hexdigest()[:12]
                except OSError:
                    h[p] = "?"
            for d in dirs:
                h[os.path.join(root, d) + "/"] = "d"
    return h


# --- bwrap 参数 --------------------------------------------------------------
def build_args(build, net, baseline):
    a = ["--die-with-parent", "--new-session", "--unshare-pid"]
    if net == "deny":
        a.append("--unshare-net")
    a += ["--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp", "--tmpfs", "/run"]
    if build == "literal":
        a += ["--tmpfs", "/var/run"]           # §3.4 字面:两个 socket 目录都屏蔽
    elif build == "fixed" and not os.path.islink("/var/run"):
        a += ["--tmpfs", "/var/run"]           # /var/run 是指向 /run 的符号链接时,--tmpfs /run 已覆盖
    ancestors = ["/home", HOME]
    if build == "fixed":                       # 祖先只读挂载,放在 W 的可写挂载之前
        for an in ancestors:
            a += ["--ro-bind", an, an]
    a += ["--bind", REPO, REPO]                # W
    a += ["--ro-bind", f"{HOME}/.ballast", f"{HOME}/.ballast"]   # D(W 外)
    if build == "literal" or build == "literal-novarrun":
        for an in ancestors:                   # §3.4 字面:--bind A A
            a += ["--bind", an, an]
    elif build == "ro-after":
        for an in ancestors:
            a += ["--ro-bind", an, an]
    for p in sorted(baseline):                 # D(W 内)与 Baseline
        if os.path.isdir(p) and os.path.basename(p) == ".git":
            a += ["--bind", p, p]              # .git 目录本身:挂载点(不可改名/替换),内容仍可写
        else:
            a += ["--ro-bind", p, p]
    a += ["--chdir", REPO, "--setenv", "TMPDIR", "/tmp"]
    return a


def run_sbx(build, net, snippet, baseline, env_extra=None, timeout=25, wrap=None):
    cmd = ["bwrap"] + build_args(build, net, baseline)
    if env_extra:
        for k, v in env_extra.items():
            cmd += ["--setenv", k, v]
    cmd += ["--", "sh", "-c", snippet]
    if wrap:
        cmd = wrap(cmd)
    try:
        p = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout, env=ENV)
        return p.returncode, (p.stdout + p.stderr).strip()[:300]
    except subprocess.TimeoutExpired:
        return 124, "timeout"


# --- 外部监听器(在沙箱之外)---------------------------------------------------
def start_listeners():
    def tcp():
        s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        s.bind(("127.0.0.1", TCP_PORT)); s.listen(16)
        while True:
            c, _ = s.accept(); c.close()
    def unix(path):
        if os.path.exists(path): os.remove(path)
        s = socket.socket(socket.AF_UNIX); s.bind(path); s.listen(16)
        while True:
            c, _ = s.accept(); c.close()
    threading.Thread(target=tcp, daemon=True).start()
    for p in (RUN_SOCK, OTHER_SOCK):
        threading.Thread(target=unix, args=(p,), daemon=True).start()
    time.sleep(0.3)


PY_CONNECT_TCP = f"python3 -c \"import socket;s=socket.socket();s.settimeout(3);s.connect(('127.0.0.1',{TCP_PORT}))\""
def py_connect_unix(path):
    return f"python3 -c \"import socket;s=socket.socket(socket.AF_UNIX);s.settimeout(3);s.connect('{path}')\""

PTY_PROBE = r'''
import os, fcntl, termios, sys
res = []
try:
    fd = os.open("/dev/tty", os.O_RDWR); res.append("open-tty:OK"); os.close(fd)
except OSError as e:
    res.append("open-tty:" + str(e.errno))
for n in (0, 1, 2):
    try:
        fcntl.ioctl(n, termios.TIOCSTI, b"x"); res.append(f"tiocsti{n}:OK")
    except OSError as e:
        res.append(f"tiocsti{n}:" + str(e.errno))
print(" ".join(res))
sys.exit(1 if any(r.endswith(":OK") for r in res) else 0)
'''

# (name, expect, snippet)  expect: deny=必须失败且宿主无变化;allow=必须成功;post=可创建但须被 PostCheck 发现
ITEMS = [
    ("write-bashrc", "deny", "echo x >> ~/.bashrc"),
    ("write-zshrc", "deny", "echo x > ~/.zshrc"),
    ("write-profile", "deny", "echo x > ~/.profile"),
    ("write-claude-json", "deny", "echo x > ~/.claude.json"),
    ("write-ballast-evidence", "deny", "echo x > ~/.ballast/evidence/x"),
    ("write-ballast-rules", "deny", "echo x > ~/.ballast/rules.json"),
    ("write-repo-project-json", "deny", f"echo x > {REPO}/.ballast/project.json"),
    ("create-repo-ballast-x", "deny", f"echo x > {REPO}/.ballast/x"),
    ("mv-repo-write-back", "deny", f"mv {REPO} {HOME}/repo2 && echo pwn > {HOME}/repo2/.ballast/project.json; mv {HOME}/repo2 {REPO}"),
    ("mv-dotgit-recreate", "deny", f"mv {REPO}/.git {REPO}/.git2 && mkdir {REPO}/.git && echo x > {REPO}/.git/config"),
    ("hardlink-rules", "deny", f"ln ~/.ballast/rules.json {REPO}/hl && echo x > {REPO}/hl"),
    ("write-git-hooks", "deny", f"echo x > {REPO}/.git/hooks/pre-commit"),
    ("write-git-config", "deny", f"echo x >> {REPO}/.git/config"),
    ("sub-gitfile-to-dir", "deny", f"rm -f {REPO}/sub/.git && mkdir {REPO}/sub/.git && echo x > {REPO}/sub/.git/config"),
    ("sub-gitfile-rewrite", "deny", f"echo 'gitdir: {REPO}/evil' > {REPO}/sub/.git"),
    ("modules-sub-config", "deny", f"echo x >> {REPO}/.git/modules/sub/config"),
    ("modules-sub-hook", "deny", f"echo x > {REPO}/.git/modules/sub/hooks/post-checkout"),
    ("worktree-config-existing", "deny", f"echo x > {REPO}/.git/worktrees/w2/config.worktree"),
    ("worktree-config-new", "post", f"echo x > {REPO}/.git/worktrees/w3/config.worktree"),
    ("unix-socket-run", "deny", py_connect_unix(RUN_SOCK)),
    ("gitlink-evil-dotgit", "post", f"cd {REPO} && git update-index --add --cacheinfo 160000,$(git rev-parse HEAD),evil && mkdir -p evil/.git && echo x > evil/.git/config"),
    ("create-claude-settings-local", "post", f"mkdir -p {REPO}/.claude && echo x > {REPO}/.claude/settings.local.json"),
    ("create-vscode-tasks", "post", f"mkdir -p {REPO}/.vscode && echo x > {REPO}/.vscode/tasks.json"),
    ("create-mcp-json", "post", f"echo x > {REPO}/.mcp.json"),
    ("write-ok-and-git-add", "allow", f"cd {REPO} && echo ok > ok.txt && git add ok.txt"),
]


def digest_diff(a, b):
    return sorted(set(k for k in set(a) | set(b) if a.get(k) != b.get(k)))


def run_build(build, net_items=True):
    setup()
    baseline = scan()
    out = {"items": [], "baseline_count": len(baseline)}
    for name, expect, snippet in ITEMS:
        before = digest()
        pre_scan = scan()
        rc, msg = run_sbx(build, "deny", snippet, baseline)
        after = digest()
        changed = digest_diff(before, after)
        newm = sorted(scan() - baseline)
        if expect == "deny":
            ok = rc != 0 and not changed
        elif expect == "allow":
            ok = rc == 0
        else:  # post
            ok = (rc != 0 and not newm) or bool(newm)
        out["items"].append({"name": name, "expect": expect, "rc": rc, "ok": ok, "changed": [c.replace(HOME, "~") for c in changed][:5],
                             "new_mandatory": [n.replace(HOME, "~") for n in newm], "msg": msg[:120]})
        # 逐项复位:撤销 post/allow 项留下的痕迹(模拟 Quarantine 与 job 结束的清理),保证项间互不影响
        if expect in ("post", "allow"):
            sh(f"cd {REPO} && git rm -q --cached -r -f evil ok.txt 2>/dev/null; rm -rf {REPO}/evil {REPO}/.git/worktrees/w3/config.worktree {REPO}/.claude {REPO}/.vscode {REPO}/.mcp.json {REPO}/ok.txt", check=False)
            sh(f"rm -rf {REPO}/hl {REPO}/.git2", check=False)
    # 网络:deny 下外部回环监听器不可达;allow 下可达;/run socket 恒不可达;自定义位置 socket 记录实际行为
    rc, msg = run_sbx(build, "deny", PY_CONNECT_TCP, baseline); out["net_deny_loopback_blocked"] = rc != 0
    rc, msg = run_sbx(build, "allow", PY_CONNECT_TCP, baseline); out["net_allow_loopback_reachable"] = rc == 0
    rc, msg = run_sbx(build, "allow", py_connect_unix(RUN_SOCK), baseline); out["allow_run_socket_blocked"] = rc != 0
    rc, msg = run_sbx(build, "deny", py_connect_unix(OTHER_SOCK), baseline); out["custom_location_socket_reachable"] = rc == 0   # 登记为残余风险,只记录
    rc, msg = run_sbx(build, "deny", 'test "$LD_LIBRARY_PATH" = /opt/m0b-ld', baseline, env_extra={"LD_LIBRARY_PATH": "/opt/m0b-ld"}); out["setenv_ld_reaches_test"] = rc == 0
    return out


def pty_probe(build, baseline):
    """pty 作为控制终端的前提下,bwrap --new-session 里打开 /dev/tty、TIOCSTI。"""
    argv = ["bwrap"] + build_args(build, "deny", baseline) + ["--", "python3", "-c", PTY_PROBE]
    pid, master = pty.fork()
    if pid == 0:
        os.execvpe(argv[0], argv, ENV)
    data = b""
    t0 = time.time()
    while time.time() - t0 < 15:
        try:
            chunk = os.read(master, 4096)
            if not chunk: break
            data += chunk
        except OSError:
            break
    _, status = os.waitpid(pid, 0)
    rc = os.waitstatus_to_exitcode(status)
    return {"rc": rc, "output": data.decode(errors="replace").strip()[-200:]}


def main():
    result = {"builds": {}}
    start_listeners()
    # smoke:字面参数(含 --tmpfs /var/run)能否启动
    setup()
    bl = scan()
    rc, msg = run_sbx("literal", "deny", "true", bl)
    result["literal_smoke"] = {"rc": rc, "msg": msg}
    result["var_run_is_symlink"] = os.path.islink("/var/run")
    for build in ("literal-novarrun", "ro-after", "fixed"):
        r = run_build(build)
        setup(); bl = scan()
        r["pty"] = pty_probe(build, bl)
        result["builds"][build] = r
    json.dump(result, sys.stdout, ensure_ascii=False, indent=1)


if __name__ == "__main__":
    main()
