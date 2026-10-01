"""Own a private worker process group and reap it even if the worker is killed.

Linux child-subreaper adoption makes forked selection holders waitable here.
This supervisor must remain outside the group it terminates.
"""

import ctypes
import os
import signal
import subprocess
import sys
import time

if ctypes.CDLL(None, use_errno=True).prctl(36, 1, 0, 0, 0) != 0:
    raise OSError(ctypes.get_errno(), "PR_SET_CHILD_SUBREAPER")

cancelled = False


def cancel(signum, frame):
    global cancelled
    cancelled = True


signal.signal(signal.SIGTERM, cancel)
signal.signal(signal.SIGINT, cancel)
worker = subprocess.Popen(sys.argv[1:], start_new_session=True)
try:
    while worker.poll() is None and not cancelled:
        time.sleep(0.05)
finally:
    # The harness and every launch share this group, including forked wl-copy.
    # SIGKILL also handles stopped compositors and SIGTERM-resistant holders.
    try:
        os.killpg(worker.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    worker.wait()
    while True:
        try:
            os.waitpid(-1, 0)
        except ChildProcessError:
            break
    print("Supervisor reaped private worker group and adopted holders", flush=True)
sys.exit(1 if cancelled else worker.returncode)
