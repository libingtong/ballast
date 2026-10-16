#!/usr/bin/env python3
"""t1-net-probe.py - verify entry `netprobe` of boundary-t1.sh (t1-runner-sandbox,
network variants). Runs inside the runner's sandbox and prints one
"key=value" line per probe (the output reaches the caller of
`ballast verify --foreground netprobe`); it always exits 0 - the caller judges.

  tcp_local   connect to a listener started OUTSIDE the sandbox on 127.0.0.1
  unix        connect to a unix socket under /run
  env         which of SSH_AUTH_SOCK / DOCKER_HOST / DBUS_*_BUS_ADDRESS exist
  external    HTTP GET of $T1_EXTERNAL_URL (skip when unset)
"""
import os
import socket
import urllib.request

port = int(os.environ.get("T1_TCP_PORT", "47931"))
sock = os.environ.get("T1_UNIX_SOCKET", "/run/ballast-t1/sock")


def run(fn):
    try:
        fn()
        return "ok"
    except Exception:
        return "fail"


def tcp():
    socket.create_connection(("127.0.0.1", port), timeout=2).close()


def unix():
    s = socket.socket(socket.AF_UNIX)
    s.settimeout(2)
    s.connect(sock)
    s.close()


def external():
    urllib.request.urlopen(os.environ["T1_EXTERNAL_URL"], timeout=5).read(16)


print("tcp_local=" + run(tcp))
print("unix=" + run(unix))
present = [v for v in ("SSH_AUTH_SOCK", "DOCKER_HOST", "DBUS_SESSION_BUS_ADDRESS", "DBUS_SYSTEM_BUS_ADDRESS") if os.environ.get(v)]
print("env=" + (",".join(present) if present else "none"))
print("external=" + (run(external) if os.environ.get("T1_EXTERNAL_URL") else "skip"))
