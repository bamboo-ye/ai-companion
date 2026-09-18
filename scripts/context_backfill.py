#!/usr/bin/env python3
"""Queue resumable context backfill through the operator API, without direct DB access."""
import argparse
import json
import os
import urllib.error
import urllib.request


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", default="http://localhost:8080")
    parser.add_argument("--cursor", default="")
    args = parser.parse_args()
    token = os.environ.get("CONTEXT_OPERATOR_TOKEN", "")
    if not token:
        parser.error("CONTEXT_OPERATOR_TOKEN is required")
    cursor = args.cursor
    while True:
        request = urllib.request.Request(
            args.base_url.rstrip("/") + "/v1/ops/context/backfill",
            data=json.dumps({"cursor": cursor}).encode(), method="POST",
            headers={"Authorization": "Bearer " + token, "Content-Type": "application/json", **({"X-Operator-TOTP": os.environ["CONTEXT_OPERATOR_TOTP"]} if os.environ.get("CONTEXT_OPERATOR_TOTP") else {})},
        )
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                result = json.load(response)
        except urllib.error.HTTPError as error:
            raise SystemExit(f"Backfill failed: HTTP {error.code}; resume cursor={cursor}") from None
        print(json.dumps(result), flush=True)
        if not result["has_more"]:
            break
        next_cursor = result["next_cursor"]
        if not next_cursor or next_cursor == cursor:
            raise SystemExit("Backfill did not advance")
        cursor = next_cursor


if __name__ == "__main__":
    main()
