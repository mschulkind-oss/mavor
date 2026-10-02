"""Controlled loader release; real daemon, IPC, CPU inference and compositor."""
import json
import pathlib
import sys
import time
from overlay_harness import Session


def main():
    binary, manifest = sys.argv[1:]
    evidence = []
    with Session() as session:
        session.start_child(binary)
        def capture(name):
            receipt = session.request('await_frame', timeout_ms=5000)['receipt']
            time.sleep(.15)
            path = session.capture(name)
            session.assert_focus()
            evidence.append(dict(id=name, file=str(path), receipt=receipt))
        session.apply_frame('hidden')
        time.sleep(.15)
        capture('baseline')
        for outcome in ('ready', 'error'):
            session.request('daemon_start', visual=outcome)
            capture('quiet-' + outcome)
            assert evidence[-1]['receipt']['scene']['Visual'] == 0
            session.request('daemon_request')
            capture('initializing-' + outcome)
            scene = evidence[-1]['receipt']['scene']
            assert scene['Visual'] == 4 and 'cannot record' in scene['Preview'], scene
            assert 'Press again' in scene['Preview'], scene
            result = session.request('daemon_release')
            assert result['state'] == ('idle' if outcome == 'ready' else 'failed'), result
            capture(outcome)
            if outcome == 'error':
                assert result['error'], result
                assert evidence[-1]['receipt']['scene']['Preview'] == result['error']
            session.request('daemon_stop')
        result = session.request('backup_run')['evidence']
        assert result['outputs'] == result['history'] == 1, result
        assert result['source'] == 'companion-backup' and result['cycle'] == 1, result
        capture('degraded-backup')
        assert evidence[-1]['receipt']['scene']['Visual'] == 5
        assert evidence[-1]['receipt']['scene']['Preview'] == result['warning']
        print('Controlled backup evidence:', json.dumps(result), flush=True)
        assert session.request('backup_stop')['child_reaped']
        session.request('close')
    pathlib.Path(manifest).write_text(json.dumps(evidence, indent=2))


if __name__ == '__main__':
    main()
