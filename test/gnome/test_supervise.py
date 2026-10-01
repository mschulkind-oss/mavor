"""Process lifecycle regressions; no clipboard commands are substituted."""

import os
import pathlib
import signal
import subprocess
import tempfile
import time
import unittest


class CleanupTest(unittest.TestCase):
    def check_cleanup(self, kill_worker):
        root = os.environ.get("YOLO_DURABLE_DIR", os.environ.get("XDG_CACHE_HOME", str(pathlib.Path.home() / ".cache")))
        with tempfile.TemporaryDirectory(prefix="gnome-cleanup-", dir=root) as directory:
            record = pathlib.Path(directory) / "pids"
            code = """
import os, pathlib, signal, sys, time
holder = os.fork()
if holder == 0:
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    while True: time.sleep(1)
pathlib.Path(sys.argv[1]).write_text(f'{os.getpid()} {holder}')
while True: time.sleep(1)
"""
            supervisor = subprocess.Popen(["python3", "supervise.py", "python3", "-c", code, str(record)])
            try:
                end = time.monotonic() + 5
                while not record.exists() and time.monotonic() < end:
                    time.sleep(0.02)
                self.assertTrue(record.exists(), "worker did not start")
                worker, holder = map(int, record.read_text().split())
                os.kill(worker if kill_worker else supervisor.pid, signal.SIGKILL if kill_worker else signal.SIGTERM)
                supervisor.wait(timeout=5)
                self.assertFalse(pathlib.Path(f"/proc/{worker}").exists())
                self.assertFalse(pathlib.Path(f"/proc/{holder}").exists(), "forked holder was not reaped")
            finally:
                if record.exists():
                    for pid in map(int, record.read_text().split()):
                        try:
                            os.kill(pid, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                if supervisor.poll() is None:
                    supervisor.kill()
                supervisor.wait()

    def test_worker_sigkill(self):
        self.check_cleanup(True)

    def test_supervisor_cancellation(self):
        self.check_cleanup(False)


if __name__ == "__main__":
    unittest.main()
