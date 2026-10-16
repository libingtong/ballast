#!/usr/bin/env python3
"""在 pty 里驱动交互式 claude TUI(M0b lower-sandbox-merge 的观测手段 ②)。

用法:tui.py <cwd> <step>...    环境变量(含 ANTHROPIC_BASE_URL、dummy key)由调用者设置。
step:
  wait:<秒>          等待并持续读取输出
  send:<文本>        发送按键(\\r、\\n、\\x1b 等按 Python 转义解释)
  snap:<名字>        保存当前累计输出末尾约 6000 字符(去 ANSI 控制序列)
输出 JSON:{"snaps": {名字: 文本}, "exit": <退出码或 null>}。结束时先发 Ctrl-C 两次,再 SIGKILL。
本地命令(/sandbox 等)不请求模型。不联网、不接触真实凭据。"""
import fcntl, json, os, pty, re, select, signal, struct, sys, termios, time

ANSI = re.compile(r"\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][A-Za-z0-9]|\x1b[=>MD78]")


def clean(b):
    t = ANSI.sub("", b.decode("utf-8", "replace"))
    t = t.replace("\r", "\n")
    return re.sub(r"\n{3,}", "\n\n", t)


def main():
    cwd = sys.argv[1]
    steps = sys.argv[2:]
    pid, fd = pty.fork()
    if pid == 0:
        os.chdir(cwd)
        os.execvp("claude", ["claude"])
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 50, 140, 0, 0))
    buf = b""
    snaps = {}

    def pump(sec):
        nonlocal buf
        end = time.time() + sec
        while time.time() < end:
            r, _, _ = select.select([fd], [], [], 0.2)
            if r:
                try:
                    d = os.read(fd, 65536)
                except OSError:
                    return
                if not d:
                    return
                buf += d

    for st in steps:
        kind, _, arg = st.partition(":")
        if kind == "wait":
            pump(float(arg))
        elif kind == "send":
            os.write(fd, arg.encode().decode("unicode_escape").encode())
            pump(0.5)
        elif kind == "snap":
            snaps[arg] = clean(buf[-12000:])[-6000:]
    code = None
    try:
        os.write(fd, b"\x03")
        pump(0.5)
        os.write(fd, b"\x03")
        pump(1.0)
    except OSError:
        pass
    try:
        p, status = os.waitpid(pid, os.WNOHANG)
        if p:
            code = os.waitstatus_to_exitcode(status)
        else:
            os.kill(pid, signal.SIGKILL)
            os.waitpid(pid, 0)
    except ChildProcessError:
        pass
    json.dump({"snaps": snaps, "exit": code}, sys.stdout, ensure_ascii=False)


main()
