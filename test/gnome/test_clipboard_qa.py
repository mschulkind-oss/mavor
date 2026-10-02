"""Report and genuine-frame evidence regressions, without a desktop."""
import pathlib
import tempfile
import unittest
from clipboard_qa import capture, report, inspect_png


class StorybookTests(unittest.TestCase):
    def test_capture_failure(self):
        with tempfile.TemporaryDirectory() as d:
            path = pathlib.Path(d) / 'missing.png'
            with self.assertRaises(AssertionError):
                capture(lambda args: "(false, '')", path)
            with self.assertRaises(FileNotFoundError):
                capture(lambda args: "(true, 'missing.png')", path)

    def test_invalid_frame(self):
        with tempfile.TemporaryDirectory() as d:
            p = pathlib.Path(d) / 'bad.png'
            p.write_bytes(b'not PNG')
            with self.assertRaises(AssertionError):
                inspect_png(p)

    def test_wallpaper_without_window_rejected(self):
        with self.assertRaises(AssertionError):
            inspect_png(pathlib.Path(__file__).parent / '../desktop/wallpaper.png')

    def test_report_escaping_and_paths(self):
        with tempfile.TemporaryDirectory() as d:
            p = pathlib.Path(d) / 'report.html'
            report(p, [{'id': '01-ready', 'caption': '<script>&',
                        'text': 'fixture α', 'image': 'gnome-screenshots/01-ready.png',
                        'width': 1280, 'height': 720}])
            html = p.read_text()
            self.assertIn('&lt;script&gt;&amp;', html)
            self.assertNotIn('<script>', html)
            self.assertIn('gnome-screenshots/01-ready.png', html)
            self.assertIn('fixture α', html)
            self.assertIn('ui-storybook.html', html)
            self.assertIn('No mavor HUD', html)
            self.assertIn('clears the editor before each Paste', html)
            import json
            metadata = json.loads(p.with_suffix('.json').read_text())
            self.assertEqual(metadata[0]['id'], '01-ready')
            self.assertEqual(metadata[0]['width'], 1280)
            self.assertEqual(metadata[0]['text'], 'fixture α')
