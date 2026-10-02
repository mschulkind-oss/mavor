"""HUD capture staging must be import-safe; clipboard pictures are a separate QA module."""
import importlib
import unittest
from unittest.mock import patch


class HUDCaptureContract(unittest.TestCase):
    def test_import_starts_nothing(self):
        with patch('subprocess.Popen', side_effect=AssertionError('import launched a process')):
            module = importlib.import_module('hud_storybook_harness')
            importlib.reload(module)

    def test_capture_driver_does_not_render_html_or_pixels(self):
        import inspect
        import hud_storybook_harness
        source = inspect.getsource(hud_storybook_harness)
        self.assertIn('session.start_child(binary)', source)
        self.assertIn('session.capture', source)
        self.assertNotIn('<html', source)
        self.assertNotIn('Render(', source)
