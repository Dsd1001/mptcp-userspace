"""Verify the status recorder emits useful diagnostics without exposing secrets."""
import importlib.util
import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
MODULE = ROOT / "scripts/capture-landing-telemetry.py"


class LandingTelemetryTests(unittest.TestCase):
    def test_sanitization_and_fields(self):
        spec = importlib.util.spec_from_file_location("landing_telemetry", MODULE)
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
        status = {
            "version": "1.1.4",
            "transport_key": "THIS MUST NOT LEAK",
            "tcp": [{
                "sent": 100,
                "resources": {
                    "path_selection": {"attempts": 13, "flight_budget": 10},
                    "session_lock_wait_ns": 7,
                    "queue_admission_waiters": 2,
                    "secret": "HIDE ME",
                },
                "lifecycle": {"session_tag": "only-this-tag", "events": ["DO NOT LEAK"]},
                "path_stats": [{"id": 1, "budget_bytes": 1234, "outstanding_bytes": 100,
                                "address": "private-address", "last_error": "timeout"}],
            }],
        }
        out = mod.sanitized_snapshot(status)
        text = str(out)
        for secret in ("THIS MUST NOT LEAK", "HIDE ME", "DO NOT LEAK", "private-address"):
            self.assertNotIn(secret, text)
        session = out["sessions"][0]
        self.assertEqual(session["resources"]["path_selection"]["flight_budget"], 10)
        self.assertEqual(session["carriers"][0]["budget_bytes"], 1234)
        self.assertEqual(session["session_tag"], "only-this-tag")


if __name__ == "__main__":
    unittest.main()
