from __future__ import annotations

import json
import os
import re
import tempfile
import threading
import time
import uuid
from pathlib import Path
from typing import Any


_CACHE_ENVELOPE_VERSION = "worker-result-cache-v1"
_SAFE_COMPONENT = re.compile(r"^[a-z0-9][a-z0-9_-]{0,63}$")
_SAFE_KEY = re.compile(r"^[a-f0-9]{64}$")


def load_json_result(namespace: str, key: str) -> Any | None:
    """Load one bounded, short-lived worker result shared by local subprocesses."""

    path = _entry_path(namespace, key)
    if path is None:
        return None
    try:
        stat = path.stat()
        if stat.st_size <= 0 or stat.st_size > _max_entry_bytes():
            _unlink(path)
            return None
        raw = path.read_bytes()
        envelope = json.loads(raw)
        if (
            not isinstance(envelope, dict)
            or envelope.get("version") != _CACHE_ENVELOPE_VERSION
            or float(envelope.get("expires_at") or 0) < time.time()
        ):
            _unlink(path)
            return None
        return envelope.get("value")
    except (FileNotFoundError, OSError, TypeError, ValueError, json.JSONDecodeError):
        return None


def store_json_result(namespace: str, key: str, value: Any) -> bool:
    """Atomically persist a JSON result without allowing unbounded cache growth."""

    path = _entry_path(namespace, key, create=True)
    if path is None:
        return False
    try:
        encoded = json.dumps(
            {
                "version": _CACHE_ENVELOPE_VERSION,
                "expires_at": time.time() + _ttl_seconds(),
                "value": value,
            },
            allow_nan=False,
            ensure_ascii=False,
            separators=(",", ":"),
        ).encode("utf-8")
    except (TypeError, ValueError):
        return False
    if len(encoded) > _max_entry_bytes():
        return False
    temporary = path.parent / (
        f".{key}.{os.getpid()}.{threading.get_ident()}.{uuid.uuid4().hex}.tmp"
    )
    try:
        descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "wb") as output:
            output.write(encoded)
            output.flush()
        os.replace(temporary, path)
        _prune(path.parent)
        return True
    except OSError:
        _unlink(temporary)
        return False


def delete_json_result(namespace: str, key: str) -> None:
    path = _entry_path(namespace, key)
    if path is not None:
        _unlink(path)


def _entry_path(namespace: str, key: str, *, create: bool = False) -> Path | None:
    if (
        not _cache_enabled()
        or not _SAFE_COMPONENT.fullmatch(namespace)
        or not _SAFE_KEY.fullmatch(key)
    ):
        return None
    root = _cache_root()
    directory = root / namespace
    try:
        if create:
            root.mkdir(mode=0o700, parents=True, exist_ok=True)
            directory.mkdir(mode=0o700, exist_ok=True)
            os.chmod(root, 0o700)
            os.chmod(directory, 0o700)
        elif not directory.is_dir():
            return None
    except OSError:
        return None
    return directory / f"{key}.json"


def _cache_root() -> Path:
    configured = os.environ.get("WORKER_RESULT_CACHE_DIR", "").strip()
    if configured:
        return Path(configured)
    return Path(tempfile.gettempdir()) / f"ai-companion-worker-cache-{os.getuid()}"


def _cache_enabled() -> bool:
    return os.environ.get("WORKER_RESULT_CACHE_ENABLED", "true").strip().lower() in {
        "1",
        "true",
        "yes",
        "on",
    }


def _ttl_seconds() -> int:
    return _bounded_env("WORKER_RESULT_CACHE_TTL_SECONDS", 1_800, minimum=60, maximum=86_400)


def _max_entries() -> int:
    return _bounded_env("WORKER_RESULT_CACHE_MAX_ENTRIES", 128, minimum=8, maximum=2_048)


def _max_bytes() -> int:
    return _bounded_env(
        "WORKER_RESULT_CACHE_MAX_BYTES",
        64 * 1024 * 1024,
        minimum=8 * 1024 * 1024,
        maximum=512 * 1024 * 1024,
    )


def _max_entry_bytes() -> int:
    return min(24 * 1024 * 1024, _max_bytes())


def _bounded_env(name: str, fallback: int, *, minimum: int, maximum: int) -> int:
    raw = os.environ.get(name, "").strip()
    try:
        value = int(raw) if raw else fallback
    except ValueError:
        return fallback
    return min(maximum, max(minimum, value))


def _prune(directory: Path) -> None:
    try:
        entries = sorted(
            (
                (path, path.stat())
                for path in directory.glob("*.json")
                if path.is_file()
            ),
            key=lambda item: item[1].st_mtime,
            reverse=True,
        )
    except OSError:
        return
    now = time.time()
    retained: list[tuple[Path, os.stat_result]] = []
    for path, stat in entries:
        if now - stat.st_mtime > _ttl_seconds():
            _unlink(path)
        else:
            retained.append((path, stat))
    total = 0
    for index, (path, stat) in enumerate(retained):
        total += stat.st_size
        if index >= _max_entries() or total > _max_bytes():
            _unlink(path)


def _unlink(path: Path) -> None:
    try:
        path.unlink(missing_ok=True)
    except OSError:
        pass
