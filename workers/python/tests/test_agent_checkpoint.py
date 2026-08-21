from __future__ import annotations

import unittest
from urllib.parse import parse_qs, urlsplit

from ai_companion_worker.agent_checkpoint import scoped_dsn


class AgentCheckpointTest(unittest.TestCase):
    def test_scoped_dsn_adds_langgraph_search_path(self) -> None:
        value = scoped_dsn(
            "postgres://agent:secret@postgres:5432/ai_companion?sslmode=disable"
        )
        query = parse_qs(urlsplit(value).query)
        self.assertEqual(query["sslmode"], ["disable"])
        self.assertEqual(
            query["options"],
            ["-csearch_path=langgraph,public"],
        )

    def test_scoped_dsn_rejects_non_postgres_uri(self) -> None:
        with self.assertRaises(ValueError):
            scoped_dsn("mysql://localhost/database")


if __name__ == "__main__":
    unittest.main()
