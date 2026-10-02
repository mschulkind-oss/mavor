"""Real Shell captures only; shared Go catalog, validation and HTML are authoritative."""
import json
import pathlib
import sys
import time
from overlay_harness import Session, until


def main():
    binary, catalog, manifest = sys.argv[1:]
    scenes = json.loads(pathlib.Path(catalog).read_text())
    evidence = []
    names = ['hidden', 'recording', 'transcribing', 'error']
    with Session() as session:
        session.start_child(binary)
        for scene in scenes:
            visual = names[scene['Visual']]
            receipt = session.apply_frame(visual, scene['AudioLevel'], scene['Preview'])
            if scene['FeedFrames'] > 1:
                for _ in range(scene['FeedFrames'] - 1):
                    receipt = session.apply_frame(visual, scene['AudioLevel'], scene['Preview'])
                    time.sleep(.04)
            time.sleep(.1)
            receipt = session.request('await_frame', timeout_ms=5000)['receipt']
            path = session.capture(scene['ID'])
            session.assert_focus()
            log = session.run / 'consumer.log'
            previous = log.read_text().count('STATE\t')
            session.consumer.stdin.write(b'state\n');session.consumer.stdin.flush()
            until(lambda: log.read_text().count('STATE\t') > previous, 'fresh native editor readback')
            states = [line for line in log.read_text().splitlines() if line.startswith('STATE\t')]
            assert states[-1] == 'STATE\tNative editor sentinel: preview never emits.\tEND', 'editor changed'
            evidence.append(dict(id=scene['ID'], file=str(path), receipt=receipt))
        receipt = session.apply_frame('hidden', 0, '')
        time.sleep(.1)
        path = session.capture('hidden-after-preview')
        pathlib.Path(manifest + '.hidden.json').write_text(json.dumps(dict(id='hidden-after-preview',file=str(path),receipt=receipt)))
        session.assert_focus()
        session.request('close')
    pathlib.Path(manifest).write_text(json.dumps(evidence, indent=2))


if __name__ == '__main__':
    main()
