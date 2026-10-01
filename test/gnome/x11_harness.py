"""Real isolated GNOME acceptance. Every unmet prerequisite is a failure.

Shell Eval sets/observes test focus only; it never reads/writes selections,
focuses wl-copy, injects keys, or approves portals.
"""

import ast
import json
import ctypes
import os
import pathlib
import shlex
import signal
import subprocess
import sys
import shutil
import tempfile
import time

# Adopt forked wl-copy holders so cleanup can reap them, not just signal them.
if ctypes.CDLL(None, use_errno=True).prctl(36, 1, 0, 0, 0) != 0:
    raise OSError(ctypes.get_errno(), "PR_SET_CHILD_SUBREAPER")


def interrupted(signum, frame):
    raise RuntimeError("harness deadline or termination: " + str(signum))


signal.signal(signal.SIGTERM, interrupted)
signal.signal(signal.SIGALRM, interrupted)
signal.alarm(100)

storybook_mode = sys.argv[2] == "storybook"

TITLE = "Mavor native clipboard consumer"
default_root = (
    (os.environ["YOLO_DURABLE_DIR"] + "/gnome-headless/tests")
    if "YOLO_DURABLE_DIR" in os.environ
    else (
        os.environ.get("XDG_CACHE_HOME", str(pathlib.Path.home() / ".cache"))
        + "/mavor-gnome-tests"
    )
)
if storybook_mode and "YOLO_DURABLE_DIR" in os.environ:
    default_root = os.environ["YOLO_DURABLE_DIR"] + "/storybooks/g"
root = pathlib.Path(os.environ.get("MAVOR_GNOME_ARTIFACTS", default_root)).resolve()
root.mkdir(parents=True, exist_ok=True)
run = pathlib.Path(tempfile.mkdtemp(prefix=sys.argv[2] + "-", dir=root))
print("Diagnostics:", run, flush=True)
env = os.environ.copy()
for key in (
    "LD_LIBRARY_PATH",
    "XAUTHORITY",
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
if storybook_mode:
    env["MAVOR_STORYBOOK"] = "1"
    env["GSK_RENDERER"] = "cairo"
    env["SHELL_BACKGROUND_IMAGE"] = str((pathlib.Path(__file__).parent / "../desktop/wallpaper.png").resolve())
processes = []
files = []


def launch(args, label, **kwargs):
    log = open(run / (label + ".log"), "w")
    files.append(log)
    child = subprocess.Popen(
        args, env=env, stdout=log, stderr=log, **kwargs
    )
    processes.append(child)
    with open(run / "processes.jsonl", "a") as record:
        record.write(json.dumps({"label": label, "pid": child.pid, "args": args, "monotonic": time.monotonic()}) + "\n")
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


def eval_value(js):
    raw = evaluate(js).strip()
    success, payload = ast.literal_eval(raw.replace("(true,", "(True,", 1))
    assert success
    return json.loads(payload)


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


try:
    # Build the consumer with real GTK; dependency absence is not a skip.
    flags = shlex.split(call(["pkg-config", "--cflags", "--libs", "gtk4"]))
    call(["cc", str(pathlib.Path(__file__).parent / "client/consumer.c"), "-o", str(run / "consumer"), *flags], 30)
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
    until(lambda: "GNOME Shell started" in (run / "shell.log").read_text(), "mature Shell", 30)
    time.sleep(3)
    display, auth = eval_value('[imports.gi.GLib.getenv("DISPLAY"), imports.gi.GLib.getenv("XAUTHORITY")]')
    assert display and auth and pathlib.Path(auth).exists(), (display, auth)
    assert pathlib.Path(auth).is_relative_to(run / "runtime"), "XAUTHORITY escaped private runtime"
    env.update(DISPLAY=display, XAUTHORITY=auth)
    (run / "x11-environment.json").write_text(json.dumps({"DISPLAY": display, "XAUTHORITY": auth}))
    print("Private X11:", display, auth, flush=True)
    version = subprocess.run(["xclip", "-version"], env=env, capture_output=True, text=True, check=True)
    print(version.stdout + version.stderr, flush=True)
    (run / "xclip-version.txt").write_text(shutil.which("xclip") + "\n" + version.stdout + version.stderr)
    initial_events = (run / "shell.log").read_text()
    owner_title = "Mavor PRIMARY owner"
    if not storybook_mode:
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
    consumer = launch([str(run / "consumer"), TITLE], "consumer", stdin=subprocess.PIPE)
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
    def focus_barrier():
        marker = "XCLIP_BARRIER_" + str(time.monotonic_ns())
        evaluate('console.log("' + marker + '"); true')
        until(lambda: marker in (run / "shell.log").read_text(), "focus-log barrier")
        time.sleep(.2)

    env["MAVOR_GNOME_CHILD"] = "1"
    dispatcher = launch([sys.argv[1], "-test.run=^TestX11DispatcherChild$", "-test.v"], "dispatcher", stdin=subprocess.PIPE)

    class Owner:
        def __init__(self, pid):
            self.pid = pid
        def poll(self):
            return None if pathlib.Path(f"/proc/{self.pid}").exists() else 0
        @property
        def returncode(self):
            return self.poll()
        def terminate(self):
            dispatcher.stdin.write(b"CLOSE\n")
            dispatcher.stdin.flush()
        def wait(self, timeout):
            dispatcher.wait(timeout=timeout)
            assert dispatcher.returncode == 0, "production Close failed"
            assert self.poll() == 0, "production owner not reaped"

    def set_clipboard(text, label):
        offset = (run / "dispatcher.log").stat().st_size
        dispatcher.stdin.write((text + "\n").encode())
        dispatcher.stdin.flush()
        until(lambda: "EMITTED\t" + text in (run / "dispatcher.log").read_bytes()[offset:].decode(), "production Emit", 5)
        children = []
        for task in pathlib.Path(f"/proc/{dispatcher.pid}/task").iterdir():
            try:
                children.extend((task / "children").read_text().split())
            except FileNotFoundError:
                pass
        assert len(children) == 1, children
        child = Owner(int(children[0]))
        with open(run / "processes.jsonl", "a") as record:
            record.write(json.dumps({"label": label, "pid": child.pid, "production_owner": True}) + "\n")
        time.sleep(.5)
        assert child.poll() is None, "foreground production owner died"
        print("Production owner", label, child.pid, "alive at", time.monotonic(), flush=True)
        return child

    # Focus is monitored by both native active-state events and every Shell
    # focus-window change; an event counter avoids final-state-only assertions.
    evaluate('globalThis.xclipFocusEvents = []; global.display.connect("notify::focus-window", () => xclipFocusEvents.push(global.display.focus_window?.title ?? "NONE")); true')
    if storybook_mode:
        from storybook import capture, report
        reports = (pathlib.Path(__file__).parent / "../reports").resolve()
        scenes = []
        # Dismiss Shell's own private-session privileged-user warning through
        # its native banner dismissal; never synthesize a mavor notification.
        for _ in range(4):
            evaluate('if (Main.messageTray._banner) Main.messageTray._hideNotification(true); true')
            time.sleep(.4)
        def scene(scene_id, caption, text):
            consumer_command("state", "STATE\t" + text + "\tEND")
            time.sleep(.5)
            geometry = eval_value('global.get_window_actors().filter(a=>a.meta_window.title === "' + TITLE + '").map(a=>{let r=a.meta_window.get_frame_rect(); return [r.x,r.y,r.width,r.height]})')
            assert len(geometry) == 1 and geometry[0][2] > 500 and geometry[0][3] > 250, geometry
            image = "gnome-screenshots/" + scene_id + ".png"
            evidence = capture(call, reports / image)
            scenes.append(dict(id=scene_id, caption=caption, text=text, image=image,
                               geometry=geometry[0], **evidence))
            (run / "captures.json").write_text(json.dumps(scenes, indent=2, ensure_ascii=False))
        def copy_without_edit(text, label):
            offset = (run / "consumer.log").stat().st_size
            evaluate('xclipFocusEvents.length = 0; true')
            child = set_clipboard(text, label)
            focus_barrier()
            assert eval_value("xclipFocusEvents") == [], "copy changed Shell focus"
            events = (run / "consumer.log").read_bytes()[offset:].decode()
            assert "ACTION\t" not in events and "FOCUS\t0" not in events, events
            return child
        ready = "Session notes: ready for a manual Paste."
        first = "Plan the afternoon walk. Bring a notebook."
        replacement = "Remember to review the session notes tomorrow."
        overview_text = "Back from overview: paste when the editor is ready."
        scene("01-ready", "Native editor ready — before copying", ready)
        previous = copy_without_edit(first, "storybook-first")
        scene("02-copy-only", "Production clipboard copy — editor unchanged", ready)
        consumer_command("action-paste", "ACTION\t" + first)
        scene("03-manual-paste", "Explicit test-driven manual Paste — fixture transcript", first)
        child = copy_without_edit(replacement, "storybook-replacement")
        until(lambda: previous.poll() is not None, "replacement owner exit")
        scene("04-replacement-copy", "Replacement copy — existing text unchanged", first)
        consumer_command("action-paste", "ACTION\t" + replacement)
        scene("05-replacement-paste", "Another explicit manual Paste — replacement fixture", replacement)
        evaluate('Main.overview.show(); true')
        time.sleep(1)
        evaluate('global.display.unset_input_focus(global.get_current_time()); true')
        until(lambda: eval_value('Main.overview.visible && !Main.overview.animationInProgress && !global.display.focus_window'), "mature overview")
        copy_without_edit(overview_text, "storybook-overview")
        assert eval_value('Main.overview.visible && !global.display.focus_window')
        until(lambda: child.poll() is not None, "overview replaced owner exit")
        scene("06-overview", "Mature GNOME overview — clipboard copy does not displace it", replacement)
        evaluate('Main.overview.hide(); global.get_window_actors().find(a=>a.meta_window.title === "' + TITLE + '").meta_window.activate(global.get_current_time()); true')
        until(lambda: eval_value('global.display.focus_window?.title === "' + TITLE + '" && !Main.overview.animationInProgress'), "editor returned")
        consumer_command("action-paste", "ACTION\t" + overview_text)
        scene("07-return-paste", "Return to editor — explicit manual Paste of overview fixture", overview_text)
        report(reports / "gnome-storybook.html", scenes)
        print("GNOME STORYBOOK PASS", flush=True)
        sys.exit(0)

    owners = []
    for index, text in enumerate(("first transcript α", "replacement transcript β", "third transcript γ")):
        offset = (run / "consumer.log").stat().st_size
        evaluate('xclipFocusEvents.length = 0; true')
        previous = owners[-1] if owners else None
        child = set_clipboard(text, "xclip-" + str(index))
        owners.append(child)
        time.sleep(2)
        for repeat in range(3):
            consumer_command("paste-" + str(repeat), "paste-" + str(repeat) + "\t" + text + "\tOK")
        consumer_command("primary", "primary\tprimary sentinel α\tOK")
        consumer_command("action-paste", "ACTION\t" + text)
        focus_barrier()
        assert "FOCUS\t0" not in (run / "consumer.log").read_bytes()[offset:].decode(), "xclip stole native destination focus"
        assert eval_value("xclipFocusEvents") == [], "Shell focus changed while copying"
        assert evaluate('global.display.focus_window?.title === "' + TITLE + '"').strip().endswith(", 'true')"), "destination not focused"
        if previous:
            until(lambda: previous.poll() is not None, "replaced owner exit")
            print("Replaced owner", previous.pid, "exit", previous.returncode, flush=True)
        print("Focused delayed/repeated/replacement PASS", index, flush=True)

    # Mature-session overview: no destination or X11 helper is focused.
    evaluate('Main.overview.show(); true')
    time.sleep(1)
    print("Overview setup state:", eval_value('[Main.overview.visible, Main.overview.animationInProgress, global.display.focus_window?.title ?? null]'), flush=True)
    # Opening overview grabs stage input but can retain a focus_window. Explicitly
    # clear test destination focus, never focus an ownership helper.
    evaluate('global.display.unset_input_focus(global.get_current_time()); true')
    until(lambda: evaluate('Main.overview.visible && !Main.overview.animationInProgress && !global.display.focus_window').strip().endswith(", 'true')"), "mature overview without focus")
    evaluate('xclipFocusEvents.length = 0; true')
    previous = owners[-1]
    overview_owner = set_clipboard("overview transcript δ", "xclip-overview")
    time.sleep(3)
    focus_barrier()
    assert eval_value("xclipFocusEvents") == [], "overview copy transiently focused a window"
    assert evaluate('Main.overview.visible && !global.display.focus_window').strip().endswith(", 'true')"), "copy displaced overview"
    until(lambda: previous.poll() is not None, "overview replaced owner exit")
    evaluate('Main.overview.hide(); global.get_window_actors().find(a=>a.meta_window.title === "' + TITLE + '").meta_window.activate(global.get_current_time()); true')
    until(lambda: evaluate('global.display.focus_window?.title === "' + TITLE + '" && !Main.overview.animationInProgress').strip().endswith(", 'true')"), "destination returned")
    time.sleep(.3)
    for repeat in range(3):
        consumer_command("overview-paste-" + str(repeat), "overview-paste-" + str(repeat) + "\toverview transcript δ\tOK")
    consumer_command("primary", "primary\tprimary sentinel α\tOK")
    print("Mature overview/no-focus transfer PASS", flush=True)
    time.sleep(10)
    consumer_command("long-delayed", "long-delayed\toverview transcript δ\tOK")
    consumer_command("primary", "primary\tprimary sentinel α\tOK")
    assert overview_owner.poll() is None, "owner failed during delayed consumption"
    print("Owner remained alive through 10-second delayed read", overview_owner.pid, flush=True)
    overview_owner.terminate()
    overview_owner.wait(timeout=3)
    time.sleep(1)
    consumer_command("after-owner-death", "after-owner-death\toverview transcript δ\tOK")
    time.sleep(5)
    consumer_command("after-owner-death-delayed", "after-owner-death-delayed\toverview transcript δ\tOK")
    consumer_command("primary", "primary\tprimary sentinel α\tOK")
    print("Native readback retained 1 and 6 seconds after owner termination (bounded observation)", flush=True)
    # Real X11 startup cannot finish against a frozen private compositor.
    xservers = []
    for task in pathlib.Path(f"/proc/{shell_process.pid}/task").iterdir():
        for pid in (task / "children").read_text().split():
            cmdline = pathlib.Path(f"/proc/{pid}/cmdline").read_bytes()
            if b"Xwayland" in cmdline:
                xservers.append(int(pid))
    assert len(xservers) == 1, ("private XWayland child", xservers)
    os.kill(xservers[0], signal.SIGSTOP)
    try:
        deadline_child = launch([sys.argv[1], "-test.run=^TestX11DeadlineChild$", "-test.v"], "deadline")
        until(lambda: deadline_child.poll() is not None, "production launch deadline", 8)
        assert deadline_child.returncode == 0, (run / "deadline.log").read_text()
        assert "earlier cancellation PASS" in (run / "deadline.log").read_text()
        print("Frozen-compositor launch deadline and earlier cancellation PASS", flush=True)
    finally:
        os.kill(xservers[0], signal.SIGCONT)

    # Abrupt production process death kills its foreground owner, too.
    dispatcher = launch([sys.argv[1], "-test.run=^TestX11DispatcherChild$", "-test.v"], "dispatcher-crash", stdin=subprocess.PIPE)
    dispatcher.stdin.write(b"crash transcript\n")
    dispatcher.stdin.flush()
    until(lambda: "EMITTED" in (run / "dispatcher-crash.log").read_text(), "crash owner started")
    crash_children = []
    for task in pathlib.Path(f"/proc/{dispatcher.pid}/task").iterdir():
        crash_children.extend((task / "children").read_text().split())
    assert len(crash_children) == 1, crash_children
    crash_pid = int(crash_children[0])
    with open(run / "processes.jsonl", "a") as record:
        record.write(json.dumps({"label": "crash-owner", "pid": crash_pid, "production_owner": True}) + "\n")
    dispatcher.kill()
    dispatcher.wait(timeout=3)
    def crash_owner_reaped():
        pid, status = os.waitpid(crash_pid, os.WNOHANG)
        if not pid:
            return False
        assert os.WIFSIGNALED(status) and os.WTERMSIG(status) == signal.SIGKILL, status
        return True
    until(crash_owner_reaped, "parent-death owner cleanup", 5)
    assert not pathlib.Path(f"/proc/{crash_pid}").exists()
    print("Abrupt production process death kills owner; test subreaper reaped it PASS", flush=True)

    # A lost compositor ends ownership; do not silently relaunch or inject.
    dispatcher = launch([sys.argv[1], "-test.run=^TestX11DispatcherChild$", "-test.v"], "dispatcher-loss", stdin=subprocess.PIPE)
    # set_clipboard uses dispatcher.log; use its own acknowledgment here.
    dispatcher.stdin.write(b"connection-loss transcript\n")
    dispatcher.stdin.flush()
    until(lambda: "EMITTED" in (run / "dispatcher-loss.log").read_text(), "loss owner started")
    for task in pathlib.Path(f"/proc/{dispatcher.pid}/task").iterdir():
        for pid in (task / "children").read_text().split():
            with open(run / "processes.jsonl", "a") as record:
                record.write(json.dumps({"label": "loss-owner", "pid": int(pid), "production_owner": True}) + "\n")
    shell_process.terminate()
    until(lambda: shell_process.poll() is not None, "private compositor shutdown", 10)
    def no_owner_children():
        return all(not (task / "children").read_text().strip() for task in pathlib.Path(f"/proc/{dispatcher.pid}/task").iterdir())
    until(no_owner_children, "connection-loss owner reaped", 5)
    dispatcher.stdin.write(b"CLOSE\n")
    dispatcher.stdin.flush()
    until(lambda: dispatcher.poll() is not None, "connection-loss dispatcher close", 5)
    assert dispatcher.returncode == 0, (run / "dispatcher-loss.log").read_text()
    print("Compositor-loss owner exit/reap PASS", flush=True)
    print("PRODUCTION X11 DISPATCHER PASS (including editable GTK Paste action)", flush=True)

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
