import unittest
import overlay_harness as hud
import coexistence_harness as coexistence

class HarnessTests(unittest.TestCase):
    def test_import_does_not_start_session(self):
        self.assertTrue(callable(hud.Session))
        self.assertTrue(callable(coexistence.main))

    def test_protocol_rejects_fake_receipt(self):
        for bad in [{}, {"receipt": {"status": "presented"}}, {"receipt": {"status": "submitted", "backend": "noop"}}]:
            with self.assertRaises(AssertionError):
                hud.validate_receipt(bad)

    def test_receipt_requires_waveform(self):
        with self.assertRaises(AssertionError):
            hud.validate_receipt({"receipt": {"status": "submitted", "backend": "x11", "revision": 2, "frame": 1, "scene": {"Visual": 1, "Levels": []}}})
