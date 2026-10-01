"""Real isolated GNOME acceptance. Every unmet prerequisite is a failure.

Shell Eval sets/observes test focus only; it never reads/writes selections,
focuses wl-copy, injects keys, or approves portals.
"""

import ctypes
import os
import pathlib
import shlex
import signal
import subprocess
import sys
import tempfile
import time

# Adopt forked wl-copy holders so cleanup can reap them, not just signal them.
if ctypes.CDLL(None, use_errno=True).prctl(36, 1, 0, 0, 0) != 0:
    raise OSError(ctypes.get_errno(), "PR_SET_CHILD_SUBREAPER")


def interrupted(signum, frame):
    raise RuntimeError("harness deadline or termination: " + str(signum))


signal.signal(signal.SIGTERM, interrupted)
signal.signal(signal.SIGALRM, interrupted)
signal.alarm(40)

TITLE = "Mavor native clipboard consumer"
default_root = (
    (os.environ["YOLO_DURABLE_DIR"] + "/gnome-headless/tests")
    if "YOLO_DURABLE_DIR" in os.environ
    else (
        os.environ.get("XDG_CACHE_HOME", str(pathlib.Path.home() / ".cache"))
        + "/mavor-gnome-tests"
    )
)
root = pathlib.Path(os.environ.get("MAVOR_GNOME_ARTIFACTS", default_root)).resolve()
root.mkdir(parents=True, exist_ok=True)
run = pathlib.Path(tempfile.mkdtemp(prefix=sys.argv[2] + "-", dir=root))
print("Diagnostics:", run, flush=True)
env = os.environ.copy()
for key in (
    "LD_LIBRARY_PATH",
    "DISPLAY",
    "WAYLAND_DISPLAY",
    "DBUS_SESSION_BUS_ADDRESS",
    "DBUS_SYSTEM_BUS_ADDRESS",
    "SWAYSOCK",
    "GI_TYPELIB_PATH",
    "GIO_EXTRA_MODULES",
):
    env.pop(key, None)
for key, directory in (
    ("HOME", "home"),
    ("XDG_RUNTIME_DIR", "runtime"),
    ("XDG_CONFIG_HOME", "config"),
    ("XDG_CACHE_HOME", "cache"),
    ("XDG_DATA_HOME", "data"),
):
    (run / directory).mkdir(mode=0o700)
    env[key] = str(run / directory)
env.update(
    XDG_SESSION_TYPE="wayland",
    XDG_CURRENT_DESKTOP="GNOME",
    GSETTINGS_BACKEND="memory",
    LIBGL_ALWAYS_SOFTWARE="1",
    GDK_BACKEND="wayland",
    GTK_A11Y="none",
)
processes = []
files = []


def launch(args, label, **kwargs):
    log = open(run / (label + ".log"), "w")
    files.append(log)
    child = subprocess.Popen(
        args, env=env, stdout=log, stderr=log, **kwargs
    )
    processes.append(child)
    return child


def call(args, timeout=5):
    result = subprocess.run(
        args, env=env, capture_output=True, text=True, timeout=timeout
    )
    if result.returncode:
        raise AssertionError(f"command failed: {args}: {result.stderr}")
    return result.stdout


def evaluate(js):
    result = call(
        [
            "gdbus",
            "call",
            "--session",
            "--dest",
            "org.gnome.Shell",
            "--object-path",
            "/org/gnome/Shell",
            "--method",
            "org.gnome.Shell.Eval",
            js,
        ]
    )
    with open(run / "eval.log", "a") as log:
        log.write(js + "\n" + result)
    if not result.startswith("(true,"):
        raise AssertionError(result)
    return result


def until(check, label, timeout=10):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        try:
            if check():
                return
        except (subprocess.CalledProcessError, AssertionError):
            pass
        time.sleep(0.1)
    raise AssertionError("deadline: " + label)


def consumer_command(command, expected, target=None, label="consumer"):
    target = consumer if target is None else target
    path = run / (label + ".log")
    offset = path.stat().st_size
    target.stdin.write((command + "\n").encode())
    target.stdin.flush()
    until(
        lambda: expected in path.read_bytes()[offset:].decode(), "native " + command, 5
    )


def emit(text, label):
    env.update(MAVOR_GNOME_CHILD="1", MAVOR_GNOME_TEXT=text, WAYLAND_DEBUG="1")
    child = launch([sys.argv[1], "-test.run=^TestDispatcherChild$", "-test.v"], label)
    until(lambda: child.poll() is not None, "dispatcher startup", 5)
    return child.returncode


try:
    # Build the consumer with real GTK; dependency absence is not a skip.
    flags = shlex.split(call(["pkg-config", "--cflags", "--libs", "gtk4"]))
    call(["cc", "client/consumer.c", "-o", str(run / "consumer"), *flags], 30)
    shell = os.environ.get("MAVOR_GNOME_SHELL", "gnome-shell")
    print(
        call([shell, "--version"]).strip(),
        call(["wl-copy", "--version"]).strip(),
        flush=True,
    )
    config = run / "bus.conf"
    config.write_text(
        "<busconfig><type>session</type><listen>unix:dir="
        + env["XDG_RUNTIME_DIR"]
        + '</listen><auth>EXTERNAL</auth><policy context="default"><allow send_destination="*"/><allow receive_sender="*"/><allow own="*"/></policy></busconfig>'
    )
    address = run / "address"
    with open(address, "w") as out:
        bus = launch(
            [
                "dbus-daemon",
                "--config-file=" + str(config),
                "--nofork",
                "--print-address=" + str(out.fileno()),
            ],
            "bus",
            pass_fds=(out.fileno(),),
        )
    until(lambda: bool(address.read_text().strip()), "private bus")
    env.update(
        DBUS_SESSION_BUS_ADDRESS=address.read_text().strip(),
        DBUS_SYSTEM_BUS_ADDRESS=address.read_text().strip(),
    )
    shell_process = launch(
        [
            shell,
            "--headless",
            "--wayland",
            "--no-x11",
            "--virtual-monitor=1280x720",
            "--wayland-display=mavor-gnome-test",
            "--unsafe-mode",
        ],
        "shell",
    )
    until(
        lambda: (
            (run / "runtime/mavor-gnome-test").exists() and "true" in evaluate("true")
        ),
        "GNOME readiness",
        20,
    )
    evaluate(
        "globalThis.testKeyboard = global.stage.context.get_backend().get_default_seat().create_virtual_device(1); true"
    )
    evaluate(
        'global.display.connect("notify::focus-window", () => console.log("MAVOR_FOCUS " + (global.display.focus_window?.title ?? "NONE"))); true'
    )
    env["WAYLAND_DISPLAY"] = "mavor-gnome-test"
    env["WAYLAND_DEBUG"] = "1"
    if sys.argv[2] == "deadline":
        os.kill(shell_process.pid, signal.SIGSTOP)
        try:
            started = time.monotonic()
            assert emit("deadline transcript", "deadline-copy") != 0, (
                "stalled compositor copy falsely succeeded"
            )
            assert time.monotonic() - started < 5, "production launch deadline exceeded"
            assert (
                "context deadline exceeded" in (run / "deadline-copy.log").read_text()
            ), "not a deadline failure"
        finally:
            os.kill(shell_process.pid, signal.SIGCONT)
        print("Real wl-copy cancellation deadline verified", flush=True)
        sys.exit(0)
    if sys.argv[2] == "overview":
        evaluate("Main.overview.show(); true")
        until(
            lambda: (
                evaluate(
                    "Main.overview.visible && !Main.overview.animationInProgress && !global.display.focus_window"
                )
                .strip()
                .endswith(", 'true')")
            ),
            "overview without focus",
        )
        assert emit("initial transcript α", "initial-copy") == 0, (
            "initial no-focus copy failed (no helper activation permitted)"
        )
        assert (
            evaluate("Main.overview.visible && !global.display.focus_window")
            .strip()
            .endswith(", 'true')")
        ), "initial copy displaced overview/focus"
        # If a future strategy succeeds, delivery must still be verified natively.
    initial_events = (run / "shell.log").read_text()
    owner_title = "Mavor PRIMARY owner"
    owner = launch(
        [str(run / "consumer"), owner_title], "primary-owner", stdin=subprocess.PIPE
    )
    until(
        lambda: (
            owner_title
            in evaluate(
                "JSON.stringify(global.get_window_actors().map(a=>a.meta_window.title))"
            )
        ),
        "PRIMARY owner mapped",
    )
    evaluate(
        'Main.overview.hide(); global.get_window_actors().find(a=>a.meta_window.title === "'
        + owner_title
        + '").meta_window.activate(global.get_current_time()); true'
    )
    until(
        lambda: "FOCUS\t1" in (run / "primary-owner.log").read_text(),
        "PRIMARY owner active",
    )
    consumer_command("seed-primary", "SEEDED", owner, "primary-owner")
    consumer = launch([str(run / "consumer")], "consumer", stdin=subprocess.PIPE)
    until(
        lambda: (
            TITLE
            in evaluate(
                "JSON.stringify(global.get_window_actors().map(a=>a.meta_window.title))"
            )
        ),
        "native destination mapped",
    )
    evaluate(
        'Main.overview.hide(); global.get_window_actors().find(a=>a.meta_window.title === "'
        + TITLE
        + '").meta_window.activate(global.get_current_time()); true'
    )
    until(
        lambda: "FOCUS\t1" in (run / "consumer.log").read_text(), "destination active"
    )
    if sys.argv[2] == "overview":
        consumer_command("initial-paste", "initial-paste\tinitial transcript α\tOK")
    copy_lost_focus = False
    for index, text in enumerate(("first transcript α", "replacement transcript β")):
        offset = (run / "consumer.log").stat().st_size
        assert emit(text, "copy-" + str(index)) == 0, "focused copy failed"
        time.sleep(
            1
        )  # Intentionally delayed manual-paste transfer, not a readiness wait.
        for repeat in range(2):
            consumer_command(
                "paste-" + str(repeat), "paste-" + str(repeat) + "\t" + text + "\tOK"
            )
        consumer_command("primary", "primary\tprimary sentinel α\tOK")
        if sys.argv[2] == "forced":
            print("Native transfer complete; forcing worker SIGKILL", flush=True)
            os.kill(os.getpid(), signal.SIGKILL)
        events = (run / "consumer.log").read_bytes()[offset:].decode()
        copy_lost_focus |= "FOCUS\t0" in events
        # Controlled destination reactivation is separate from the copy interval.
        for title, active in ((owner_title, 0), (TITLE, 1)):
            focus_offset = (run / "consumer.log").stat().st_size
            evaluate(
                'global.get_window_actors().find(a=>a.meta_window.title === "'
                + title
                + '").meta_window.activate(global.get_current_time()); true'
            )
            until(
                lambda: (
                    "FOCUS\t" + str(active)
                    in (run / "consumer.log").read_bytes()[focus_offset:].decode()
                ),
                "controlled destination reactivation",
            )
        consumer_command("reactivated-paste", "reactivated-paste\t" + text + "\tOK")
    if sys.argv[2] == "overview":
        assert "MAVOR_FOCUS wl-clipboard" not in initial_events, (
            "initial copy transiently focused helper"
        )
    assert not copy_lost_focus, (
        "copy stole destination focus; continuous native focus events retained"
    )
finally:
    # Direct children are stopped here. The external supervisor owns the shared
    # process group and reaps forked holders even if this worker is SIGKILLed.
    for child in reversed(processes):
        try:
            child.terminate()
        except ProcessLookupError:
            pass
    for child in reversed(processes):
        try:
            child.wait(timeout=3)
        except subprocess.TimeoutExpired:
            child.kill()
            child.wait(timeout=3)
        try:
            child.kill()
        except ProcessLookupError:
            pass
    for log in files:
        log.close()
    signal.alarm(0)

    def reap_holders():
        while True:
            try:
                pid, _ = os.waitpid(-1, os.WNOHANG)
                if pid == 0:
                    return False
            except ChildProcessError:
                return True

    # Forked owners can outlive their original launcher; kill adopted children.
    children = pathlib.Path(f"/proc/self/task/{os.getpid()}/children")
    for pid in children.read_text().split():
        try:
            os.kill(int(pid), signal.SIGKILL)
        except ProcessLookupError:
            pass
    until(reap_holders, "adopted holder cleanup", 3)
    print(
        "Tracked processes and adopted holders reaped",
        flush=True,
    )
