#!/usr/bin/env python3
"""t1-tty-probe.py - verify entry `tty-probe` of boundary-t1.sh (t1-tty).

Runs inside the runner's sandbox with the human's terminal as the runner's
controlling terminal. The wrapped test command gets a new session
(`bwrap --new-session`), so it must not be able to reach that terminal:
opening /dev/tty, TIOCSTI on fds 0/1/2 and writing /dev/tty must all fail.
Exits 0 only when every attempt failed. The injected text would create
/tmp/pwned if it ever reached a shell.
"""
import fcntl
import os
import sys
import termios

INJECT = b"touch /tmp/pwned\n"
bad = 0


def blocked(name, ok):
    global bad
    if ok:
        print("blocked:", name, file=sys.stderr)
    else:
        print("ESCAPED:", name, file=sys.stderr)
        bad += 1


try:
    fd = os.open("/dev/tty", os.O_RDWR)
    os.close(fd)
    blocked("open /dev/tty", False)
except OSError:
    blocked("open /dev/tty", True)

for fd in (0, 1, 2):
    try:
        for ch in INJECT:
            fcntl.ioctl(fd, termios.TIOCSTI, bytes([ch]))
        blocked("TIOCSTI on fd %d" % fd, False)
    except OSError:
        blocked("TIOCSTI on fd %d" % fd, True)

try:
    with open("/dev/tty", "w") as t:
        t.write("injected\n")
    blocked("write /dev/tty", False)
except OSError:
    blocked("write /dev/tty", True)

sys.exit(1 if bad else 0)
