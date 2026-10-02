"""Actual production HUD and X11 dispatcher in one supervised private session."""
import json
import pathlib
import sys
import subprocess
import time
from overlay_harness import Session, until

PREVIEW = 'PREVIEW ONLY — NEVER DISPATCHED 7f412'
EDITOR = 'Native editor sentinel: preview never emits.'


def main(binary):
    with Session() as s:
        def native(command, expected):
            log = s.run / 'consumer.log'
            offset = log.stat().st_size
            s.consumer.stdin.write((command + '\n').encode()); s.consumer.stdin.flush()
            until(lambda: expected in log.read_bytes()[offset:].decode(), 'native ' + command)
            return log.read_bytes()[offset:].decode()

        native('select', 'SELECTED')
        selection = native('inspect', 'SELECTION\t').split('SELECTION\t')[1].splitlines()[0]
        assert selection.startswith('1\t'), selection
        s.checkpoint_focus() # declared fixture staging ends before either component
        s.start_child(binary)
        # Session.launch already gives this child the private env; restore flag afterward.
        s.env['MAVOR_GNOME_CHILD'] = '1'
        dispatcher = s.launch([binary, '-test.run=^TestX11DispatcherChild$'], 'dispatcher', stdin=subprocess.PIPE)
        s.env.pop('MAVOR_GNOME_CHILD')

        def emit(text):
            log = s.run / 'dispatcher.log'; offset = log.stat().st_size
            dispatcher.stdin.write((text + '\n').encode()); dispatcher.stdin.flush()
            until(lambda: 'EMITTED\t' + text in log.read_bytes()[offset:].decode(), 'production Emit')
            pids = set()
            for task in pathlib.Path(f'/proc/{dispatcher.pid}/task').iterdir():
                try: pids.update(int(p) for p in (task / 'children').read_text().split())
                except FileNotFoundError: pass
            assert len(pids) == 1, pids
            pid = pids.pop()
            with open(s.run / 'processes.jsonl', 'a') as log:
                log.write(json.dumps(dict(label='clipboard-owner', pid=pid)) + '\n')
            assert pathlib.Path(f'/proc/{pid}').exists()
            return pid

        def unchanged(text=EDITOR, selected=selection):
            s.assert_focus()
            assert 'ACTION\t' not in (s.run / 'consumer.log').read_text(), 'editor changed before explicit Paste'
            result = native('inspect', 'SELECTION\t')
            assert 'STATE\t' + text + '\tEND' in result, result
            assert result.split('SELECTION\t')[1].splitlines()[0] == selected, result
            native('primary', 'primary\t' + EDITOR + '\tOK')

        receipts = {}
        def capture(name, visual):
            # Same steady-frame schedule as the shared catalog; no animation freeze.
            count = 50 if visual == 'recording' else 1
            for _ in range(count):
                r = s.apply_frame(visual, .55, PREVIEW if visual == 'recording' else '')
                time.sleep(.04)
            time.sleep(.1)
            r = s.request('await_frame', timeout_ms=5000)['receipt']
            s.capture(name); receipts[name] = r
            unchanged()

        capture('baseline', 'hidden')
        capture('mapped-before-copy', 'recording')
        first = emit('Final transcript coexistence one α')
        native('clipboard', 'clipboard\tFinal transcript coexistence one α\tOK')
        capture('mapped-with-copy', 'recording')
        capture('updated-with-copy', 'transcribing')
        second = emit('Replacement final transcript coexistence two β')
        until(lambda: not pathlib.Path(f'/proc/{first}').exists(), 'replaced owner reaped')
        native('clipboard', 'clipboard\tReplacement final transcript coexistence two β\tOK')
        capture('replacement-error', 'error')
        capture('replacement-preview', 'recording')
        native('paste', 'ACTION\tReplacement final transcript coexistence two β') # explicit fixture Paste
        native('state', 'STATE\tReplacement final transcript coexistence two β\tEND')
        s.assert_focus()
        # Preview sentinel must never become native text or clipboard content.
        native('clipboard', 'clipboard\tReplacement final transcript coexistence two β\tOK')
        s.apply_frame('hidden'); time.sleep(.2)
        receipts['hidden-after-paste'] = s.request('await_frame', timeout_ms=5000)['receipt']
        s.capture('hidden-after-paste')
        s.request('close'); s.child.wait(timeout=5); assert s.child.returncode == 0
        native('clipboard', 'clipboard\tReplacement final transcript coexistence two β\tOK')
        s.assert_focus()
        dispatcher.stdin.write(b'CLOSE\n'); dispatcher.stdin.flush()
        dispatcher.wait(timeout=5); assert dispatcher.returncode == 0
        assert not pathlib.Path(f'/proc/{second}').exists(), 'Close failed to reap clipboard owner'
        # Recreate backend after both components closed; native text stays final-only.
        s.sequence = 0; s.start_child(binary, 'replacement-hud')
        s.apply_frame('error'); time.sleep(.1)
        receipts['remapped-after-close'] = s.request('await_frame', timeout_ms=5000)['receipt']
        s.capture('remapped-after-close')
        s.request('close'); s.child.wait(timeout=5); assert s.child.returncode == 0
        native('state', 'STATE\tReplacement final transcript coexistence two β\tEND'); s.assert_focus()
        (s.run / 'coexistence-receipts.json').write_text(json.dumps(receipts, indent=2))
        print('Production HUD + X11 clipboard coexistence, focus/selection/text, explicit GTK Paste, replacement and Close/reaping PASS', flush=True)


if __name__ == '__main__':
    main(sys.argv[1])
