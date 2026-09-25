from __future__ import annotations

import os
import subprocess
import sys
import threading
from pathlib import Path

import pytest

from ai_companion_worker.model_concurrency import model_request_slot


def test_provider_slots_are_shared_and_release_after_process_death(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    tmp_path.chmod(0o700)
    monkeypatch.setenv("MODEL_CONCURRENCY_DIR", str(tmp_path))
    monkeypatch.setenv("MODEL_PROVIDER_CONCURRENCY", "1")
    monkeypatch.delenv("MODEL_CONCURRENCY_SHARED_GID", raising=False)
    script = """from ai_companion_worker.model_concurrency import model_request_slot
import sys
with model_request_slot("https://provider.test/v1",5):
 print("ready",flush=True)
 sys.stdin.read()
"""
    process = subprocess.Popen(
        [sys.executable, "-c", script],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        text=True,
        env=os.environ.copy(),
    )
    try:
        assert process.stdout is not None and process.stdout.readline().strip() == "ready"
        with pytest.raises(TimeoutError):
            with model_request_slot("https://provider.test/other", 0.03):
                pytest.fail("another process's provider permit was ignored")
        with model_request_slot("https://different.test", 1):
            pass
        process.kill()
        process.wait(timeout=2)
        with model_request_slot("https://provider.test", 1) as remaining:
            assert 0 < remaining <= 1
    finally:
        if process.poll() is None:
            process.kill()
        process.wait(timeout=2)


def test_provider_slots_bound_threads_and_release_on_errors(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    tmp_path.chmod(0o700)
    monkeypatch.setenv("MODEL_CONCURRENCY_DIR", str(tmp_path))
    monkeypatch.setenv("MODEL_PROVIDER_CONCURRENCY", "2")
    monkeypatch.delenv("MODEL_CONCURRENCY_SHARED_GID", raising=False)
    ready = threading.Barrier(3)
    release = threading.Event()

    def worker() -> None:
        with model_request_slot("https://provider.test", 3):
            ready.wait(timeout=2)
            release.wait(timeout=2)

    threads = [threading.Thread(target=worker) for _ in range(2)]
    for thread in threads:
        thread.start()
    try:
        ready.wait(timeout=2)
        with pytest.raises(TimeoutError):
            with model_request_slot("https://provider.test", 0.03):
                pytest.fail("concurrency cap exceeded")
    finally:
        release.set()
        for thread in threads:
            thread.join(timeout=2)
    with pytest.raises(RuntimeError):
        with model_request_slot("https://provider.test", 1):
            raise RuntimeError("failed request")
    with model_request_slot("https://provider.test", 1):
        pass


def test_provider_slots_reject_public_directory(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    tmp_path.chmod(0o777)
    monkeypatch.setenv("MODEL_CONCURRENCY_DIR", str(tmp_path))
    monkeypatch.delenv("MODEL_CONCURRENCY_SHARED_GID", raising=False)
    with pytest.raises(ValueError):
        with model_request_slot("https://provider.test", 1):
            pass
