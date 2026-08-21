from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
import re
import stat
import sys
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any, Mapping, Sequence

from .release_attestation import (
    KEY_ENV,
    KEY_ID_ENV,
    MAX_CLOCK_SKEW_SECONDS,
    AttestationError,
    verify_attestation,
)


AUTHORIZATION_SCHEMA_VERSION = "observability-deployment-authorization-v1"
AUTHORIZATION_SCOPE = "release-promotion"
MAX_AUTHORIZATION_BYTES = 64 * 1024
MAX_AUTHORIZATION_TTL_SECONDS = 15 * 60
_DOMAIN_SEPARATOR = b"ai-companion/observability-deployment-authorization/v1"
_DEPLOYMENT_ID_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}")
_RUN_ID_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}")
_KEY_ID_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}")
_DIGEST_RE = re.compile(r"[0-9a-f]{64}")
_AUTHORIZATION_ID_RE = re.compile(r"auth-[0-9a-f]{32}")


class DeploymentAuthorizationError(ValueError):
    pass


class DeploymentAuthorizationConfigError(DeploymentAuthorizationError):
    pass


def check_gate_for_deployment(
    gate_dir: Path,
    key: bytes,
    key_id: str,
    deployment_id: str,
    *,
    max_age_seconds: int = 900,
    now: datetime | None = None,
) -> dict[str, Any]:
    normalized_deployment_id = _validate_deployment_id(deployment_id)
    current_time = _normalize_now(now)
    attestation = verify_attestation(
        gate_dir,
        key,
        key_id,
        max_age_seconds=max_age_seconds,
        require_decision="promote",
        expected_run_id=gate_dir.name,
        now=current_time,
    )
    attestation_digest = _digest(_canonical_json(attestation))
    idempotency_key = _idempotency_key(
        normalized_deployment_id,
        str(attestation["run_id"]),
        attestation_digest,
    )
    return {
        "deployment_id": normalized_deployment_id,
        "run_id": attestation["run_id"],
        "decision": "promote",
        "key_id": attestation["key_id"],
        "attestation_sha256": attestation_digest,
        "gate_report_sha256": attestation["gate_report_sha256"],
        "attestation_created_at": attestation["created_at"],
        "gate_generated_at": attestation["gate_generated_at"],
        "idempotency_key": idempotency_key,
    }


def issue_deployment_authorization(
    gate_dir: Path,
    state_dir: Path,
    key: bytes,
    key_id: str,
    deployment_id: str,
    *,
    max_age_seconds: int = 900,
    ttl_seconds: int = 300,
    now: datetime | None = None,
) -> tuple[dict[str, Any], bool]:
    current_time = _normalize_now(now)
    time_to_live = _validate_ttl(ttl_seconds)
    readiness = check_gate_for_deployment(
        gate_dir,
        key,
        key_id,
        deployment_id,
        max_age_seconds=max_age_seconds,
        now=current_time,
    )
    _ensure_private_state_directory(state_dir)
    authorization_path = state_dir / _authorization_filename(str(readiness["deployment_id"]))

    attestation_created_at = _parse_timestamp(
        str(readiness["attestation_created_at"]),
        "attestation created_at",
    )
    gate_generated_at = _parse_timestamp(
        str(readiness["gate_generated_at"]),
        "gate generated_at",
    )
    expiry = min(
        current_time + timedelta(seconds=time_to_live),
        attestation_created_at + timedelta(seconds=max_age_seconds),
        gate_generated_at + timedelta(seconds=max_age_seconds),
    )
    if expiry <= current_time:
        raise DeploymentAuthorizationError("verified Gate has no remaining authorization lifetime")

    idempotency_key = str(readiness["idempotency_key"])
    payload: dict[str, Any] = {
        "schema_version": AUTHORIZATION_SCHEMA_VERSION,
        "authorization_id": f"auth-{idempotency_key[:32]}",
        "scope": AUTHORIZATION_SCOPE,
        "deployment_id": readiness["deployment_id"],
        "run_id": readiness["run_id"],
        "decision": "promote",
        "key_id": readiness["key_id"],
        "attestation_sha256": readiness["attestation_sha256"],
        "gate_report_sha256": readiness["gate_report_sha256"],
        "idempotency_key": idempotency_key,
        "issued_at": _format_timestamp(current_time),
        "expires_at": _format_timestamp(expiry),
    }
    authorization = dict(payload)
    authorization["signature"] = hmac.new(
        _authorization_signing_key(key),
        _canonical_json(payload),
        hashlib.sha256,
    ).hexdigest()

    try:
        _write_exclusive_private_json(authorization_path, authorization)
        return authorization, True
    except FileExistsError:
        existing = verify_deployment_authorization(
            gate_dir,
            authorization_path,
            key,
            key_id,
            deployment_id,
            max_age_seconds=max_age_seconds,
            now=current_time,
        )
        if existing["idempotency_key"] != idempotency_key:
            raise DeploymentAuthorizationError(
                "deployment_id is already bound to a different Gate"
            )
        return existing, False


def verify_deployment_authorization(
    gate_dir: Path,
    authorization_path: Path,
    key: bytes,
    key_id: str,
    expected_deployment_id: str,
    *,
    max_age_seconds: int = 900,
    now: datetime | None = None,
) -> dict[str, Any]:
    deployment_id = _validate_deployment_id(expected_deployment_id)
    current_time = _normalize_now(now)
    readiness = check_gate_for_deployment(
        gate_dir,
        key,
        key_id,
        deployment_id,
        max_age_seconds=max_age_seconds,
        now=current_time,
    )
    if authorization_path.name != _authorization_filename(deployment_id):
        raise DeploymentAuthorizationError(
            "authorization filename does not match expected deployment_id"
        )
    _validate_private_state_directory(authorization_path.parent)
    raw = _read_private_regular_file(authorization_path)
    authorization = _validate_authorization_contract(
        _load_json(raw, authorization_path.name)
    )

    signature = str(authorization["signature"])
    payload = {key: value for key, value in authorization.items() if key != "signature"}
    expected_signature = hmac.new(
        _authorization_signing_key(key),
        _canonical_json(payload),
        hashlib.sha256,
    ).hexdigest()
    if not hmac.compare_digest(signature, expected_signature):
        raise DeploymentAuthorizationError("deployment authorization signature is invalid")

    expected_bindings = {
        "deployment_id": deployment_id,
        "run_id": readiness["run_id"],
        "decision": "promote",
        "key_id": readiness["key_id"],
        "attestation_sha256": readiness["attestation_sha256"],
        "gate_report_sha256": readiness["gate_report_sha256"],
        "idempotency_key": readiness["idempotency_key"],
        "authorization_id": f"auth-{str(readiness['idempotency_key'])[:32]}",
    }
    if any(authorization.get(field) != value for field, value in expected_bindings.items()):
        raise DeploymentAuthorizationError(
            "deployment authorization does not match the verified Gate"
        )

    issued_at = _parse_timestamp(str(authorization["issued_at"]), "authorization issued_at")
    expires_at = _parse_timestamp(str(authorization["expires_at"]), "authorization expires_at")
    if issued_at > current_time + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DeploymentAuthorizationError("deployment authorization timestamp is in the future")
    if expires_at <= issued_at:
        raise DeploymentAuthorizationError("deployment authorization lifetime is invalid")
    if current_time >= expires_at:
        raise DeploymentAuthorizationError("deployment authorization has expired")
    attestation_deadline = _parse_timestamp(
        str(readiness["attestation_created_at"]),
        "attestation created_at",
    ) + timedelta(seconds=max_age_seconds)
    gate_deadline = _parse_timestamp(
        str(readiness["gate_generated_at"]),
        "gate generated_at",
    ) + timedelta(seconds=max_age_seconds)
    if expires_at > min(attestation_deadline, gate_deadline):
        raise DeploymentAuthorizationError(
            "deployment authorization exceeds verified Gate lifetime"
        )
    return authorization


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Check, issue, or verify a deployment authorization without deploying"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)
    for command in ("check", "issue", "verify"):
        command_parser = subparsers.add_parser(command)
        command_parser.add_argument("--gate-dir", type=Path, required=True)
        command_parser.add_argument("--deployment-id", required=True)
        command_parser.add_argument("--max-age-seconds", type=int, default=900)
        if command == "issue":
            command_parser.add_argument("--state-dir", type=Path, required=True)
            command_parser.add_argument("--ttl-seconds", type=int, default=300)
        if command == "verify":
            command_parser.add_argument("--authorization", type=Path, required=True)
    args = parser.parse_args(argv)

    try:
        key, key_id = _key_from_environment()
        if args.command == "check":
            result = check_gate_for_deployment(
                args.gate_dir,
                key,
                key_id,
                args.deployment_id,
                max_age_seconds=args.max_age_seconds,
            )
            output = {
                "decision": result["decision"],
                "deployment_id": result["deployment_id"],
                "idempotency_key": result["idempotency_key"],
                "run_id": result["run_id"],
                "status": "ready",
            }
        elif args.command == "issue":
            result, created = issue_deployment_authorization(
                args.gate_dir,
                args.state_dir,
                key,
                key_id,
                args.deployment_id,
                max_age_seconds=args.max_age_seconds,
                ttl_seconds=args.ttl_seconds,
            )
            output = {
                "authorization_id": result["authorization_id"],
                "deployment_id": result["deployment_id"],
                "expires_at": result["expires_at"],
                "idempotency_key": result["idempotency_key"],
                "status": "issued" if created else "reused",
            }
        else:
            result = verify_deployment_authorization(
                args.gate_dir,
                args.authorization,
                key,
                key_id,
                args.deployment_id,
                max_age_seconds=args.max_age_seconds,
            )
            output = {
                "authorization_id": result["authorization_id"],
                "deployment_id": result["deployment_id"],
                "expires_at": result["expires_at"],
                "idempotency_key": result["idempotency_key"],
                "status": "verified",
            }
    except DeploymentAuthorizationConfigError as exc:
        print(f"deployment authorization configuration error: {exc}", file=sys.stderr)
        return 2
    except (AttestationError, DeploymentAuthorizationError, OSError) as exc:
        print(f"deployment authorization verification error: {exc}", file=sys.stderr)
        return 1

    print(json.dumps(output, sort_keys=True))
    return 0


def _key_from_environment() -> tuple[bytes, str]:
    value = os.environ.get(KEY_ENV)
    key_id = os.environ.get(KEY_ID_ENV)
    if value is None:
        raise DeploymentAuthorizationConfigError(f"{KEY_ENV} is required")
    if key_id is None:
        raise DeploymentAuthorizationConfigError(f"{KEY_ID_ENV} is required")
    return value.encode("utf-8"), key_id


def _authorization_signing_key(key: bytes) -> bytes:
    return hmac.new(key, _DOMAIN_SEPARATOR, hashlib.sha256).digest()


def _idempotency_key(deployment_id: str, run_id: str, attestation_digest: str) -> str:
    return _digest(
        _canonical_json(
            {
                "schema_version": "observability-deployment-idempotency-v1",
                "deployment_id": deployment_id,
                "run_id": run_id,
                "attestation_sha256": attestation_digest,
            }
        )
    )


def _validate_deployment_id(value: str) -> str:
    if not isinstance(value, str) or _DEPLOYMENT_ID_RE.fullmatch(value) is None:
        raise DeploymentAuthorizationConfigError("deployment_id is invalid")
    return value


def _validate_ttl(value: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise DeploymentAuthorizationConfigError("authorization TTL must be an integer")
    if not 1 <= value <= MAX_AUTHORIZATION_TTL_SECONDS:
        raise DeploymentAuthorizationConfigError(
            f"authorization TTL must be between 1 and {MAX_AUTHORIZATION_TTL_SECONDS} seconds"
        )
    return value


def _authorization_filename(deployment_id: str) -> str:
    return f"{deployment_id}.authorization.json"


def _ensure_private_state_directory(path: Path) -> None:
    try:
        path.mkdir(mode=0o700, parents=False, exist_ok=False)
    except FileExistsError:
        pass
    except OSError as exc:
        raise DeploymentAuthorizationError(
            f"cannot create authorization state directory: {exc}"
        ) from exc
    _validate_private_state_directory(path)


def _validate_private_state_directory(path: Path) -> None:
    try:
        metadata = path.lstat()
    except OSError as exc:
        raise DeploymentAuthorizationError(
            f"cannot inspect authorization state directory: {exc}"
        ) from exc
    if not stat.S_ISDIR(metadata.st_mode) or path.is_symlink():
        raise DeploymentAuthorizationError(
            "authorization state directory must be a real directory"
        )
    if stat.S_IMODE(metadata.st_mode) != 0o700:
        raise DeploymentAuthorizationError(
            "authorization state directory permissions must be 0700"
        )
    if hasattr(os, "getuid") and metadata.st_uid != os.getuid():
        raise DeploymentAuthorizationError(
            "authorization state directory must be owned by the current user"
        )


def _read_private_regular_file(path: Path) -> bytes:
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError as exc:
        raise DeploymentAuthorizationError(
            f"cannot open deployment authorization: {exc}"
        ) from exc
    try:
        metadata = os.fstat(descriptor)
        if not stat.S_ISREG(metadata.st_mode):
            raise DeploymentAuthorizationError(
                "deployment authorization must be a regular file"
            )
        if stat.S_IMODE(metadata.st_mode) != 0o600:
            raise DeploymentAuthorizationError(
                "deployment authorization permissions must be 0600"
            )
        if metadata.st_nlink != 1:
            raise DeploymentAuthorizationError(
                "deployment authorization must not be hard-linked"
            )
        if hasattr(os, "getuid") and metadata.st_uid != os.getuid():
            raise DeploymentAuthorizationError(
                "deployment authorization must be owned by the current user"
            )
        if metadata.st_size <= 0 or metadata.st_size > MAX_AUTHORIZATION_BYTES:
            raise DeploymentAuthorizationError("deployment authorization size is invalid")
        chunks: list[bytes] = []
        remaining = MAX_AUTHORIZATION_BYTES + 1
        while remaining > 0:
            chunk = os.read(descriptor, min(64 * 1024, remaining))
            if not chunk:
                break
            chunks.append(chunk)
            remaining -= len(chunk)
        content = b"".join(chunks)
        if len(content) != metadata.st_size or len(content) > MAX_AUTHORIZATION_BYTES:
            raise DeploymentAuthorizationError(
                "deployment authorization changed while being read"
            )
        return content
    finally:
        os.close(descriptor)


def _validate_authorization_contract(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "authorization_id",
        "scope",
        "deployment_id",
        "run_id",
        "decision",
        "key_id",
        "attestation_sha256",
        "gate_report_sha256",
        "idempotency_key",
        "issued_at",
        "expires_at",
        "signature",
    }
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DeploymentAuthorizationError("deployment authorization contract is invalid")
    result = dict(value)
    if result.get("schema_version") != AUTHORIZATION_SCHEMA_VERSION:
        raise DeploymentAuthorizationError(
            "deployment authorization schema_version is invalid"
        )
    if result.get("scope") != AUTHORIZATION_SCOPE or result.get("decision") != "promote":
        raise DeploymentAuthorizationError("deployment authorization scope is invalid")
    string_patterns = (
        ("authorization_id", _AUTHORIZATION_ID_RE),
        ("deployment_id", _DEPLOYMENT_ID_RE),
        ("run_id", _RUN_ID_RE),
        ("key_id", _KEY_ID_RE),
        ("attestation_sha256", _DIGEST_RE),
        ("gate_report_sha256", _DIGEST_RE),
        ("idempotency_key", _DIGEST_RE),
        ("signature", _DIGEST_RE),
    )
    for field, pattern in string_patterns:
        field_value = result.get(field)
        if not isinstance(field_value, str) or pattern.fullmatch(field_value) is None:
            raise DeploymentAuthorizationError(
                f"deployment authorization {field} is invalid"
            )
    for field in ("issued_at", "expires_at"):
        field_value = result.get(field)
        if not isinstance(field_value, str):
            raise DeploymentAuthorizationError(
                f"deployment authorization {field} is invalid"
            )
        _parse_timestamp(field_value, f"authorization {field}")
    return result


def _load_json(data: bytes, name: str) -> Any:
    try:
        return json.loads(data, object_pairs_hook=_reject_duplicate_keys)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise DeploymentAuthorizationError(f"invalid authorization JSON {name}: {exc}") from exc


def _reject_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def _normalize_now(value: datetime | None) -> datetime:
    result = value or datetime.now(UTC)
    if result.tzinfo is None:
        raise DeploymentAuthorizationConfigError("current time must include a timezone")
    return result.astimezone(UTC)


def _parse_timestamp(value: str, field: str) -> datetime:
    try:
        result = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise DeploymentAuthorizationError(f"{field} is invalid") from exc
    if result.tzinfo is None:
        raise DeploymentAuthorizationError(f"{field} must include a timezone")
    return result.astimezone(UTC)


def _format_timestamp(value: datetime) -> str:
    return value.astimezone(UTC).isoformat().replace("+00:00", "Z")


def _canonical_json(value: Mapping[str, Any]) -> bytes:
    return json.dumps(
        value,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")


def _digest(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def _write_exclusive_private_json(path: Path, value: Mapping[str, Any]) -> None:
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(path, flags, 0o600)
    try:
        content = json.dumps(value, ensure_ascii=False, indent=2).encode("utf-8") + b"\n"
        with os.fdopen(descriptor, "wb", closefd=False) as output:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        os.chmod(path, 0o600)
    except BaseException:
        try:
            path.unlink()
        except OSError:
            pass
        raise
    finally:
        os.close(descriptor)


if __name__ == "__main__":
    raise SystemExit(main())
