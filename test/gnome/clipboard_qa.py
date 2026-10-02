"""Escaped report and validation of actual Shell screenshot frames."""
import html
import json
import pathlib
import struct
import zlib


def inspect_png(path):
    data = pathlib.Path(path).read_bytes()
    assert data[:8] == b'\x89PNG\r\n\x1a\n', 'not a PNG screenshot'
    offset, compressed = 8, b''
    while offset < len(data):
        size = struct.unpack('>I', data[offset:offset+4])[0]
        kind = data[offset+4:offset+8]
        payload = data[offset+8:offset+8+size]
        if kind == b'IHDR':
            width, height, depth, color, _, _, interlace = struct.unpack('>IIBBBBB', payload)
            assert depth == 8 and color in (2, 6) and interlace == 0
        if kind == b'IDAT':
            compressed += payload
        offset += size + 12
    channels = 3 if color == 2 else 4
    raw = zlib.decompress(compressed)
    stride = width * channels
    previous = bytearray(stride)
    bright = teal = blue = 0
    for y in range(height):
        pos = y * (stride + 1)
        mode = raw[pos]
        row = bytearray(raw[pos+1:pos+1+stride])
        for i in range(stride):
            a = row[i-channels] if i >= channels else 0
            b = previous[i]
            c = previous[i-channels] if i >= channels else 0
            if mode == 1:
                v = a
            elif mode == 2:
                v = b
            elif mode == 3:
                v = (a+b)//2
            elif mode == 4:
                p = a+b-c
                distances = (abs(p-a), abs(p-b), abs(p-c))
                v = (a, b, c)[distances.index(min(distances))]
            else:
                assert mode == 0
                v = 0
            row[i] = (row[i]+v) & 255
        for x in range(0, stride, channels):
            r, g, b = row[x:x+3]
            bright += min(r, g, b) > 180
            teal += g > r*1.4 and g > b*1.15 and g > 30
            blue += b > r*1.5 and b > g*1.1 and b > 30
        previous = row
    assert width == 1280 and height == 720, (width, height)
    # Nonuniform colored wallpaper and the light native editor must both be
    # present, even in Shell's dimmed overview. Not a file-existence check.
    assert teal > 5000 and blue > 5000 and bright > 10000, (teal, blue, bright)
    return {'width': width, 'height': height, 'teal_pixels': teal,
            'blue_pixels': blue, 'bright_pixels': bright}


def capture(call, path):
    path = pathlib.Path(path).resolve()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.unlink(missing_ok=True)
    result = call(['gdbus', 'call', '--session', '--dest', 'org.gnome.Shell.Screenshot',
                   '--object-path', '/org/gnome/Shell/Screenshot', '--method',
                   'org.gnome.Shell.Screenshot.Screenshot', 'false', 'false', str(path)])
    assert result.startswith('(true,'), 'Shell screenshot failed: ' + result
    return inspect_png(path)


def report(path, scenes):
    escape = lambda value: html.escape(str(value), quote=True)
    body = ''.join('<section id="{id}"><h2>{caption}</h2><p>Editor text: <code>{text}</code></p>'
                   '<a href="{image}"><img src="{image}" width="{width}" height="{height}" '
                   'alt="{caption}"></a></section>'.format(**{k: escape(v) for k, v in s.items()})
                   for s in scenes)
    pathlib.Path(path).write_text('''<!doctype html><html lang="en"><meta charset="utf-8">
<title>mavor GNOME clipboard QA</title><style>body{background:#101c30;color:#edf4ff;font:18px sans-serif;
max-width:1280px;margin:32px auto;padding:20px}img{width:100%;height:auto}section{margin:48px 0}
a{color:#82dacc}code{white-space:pre-wrap}</style><h1>GNOME clipboard QA</h1>
<p>Actual private GNOME Shell / Mutter screenshots. Local fixture transcripts, not recognition.
Production X11 clipboard copy preserves focus. Manual Paste is a test-driven GTK clipboard.paste action,
not physical Ctrl+V or automatic typing. The test clears the editor before each Paste
to show replacement text; production copy never edits it. No mavor HUD, notifications, or model inference.</p>
<a href="ui-storybook.html">Sway overlay storybook</a>''' + body + '</html>')
    pathlib.Path(path).with_suffix('.json').write_text(json.dumps(scenes, indent=2, ensure_ascii=False))
