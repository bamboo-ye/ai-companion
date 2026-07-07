from __future__ import annotations

import json
import signal
import threading
import time
from dataclasses import asdict, dataclass


@dataclass(frozen=True)
class WorkerStatus:
    service: str = "ai-companion-python-worker"
    status: str = "ready"


def status_payload() -> str:
    return json.dumps(asdict(WorkerStatus()), ensure_ascii=False, sort_keys=True)


def main() -> None:
    stopped = threading.Event()

    def stop(_signum: int, _frame: object) -> None:
        stopped.set()

    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)
    print(status_payload(), flush=True)

    while not stopped.wait(timeout=30.0):
        print(json.dumps({"event": "heartbeat", "at": time.time()}), flush=True)


if __name__ == "__main__":
    main()

