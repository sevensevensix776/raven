#!/usr/bin/env python3
"""spawn.py <pidfile> <cmd...>

Launches cmd in its own session (os.setsid) with fds detached, writes its pid,
and exits immediately. Survives the launching shell being killed.
"""
import os
import subprocess
import sys

pidfile, cmd = sys.argv[1], sys.argv[2:]
log = open(os.path.expanduser("~/speech/.detached.log"), "a")

p = subprocess.Popen(
    cmd,
    stdin=subprocess.DEVNULL,
    stdout=log,
    stderr=log,
    start_new_session=True,  # os.setsid() in the child
    cwd=os.path.expanduser("~/speech"),
)
open(pidfile, "w").write(str(p.pid))
