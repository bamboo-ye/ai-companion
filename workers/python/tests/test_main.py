from __future__ import annotations

import json
import unittest

from ai_companion_worker.main import status_payload


class WorkerStatusTest(unittest.TestCase):
    def test_status_payload_is_ready(self) -> None:
        payload = json.loads(status_payload())
        self.assertEqual(payload["service"], "ai-companion-python-worker")
        self.assertEqual(payload["status"], "ready")


if __name__ == "__main__":
    unittest.main()
