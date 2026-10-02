"""Import-safe private GNOME setup/capture for production Overlay acceptance.

Shell Eval is only fixture staging/observation/input. Production window creation
and painting are exclusively in the Go child. The supervisor owns process reaping.
"""
import ast
import json
import os
import pathlib
import select
import re
import shlex
import signal
import subprocess
import sys
import tempfile
import time


def until(check, label, timeout=10):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        if check():
            return
        time.sleep(.1)
    raise AssertionError("deadline: " + label)


def validate_receipt(response):
    r = response["receipt"] if "receipt" in response else {}
    assert r.get("status") == "submitted" and r.get("backend") == "x11", r
    assert r.get("frame", 0) > 0 and r.get("revision", 0) > 0, r
    assert len(r.get("scene", {}).get("Levels", [])) == 46, r
    return r


class Session:
    """Context manager; one private bus, real Shell, native GTK fixture.

    Consumers may use env, run, evaluate/value, capture, start_child, request.
    All launches join the supervisor's worker group; never run without supervise.py.
    """
    def __init__(self, width=1920, height=1080, monitors=None):
        root = pathlib.Path(os.environ.get("MAVOR_GNOME_ARTIFACTS", "/workspace/.yolo/durable/ghud/runs"))
        root.mkdir(parents=True, exist_ok=True)
        self.run = pathlib.Path(tempfile.mkdtemp(prefix="hud-", dir=root))
        print("Diagnostics:", self.run, flush=True)
        self.width, self.height = width, height
        self.monitors=monitors or [f"{width}x{height}"]
        self.processes, self.files = [], []
        self.env = os.environ.copy()
        for key in ("LD_LIBRARY_PATH", "DISPLAY", "XAUTHORITY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS", "DBUS_SYSTEM_BUS_ADDRESS", "SWAYSOCK", "GI_TYPELIB_PATH", "GIO_EXTRA_MODULES"):
            self.env.pop(key, None)
        for key, directory in (("HOME", "home"), ("XDG_RUNTIME_DIR", "r"), ("XDG_CONFIG_HOME", "config"), ("XDG_CACHE_HOME", "cache"), ("XDG_DATA_HOME", "data")):
            path = self.run / directory
            path.mkdir(mode=0o700)
            self.env[key] = str(path)
        self.env.update(XDG_SESSION_TYPE="wayland", XDG_CURRENT_DESKTOP="GNOME", GSETTINGS_BACKEND="memory", LIBGL_ALWAYS_SOFTWARE="1", GDK_BACKEND="wayland", GTK_A11Y="none", GSK_RENDERER="cairo", SHELL_BACKGROUND_IMAGE=str(pathlib.Path(__file__).resolve().parent.parent / "desktop/wallpaper.png"))
        self.sequence = 0
        self.focus_offset=0

    def launch(self, args, label, **kwargs):
        log = open(self.run / (label + ".log"), "w")
        self.files.append(log)
        child = subprocess.Popen(args, env=self.env, stdout=log, stderr=log, **kwargs)
        self.processes.append(child)
        with open(self.run / "processes.jsonl", "a") as record:
            record.write(json.dumps({"label": label, "pid": child.pid, "args": args}) + "\n")
        return child

    def call(self, args, timeout=5):
        result = subprocess.run(args, env=self.env, capture_output=True, text=True, timeout=timeout)
        assert result.returncode == 0, (args, result.stderr)
        return result.stdout

    def evaluate(self, js):
        result = self.call(["gdbus", "call", "--session", "--dest", "org.gnome.Shell", "--object-path", "/org/gnome/Shell", "--method", "org.gnome.Shell.Eval", js])
        with open(self.run / "eval.log", "a") as log:
            log.write(js + "\n" + result)
        assert result.startswith("(true,"), result
        return result

    def value(self, js):
        success, payload = ast.literal_eval(self.evaluate(js).strip().replace("(true,", "(True,", 1))
        assert success
        return json.loads(payload)

    def capture(self, name):
        path = self.run / (name + ".png")
        result = self.call(["gdbus", "call", "--session", "--dest", "org.gnome.Shell.Screenshot", "--object-path", "/org/gnome/Shell/Screenshot", "--method", "org.gnome.Shell.Screenshot.Screenshot", "false", "false", str(path)])
        assert result.startswith("(true,"), result
        assert path.stat().st_size > 1000
        return path

    def __enter__(self):
        try:
            self.start()
            return self
        except BaseException:
            self.close()
            raise

    def start(self):
        source = pathlib.Path(__file__).resolve().parent / "overlay_consumer.c"
        flags = shlex.split(self.call(["pkg-config", "--cflags", "--libs", "gtk4"]))
        self.call(["cc", str(source), "-o", str(self.run / "consumer"), *flags], 30)
        shell = os.environ.get("MAVOR_GNOME_SHELL", "gnome-shell")
        print(self.call([shell, "--version"]).strip(), flush=True)
        config = self.run / "bus.conf"
        config.write_text('<busconfig><type>session</type><listen>unix:dir=' + self.env["XDG_RUNTIME_DIR"] + '</listen><auth>EXTERNAL</auth><policy context="default"><allow send_destination="*"/><allow receive_sender="*"/><allow own="*"/></policy></busconfig>')
        address = self.run / "address"
        with open(address, "w") as out:
            self.launch(["dbus-daemon", "--config-file=" + str(config), "--nofork", "--print-address=" + str(out.fileno())], "bus", pass_fds=(out.fileno(),))
        until(lambda: bool(address.read_text().strip()), "bus")
        self.env.update(DBUS_SESSION_BUS_ADDRESS=address.read_text().strip(), DBUS_SYSTEM_BUS_ADDRESS=address.read_text().strip())
        self.shell = self.launch([shell, "--headless", "--wayland", *["--virtual-monitor="+m for m in self.monitors], "--wayland-display=mavor-hud", "--unsafe-mode"], "shell")
        until(lambda: (self.run / "r/mavor-hud").exists(), "Shell socket", 30)
        self.env["WAYLAND_DISPLAY"] = "mavor-hud"
        until(lambda: "GNOME Shell started" in (self.run / "shell.log").read_text(), "mature Shell", 30)
        time.sleep(3)
        display, auth = self.value('[imports.gi.GLib.getenv("DISPLAY"), imports.gi.GLib.getenv("XAUTHORITY")]')
        assert display and auth and pathlib.Path(auth).exists()
        assert pathlib.Path(auth).is_relative_to(self.run / "r")
        self.env.update(DISPLAY=display, XAUTHORITY=auth)
        self.evaluate("globalThis.hudKeyboard=global.stage.context.get_backend().get_default_seat().create_virtual_device(1);true")
        self.env["WAYLAND_DEBUG"]="1"
        self.consumer = self.launch([str(self.run / "consumer")], "consumer", stdin=subprocess.PIPE)
        until(lambda: "Mavor HUD native fixture" in self.evaluate('JSON.stringify(global.get_window_actors().map(a=>a.meta_window.title))'), "native fixture")
        self.evaluate('Main.overview.hide(); global.get_window_actors().find(a=>a.meta_window.title==="Mavor HUD native fixture").meta_window.activate(global.get_current_time()); true')
        until(lambda: "FOCUS\t1" in (self.run / "consumer.log").read_text(), "native focus")
        self.evaluate('globalThis.hudFocusEvents=[];global.display.connect("notify::focus-window",()=>hudFocusEvents.push(global.display.focus_window?.title??"NONE"));true')
        self.evaluate('globalThis.hudPointer=global.stage.context.get_backend().get_default_seat().create_virtual_device(0);true')
        for _ in range(4):
            self.evaluate('if (Main.messageTray._banner) Main.messageTray._hideNotification(true);true')
            time.sleep(.3)
        self.checkpoint_focus()

    def start_child(self, binary, label="child"):
        self.child_label=label
        log = open(self.run / (self.child_label+".log"), "w")
        self.files.append(log)
        env = self.env.copy()
        env["MAVOR_GNOME_OVERLAY_CHILD"] = "1"
        self.child = subprocess.Popen([binary, "-test.run=^TestOverlayChild$"], env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=log)
        self.processes.append(self.child)
        reply = self.request("ready", version=1)
        assert reply == {"version": 1, "backend": "x11", "status": "ready"}, reply

    def request(self, command, **kwargs):
        data = {"command": command, **kwargs}
        if command != "ready":
            self.sequence += 1
            data["sequence"] = self.sequence
        self.child.stdin.write((json.dumps(data) + "\n").encode())
        self.child.stdin.flush()
        readable, _, _ = select.select([self.child.stdout], [], [], 8)
        assert readable, "child response deadline: " + (self.run / (self.child_label+".log")).read_text()
        line = self.child.stdout.readline()
        assert line, "child exited: " + (self.run / (self.child_label+".log")).read_text()
        try:
            response = json.loads(line)
        except json.JSONDecodeError:
            raise AssertionError((line, self.child.stdout.read().decode(), (self.run / (self.child_label+".log")).read_text()))
        if command != "ready":
            assert response["sequence"] == self.sequence
        with open(self.run / "protocol.jsonl", "a") as log:
            log.write(json.dumps({"request": data, "response": response}) + "\n")
        return response

    def apply_frame(self, visual, level=None, text=None):
        self.request("apply", visual=visual, level=level, text=text)
        return validate_receipt(self.request("await_frame", timeout_ms=5000))



    def checkpoint_focus(self):
        # A fixture transition may deliberately change focus. Never exclude
        # backend updates: checkpoint only after fixture staging has settled.
        time.sleep(.3)
        self.evaluate("hudFocusEvents=[];true")
        self.focus_offset=(self.run/"consumer.log").stat().st_size

    def display_state(self):
        result=self.call(["gdbus","call","--session","--dest","org.gnome.Mutter.DisplayConfig","--object-path","/org/gnome/Mutter/DisplayConfig","--method","org.gnome.Mutter.DisplayConfig.GetCurrentState"])
        text=re.sub(r"\b(?:uint32|int32|uint64|int64)\s+", "", result)
        text=re.sub(r"@a\{sv\}\s*", "", text).replace("<", "").replace(">", "")
        text=re.sub(r"\btrue\b", "True", text);text=re.sub(r"\bfalse\b", "False", text)
        return ast.literal_eval(text)

    def configure_monitors(self, entries):
        state=self.display_state()
        configs=[]
        for index,x,y,scale,primary,transform in entries:
            monitor=state[1][index]
            mode=next(m[0] for m in monitor[1] if m[6].get("is-current")) if any(m[6].get("is-current") for m in monitor[1]) else monitor[1][0][0]
            configs.append(f"({x},{y},{scale},uint32 {transform},{str(primary).lower()},[('{monitor[0][0]}','{mode}',@a{{sv}} {{}})])")
        self.call(["gdbus","call","--session","--dest","org.gnome.Mutter.DisplayConfig","--object-path","/org/gnome/Mutter/DisplayConfig","--method","org.gnome.Mutter.DisplayConfig.ApplyMonitorsConfig",str(state[0]),"1","["+",".join(configs)+"]","{'layout-mode': <uint32 1>}"])
        time.sleep(5) # coalesced geometry poll + bounded rebuild retry
        self.assert_focus() # observe the WHOLE resize/rebuild interval

    def xwayland_pid(self):
        # Test fixture only: limit discovery to this private Shell's descendants.
        pending=[self.shell.pid]
        while pending:
            pid=pending.pop()
            try:
                pending.extend(int(p) for p in pathlib.Path(f"/proc/{pid}/task/{pid}/children").read_text().split())
                argv=pathlib.Path(f"/proc/{pid}/cmdline").read_bytes().split(b"\0")
                if argv and pathlib.Path(os.fsdecode(argv[0])).name=="Xwayland":
                    return pid
            except FileNotFoundError:
                pass
        raise AssertionError("private XWayland process absent")

    def assert_focus(self):
        assert self.value("hudFocusEvents") == [], "HUD moved Shell focus"
        assert "FOCUS\t" not in (self.run / "consumer.log").read_bytes()[self.focus_offset:].decode(), "HUD moved native focus"

    def click(self, x, y):
        before = (self.run / "consumer.log").read_text().count("CLICK\t")
        self.evaluate(f'hudPointer.notify_relative_motion(0,0,200);true')
        time.sleep(.3)
        self.evaluate(f'hudPointer.notify_absolute_motion(0,{x},{y});true')
        time.sleep(.2)
        print("ACTUAL POINTER",self.evaluate("JSON.stringify(global.get_pointer())"),flush=True)
        self.evaluate('hudPointer.notify_button(imports.gi.GLib.get_monotonic_time(),1,1);true')
        time.sleep(.1)
        self.evaluate('hudPointer.notify_button(imports.gi.GLib.get_monotonic_time(),1,0);true')
        until(lambda: (self.run / "consumer.log").read_text().count("CLICK\t") > before, "native click through HUD")

    def close(self):
        for child in reversed(self.processes):
            if child.poll() is None:
                child.terminate()
        for child in reversed(self.processes):
            try:
                child.wait(timeout=3)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=3)
        (self.run/"cleanup.json").write_text(json.dumps([{"pid":p.pid,"returncode":p.returncode} for p in self.processes],indent=2))
        for p in self.processes:
            for stream in (p.stdin,p.stdout,p.stderr):
                if stream is not None:stream.close()
        for log in self.files:
            log.close()
        print("Tracked processes stopped; supervisor reaps adopted children", flush=True)

    def __exit__(self, *unused):
        self.close()


def main(binary):
    with Session() as s:
        s.start_child(binary)
        receipts = {}
        receipts["baseline"] = s.apply_frame("hidden")
        time.sleep(.3)
        s.capture("baseline")
        for name, visual, text in [("recording", "recording", None), ("preview", "recording", "Real production preview: words stay in the HUD"), ("preview-long", "recording", "A long partial that fits its tail: " * 30), ("preview-cleared", "recording", ""), ("transcribing", "transcribing", None), ("error", "error", None), ("hidden", "hidden", None), ("recording-again", "recording", None)]:
            if visual == "recording":
                for i in range(50):
                    r = s.apply_frame(visual, .55, text)
                    time.sleep(.04)
            else:
                r = s.apply_frame(visual, text=text)
            time.sleep(.1)
            receipts[name] = validate_receipt(s.request("await_frame",timeout_ms=5000))
            s.assert_focus()
            s.capture(name)
        # Fullscreen/workspace transitions are fixture setup, not HUD activity.
        fixture='global.get_window_actors().find(a=>a.meta_window.title==="Mavor HUD native fixture").meta_window'
        s.evaluate(fixture+'.make_fullscreen();true');time.sleep(.5);s.checkpoint_focus()
        s.apply_frame("hidden");time.sleep(.2);s.capture("fullscreen-baseline")
        for _ in range(50):
            receipts["fullscreen"]=s.apply_frame("recording",.6,"Fullscreen passive HUD");time.sleep(.04)
        time.sleep(.1);receipts["fullscreen"]=validate_receipt(s.request("await_frame",timeout_ms=5000));s.assert_focus();s.capture("fullscreen")
        s.evaluate(fixture+'.unmake_fullscreen();Main.overview.show();true');time.sleep(.7);s.checkpoint_focus()
        s.apply_frame("transcribing");s.assert_focus();s.capture("overview-authoritative")
        s.evaluate('Main.overview.hide();'+fixture+'.activate(global.get_current_time());true');time.sleep(.7);s.checkpoint_focus()
        for _ in range(50):
            receipts["overview-exit"]=s.apply_frame("recording",.6,"");time.sleep(.04)
        time.sleep(.1);receipts["overview-exit"]=validate_receipt(s.request("await_frame",timeout_ms=5000));s.assert_focus();s.capture("overview-exit")
        s.evaluate('global.workspace_manager.get_workspace_by_index(1).activate(global.get_current_time());true');time.sleep(.5);s.checkpoint_focus()
        for _ in range(50):
            receipts["workspace-other"]=s.apply_frame("recording",.6,"Sticky, primary-monitor HUD");time.sleep(.04)
        time.sleep(.1);receipts["workspace-other"]=validate_receipt(s.request("await_frame",timeout_ms=5000));s.assert_focus();s.capture("workspace-other")
        s.evaluate('global.workspace_manager.get_workspace_by_index(0).activate(global.get_current_time());'+fixture+'.activate(global.get_current_time());true');time.sleep(.5);s.checkpoint_focus()
        # Put the native fixture under actual pill and preview pixels. Intentional
        # fixture moves are setup; focus observation remains active for HUD updates.
        s.evaluate('global.get_window_actors().find(a=>a.meta_window.title==="Mavor HUD native fixture").meta_window.move_resize_frame(false,510,32,900,500);true')
        s.apply_frame("recording", .5, "Pointer pass-through preview")
        time.sleep(.3)
        r = receipts["preview"]["screen"]
        x = (r["Min"]["X"] + r["Max"]["X"]) // 2
        s.capture("pointer-fixture")
        print("POINTER FRAME",s.evaluate("JSON.stringify(global.get_window_actors().map(a=>({title:a.meta_window.title,rect:a.meta_window.get_frame_rect()})))"),flush=True)
        s.click(x, r["Min"]["Y"] + 46)
        s.click(x, r["Min"]["Y"] + 75)
        s.apply_frame("hidden")
        time.sleep(.2)
        s.click(x, r["Min"]["Y"] + 46)
        s.assert_focus()
        s.consumer.stdin.write(b"state\n");s.consumer.stdin.flush()
        until(lambda:"STATE\tNative editor sentinel: preview never emits.\tEND" in (s.run / "consumer.log").read_text(), "unchanged native editor")
        # A live compositor and temporarily stopped X server allow an actual
        # same-session retry proof, unlike mandatory-XWayland process death.
        s.apply_frame("recording",.6,"Recovery before stall")
        stopped=s.xwayland_pid();os.kill(stopped,signal.SIGSTOP)
        try:
            s.request("apply",visual="recording",level=.7,text="Latest desired preview survives transport loss")
            time.sleep(6) # exceed the active five-second upload deadline
        finally:os.kill(stopped,signal.SIGCONT)
        time.sleep(3) # retry is every two seconds, using the same credentials
        recovered=s.apply_frame("recording",.7,None)
        assert recovered["scene"]["Preview"]=="Latest desired preview survives transport loss"
        assert "submission lost" in (s.run/"child.log").read_text()
        s.assert_focus();time.sleep(.3);s.capture("transport-recovered")
        (s.run/"recovery-receipt.json").write_text(json.dumps(recovered,indent=2))
        print("Actual same-session transport loss/retry preserves latest state PASS",flush=True)
        s.request("close")
        s.child.wait(timeout=5)
        assert s.child.returncode == 0
        s.assert_focus()
        # Existing owner's Close force-unblocks an in-flight checked X upload.
        s.sequence=0;s.start_child(binary,"close-stall-child")
        s.apply_frame("hidden")
        stopped=s.xwayland_pid();os.kill(stopped,signal.SIGSTOP)
        try:
            s.request("apply",visual="recording",level=.5)
            time.sleep(.1);start=time.monotonic();s.request("close")
            s.child.wait(timeout=5);assert s.child.returncode==0
            print("Frozen Close elapsed",round(time.monotonic()-start,3),flush=True)
            assert time.monotonic()-start<5
        finally:os.kill(stopped,signal.SIGCONT)
        print("Close force-unblocked frozen transport PASS",flush=True)
        # A stopped X server must not make constructor or Close unbounded.
        # Constructor cannot emit ready; its five-second transaction deadline
        # yields a real failure, followed by reaping, not Noop success.
        stopped=s.xwayland_pid();os.kill(stopped,signal.SIGSTOP)
        try:
            log=open(s.run/"stalled-child.log","w");s.files.append(log)
            env=s.env.copy();env["MAVOR_GNOME_OVERLAY_CHILD"]="1"
            child=subprocess.Popen([binary,"-test.run=^TestOverlayChild$"],env=env,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=log)
            s.processes.append(child);start=time.monotonic()
            child.stdin.write(b'{"command":"ready","version":1}\n');child.stdin.flush()
            child.wait(timeout=7)
            print("Frozen constructor elapsed",round(time.monotonic()-start,3),flush=True)
            assert child.returncode!=0 and time.monotonic()-start<6.5
            assert child.stdout.read()==b"", "stalled backend claimed readiness"
        finally:
            os.kill(stopped,signal.SIGCONT)
        # Authorization failure must never fall through to unauthenticated X.
        import struct,socket
        fake=s.run/"invalid-authority"
        fields=[socket.gethostname().encode(),b"",b"MIT-MAGIC-COOKIE-1",bytes(16)]
        fake.write_bytes(struct.pack(">H",256)+b"".join(struct.pack(">H",len(f))+f for f in fields))
        log=open(s.run/"unauthorized-child.log","w");s.files.append(log)
        env=s.env.copy();env["MAVOR_GNOME_OVERLAY_CHILD"]="1";env["XAUTHORITY"]=str(fake)
        child=subprocess.Popen([binary,"-test.run=^TestOverlayChild$"],env=env,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=log)
        s.processes.append(child);child.stdin.write(b'{"command":"ready","version":1}\n');child.stdin.flush();child.wait(timeout=7)
        assert child.returncode!=0 and child.stdout.read()==b""
        print("Wrong Xauthority rejected without readiness PASS",flush=True)
        # In this private launch Mutter marks XWayland mandatory; killing it
        # exits Shell too. Prove real compositor loss/retry/unavailable, not a
        # pretend recovery across a new session with stale credentials.
        s.sequence=0
        s.start_child(binary,"loss-child")
        s.apply_frame("recording",.6,"Loss keeps latest desired state")
        os.kill(s.xwayland_pid(),signal.SIGKILL)
        time.sleep(4)
        s.request("apply",visual="error")
        s.child.stdin.write((json.dumps({"command":"await_frame","sequence":s.sequence+1,"timeout_ms":5000})+"\n").encode());s.child.stdin.flush()
        s.child.wait(timeout=6)
        assert s.child.returncode!=0
        assert s.child.stdout.read()==b"", "lost backend produced fake frame"
        assert "connection lost" in (s.run/"loss-child.log").read_text()
        (s.run / "receipts.json").write_text(json.dumps(receipts, indent=2))
        print("Production states, zero focus changes, pill/preview/hidden pointer clicks and bounded close PASS", flush=True)



def geometry_main(binary):
    with Session(monitors=["1920x1080","1280x720"]) as s:
        s.start_child(binary)
        receipts={}
        for name,entries,expected_scale in [
            ("multiple-1x",None,1),
            ("primary-only-2x",[(0,0,0,2.0,True,0)],2),
            ("primary-only-fractional",[(0,0,0,1.25,True,0)],2),
            ("mixed-primary-changed",[(0,1280,0,1.25,False,0),(1,0,0,1.0,True,0)],2),
            ("rotated-primary",[(0,0,0,1.0,True,1)],1),
            ("restored-two",[(0,0,0,1.0,True,0),(1,1920,0,1.0,False,0)],1),
        ]:
            if entries is not None:s.configure_monitors(entries)
            r=s.apply_frame("recording",.6,"Actual production geometry: "+name)
            assert r["scale"]==expected_scale,r
            assert abs(r["screen"]["Max"]["X"]-r["screen"]["Min"]["X"]-r["canvas"]["X"]*r["scale"])<=1
            time.sleep(.3);s.assert_focus();s.capture(name);receipts[name]=r
            r["capture_stage_width"]=s.value("global.stage.width")
            print(name,"actual frame",r["frame"],"scale",r["scale"],"rect",r["screen"],flush=True)
        s.request("close");s.child.wait(timeout=5);assert s.child.returncode==0
        (s.run/"geometry-receipts.json").write_text(json.dumps(receipts,indent=2))
        print("Real monitor removal/addition, primary change, 2x, fractional/mixed and transform proof PASS",flush=True)

if __name__ == "__main__":
    signal.alarm(170)
    if len(sys.argv)>2 and sys.argv[2]=="geometry":geometry_main(sys.argv[1])
    else:main(sys.argv[1])
