"""Host-wide provider permits shared by Go and persistent Python workers.

POSIX advisory locks are released by the OS on process death. Containers must
mount the same directory, use the same limit, and share a uid or configured gid.
"""

from __future__ import annotations

import fcntl
import hashlib
import os
import stat
import tempfile
import time
from contextlib import contextmanager
from pathlib import Path
from typing import Iterator
from urllib.parse import urlsplit


@contextmanager
def model_request_slot(base_url: str, timeout_seconds: float) -> Iterator[float]:
    limit = int(os.getenv("MODEL_PROVIDER_CONCURRENCY", "8"))
    if not 1 <= limit <= 64:
        raise ValueError("MODEL_PROVIDER_CONCURRENCY must be between 1 and 64")
    if timeout_seconds <= 0:
        raise TimeoutError("model request deadline exceeded")
    root = Path(
        os.getenv("MODEL_CONCURRENCY_DIR")
        or str(Path(tempfile.gettempdir()) / f"ai-companion-model-slots-{os.getuid()}")
    )
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = root.lstat()
    shared = os.getenv("MODEL_CONCURRENCY_SHARED_GID", "")
    group_ok = (
        bool(shared)
        and info.st_gid == int(shared)
        and int(shared) in {os.getgid(), *os.getgroups()}
        and not info.st_mode & 0o007
        and bool(info.st_mode & stat.S_ISGID)
    )
    private_ok = info.st_uid == os.getuid() and not info.st_mode & 0o077
    if not stat.S_ISDIR(info.st_mode) or not (group_ok or private_ok):
        raise ValueError("model concurrency directory must be private and owned by the worker")
    host = urlsplit(base_url).netloc.lower()
    if not host:
        raise ValueError("model provider URL must include a host")
    key = hashlib.sha256(host.encode()).hexdigest()
    deadline = time.monotonic() + timeout_seconds
    while True:
        for index in range(limit):
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError("model provider concurrency wait exceeded deadline")
            try:
                fd = os.open(
                    root / f"{key}-{index}.lock",
                    os.O_CREAT | os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC,
                    0o640 if group_ok else 0o600,
                )
            except PermissionError:
                # Another uid may have just created the file under a restrictive
                # umask and is about to make the group read bit explicit.
                if group_ok:
                    continue
                raise
            try:
                if group_ok and os.fstat(fd).st_uid == os.getuid():
                    os.fchmod(fd, 0o640)
                try:
                    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                except BlockingIOError:
                    continue
                try:
                    yield remaining
                finally:
                    fcntl.flock(fd, fcntl.LOCK_UN)
                return
            finally:
                os.close(fd)
        time.sleep(min(0.02, max(0, deadline - time.monotonic())))
