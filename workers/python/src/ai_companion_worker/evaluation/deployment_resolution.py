from __future__ import annotations

import argparse
import errno
import hashlib
import hmac
import json
import os
import re
import stat
import sys
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any, Callable, Literal, Mapping, Sequence, cast

from .deployment_controller import (
    DeploymentBusyError,
    DeploymentControllerConfigError,
    DeploymentControllerError,
    DeploymentLedger,
    ManualResolution,
)
from .release_attestation import MAX_CLOCK_SKEW_SECONDS


EVIDENCE_SCHEMA_VERSION = "observability-deployment-resolution-evidence-v1"
REQUEST_SCHEMA_VERSION = "observability-deployment-resolution-request-v1"
APPROVAL_SCHEMA_VERSION = "observability-deployment-resolution-approval-v1"
RESOLUTION_SCOPE = "deployment-manual-resolution"
REQUEST_KEY_ENV = "OBSERVABILITY_RESOLUTION_REQUEST_KEY"
REQUEST_KEY_ID_ENV = "OBSERVABILITY_RESOLUTION_REQUEST_KEY_ID"
APPROVAL_KEY_ENV = "OBSERVABILITY_RESOLUTION_APPROVAL_KEY"
APPROVAL_KEY_ID_ENV = "OBSERVABILITY_RESOLUTION_APPROVAL_KEY_ID"
MAX_FILE_BYTES = 64 * 1024
MAX_REQUEST_TTL_SECONDS = 15 * 60
MAX_APPROVAL_TTL_SECONDS = 5 * 60
MAX_EVIDENCE_AGE_SECONDS = 24 * 60 * 60
_REQUEST_DOMAIN = b"ai-companion/deployment-manual-resolution/request/v1"
_APPROVAL_DOMAIN = b"ai-companion/deployment-manual-resolution/approval/v1"
_IDENTIFIER_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}")
_KEY_ID_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}")
_DIGEST_RE = re.compile(r"[0-9a-f]{64}")
_ERROR_CODE_RE = re.compile(r"[a-z][a-z0-9_]{0,63}")
_EXTERNAL_ID_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}")
_RESOLUTION_ID_RE = re.compile(r"resolve-[0-9a-f]{32}")


class DeploymentResolutionError(ValueError):
    pass


class DeploymentResolutionConfigError(DeploymentResolutionError):
    pass


def create_resolution_evidence(
    ledger: DeploymentLedger,
    state_dir: Path,
    deployment_id: str,
    proposed_status: Literal["completed", "failed"],
    reason_code: str,
    requested_by: str,
    provider_evidence_sha256: str,
    *,
    now: datetime | None = None,
) -> tuple[dict[str, Any], Path, bool]:
    current_time = _normalize_now(now)
    deployment_id = _validate_identifier(deployment_id, "deployment_id")
    proposed_status = _validate_proposed_status(proposed_status)
    reason_code = _validate_reason_code(reason_code)
    requested_by = _validate_identifier(requested_by, "requested_by")
    provider_evidence_sha256 = _validate_digest(
        provider_evidence_sha256,
        "provider_evidence_sha256",
    )
    operation = ledger.get(deployment_id)
    if operation["status"] != "indeterminate":
        raise DeploymentResolutionError(
            "resolution evidence requires an indeterminate deployment"
        )
    external_operation_id = operation["external_operation_id"]
    if proposed_status == "completed" and external_operation_id is None:
        raise DeploymentResolutionError(
            "completed resolution evidence requires an external operation identifier"
        )
    payload: dict[str, Any] = {
        "schema_version": EVIDENCE_SCHEMA_VERSION,
        "deployment_id": deployment_id,
        "proposed_status": proposed_status,
        "adapter": operation["adapter"],
        "external_operation_id": external_operation_id,
        "reason_code": reason_code,
        "observed_at": _format_timestamp(current_time),
        "requested_by": requested_by,
        "provider_evidence_sha256": provider_evidence_sha256,
    }
    evidence = _validate_evidence_contract(payload)
    evidence_sha256 = _digest(_canonical_json(evidence))
    _ensure_private_state_directory(state_dir)
    path = state_dir / f"evidence-{evidence_sha256[:32]}.json"
    try:
        _write_exclusive_private_json(path, evidence)
        return evidence, path, True
    except FileExistsError:
        existing = _read_contract(path, _validate_evidence_contract)
        if existing != evidence:
            raise DeploymentResolutionError(
                "resolution evidence filename is bound to different content"
            )
        return existing, path, False


def issue_resolution_request(
    ledger: DeploymentLedger,
    evidence_path: Path,
    state_dir: Path,
    key: bytes,
    key_id: str,
    *,
    ttl_seconds: int = 300,
    now: datetime | None = None,
) -> tuple[dict[str, Any], Path, bool]:
    current_time = _normalize_now(now)
    signing_key = _validate_key(key, "request key")
    signing_key_id = _validate_key_id(key_id, "request key_id")
    time_to_live = _validate_ttl(
        ttl_seconds,
        MAX_REQUEST_TTL_SECONDS,
        "request",
    )
    evidence = _read_contract(evidence_path, _validate_evidence_contract)
    _validate_evidence_time(evidence, current_time)
    operation = _require_evidence_matches_ledger(ledger, evidence)
    if ledger.resolution(str(evidence["deployment_id"])) is not None:
        raise DeploymentResolutionError("deployment already has a manual resolution")
    evidence_sha256 = _digest(_canonical_json(evidence))
    resolution_seed = _canonical_json(
        {
            "schema_version": "observability-deployment-resolution-id-v1",
            "deployment_id": evidence["deployment_id"],
            "expected_updated_at": operation["updated_at"],
            "evidence_sha256": evidence_sha256,
        }
    )
    resolution_id = f"resolve-{_digest(resolution_seed)[:32]}"
    expiry = current_time + timedelta(seconds=time_to_live)
    payload: dict[str, Any] = {
        "schema_version": REQUEST_SCHEMA_VERSION,
        "resolution_id": resolution_id,
        "scope": RESOLUTION_SCOPE,
        "deployment_id": evidence["deployment_id"],
        "proposed_status": evidence["proposed_status"],
        "adapter": evidence["adapter"],
        "external_operation_id": evidence["external_operation_id"],
        "reason_code": evidence["reason_code"],
        "expected_updated_at": operation["updated_at"],
        "expected_error_code": operation["error_code"],
        "evidence_sha256": evidence_sha256,
        "provider_evidence_sha256": evidence["provider_evidence_sha256"],
        "requested_by": evidence["requested_by"],
        "requester_key_id": signing_key_id,
        "issued_at": _format_timestamp(current_time),
        "expires_at": _format_timestamp(expiry),
    }
    request = dict(payload)
    request["signature"] = _sign(signing_key, _REQUEST_DOMAIN, payload)
    request = _validate_request_contract(request)
    _ensure_private_state_directory(state_dir)
    path = state_dir / f"{resolution_id}.request.json"
    try:
        _write_exclusive_private_json(path, request)
        return request, path, True
    except FileExistsError:
        existing = verify_resolution_request(
            evidence_path,
            path,
            signing_key,
            signing_key_id,
            now=current_time,
        )
        if existing["resolution_id"] != resolution_id:
            raise DeploymentResolutionError(
                "resolution_id is already bound to a different request"
            )
        return existing, path, False


def verify_resolution_request(
    evidence_path: Path,
    request_path: Path,
    key: bytes,
    expected_key_id: str,
    *,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = _normalize_now(now)
    verification_key = _validate_key(key, "request key")
    key_id = _validate_key_id(expected_key_id, "request key_id")
    evidence = _read_contract(evidence_path, _validate_evidence_contract)
    _validate_evidence_time(evidence, current_time)
    request = _read_contract(request_path, _validate_request_contract)
    if request["requester_key_id"] != key_id:
        raise DeploymentResolutionError("resolution request key_id is not trusted")
    _verify_signature(request, verification_key, _REQUEST_DOMAIN, "resolution request")
    expected_bindings = {
        "deployment_id": evidence["deployment_id"],
        "proposed_status": evidence["proposed_status"],
        "adapter": evidence["adapter"],
        "external_operation_id": evidence["external_operation_id"],
        "reason_code": evidence["reason_code"],
        "evidence_sha256": _digest(_canonical_json(evidence)),
        "provider_evidence_sha256": evidence["provider_evidence_sha256"],
        "requested_by": evidence["requested_by"],
    }
    if any(request.get(field) != value for field, value in expected_bindings.items()):
        raise DeploymentResolutionError(
            "resolution request does not match the evidence"
        )
    _validate_signed_lifetime(
        request,
        current_time,
        MAX_REQUEST_TTL_SECONDS,
        "resolution request",
    )
    return request


def approve_resolution_request(
    request_path: Path,
    state_dir: Path,
    key: bytes,
    key_id: str,
    approved_by: str,
    *,
    ttl_seconds: int = 180,
    now: datetime | None = None,
) -> tuple[dict[str, Any], Path, bool]:
    current_time = _normalize_now(now)
    signing_key = _validate_key(key, "approval key")
    signing_key_id = _validate_key_id(key_id, "approval key_id")
    approved_by = _validate_identifier(approved_by, "approved_by")
    time_to_live = _validate_ttl(
        ttl_seconds,
        MAX_APPROVAL_TTL_SECONDS,
        "approval",
    )
    # The approval job intentionally does not possess the requester's HMAC secret.
    # It signs the exact request bytes; the apply job verifies both independent signatures.
    request = _read_contract(request_path, _validate_request_contract)
    if request["requester_key_id"] == signing_key_id:
        raise DeploymentResolutionConfigError(
            "request and approval key IDs must be different"
        )
    if request["requested_by"] == approved_by:
        raise DeploymentResolutionConfigError(
            "requester and approver must be different"
        )
    request_expiry = _parse_timestamp(str(request["expires_at"]), "request expires_at")
    if current_time >= request_expiry:
        raise DeploymentResolutionError("resolution request has expired")
    expiry = min(
        request_expiry,
        current_time + timedelta(seconds=time_to_live),
    )
    request_sha256 = _digest(_canonical_json(request))
    payload: dict[str, Any] = {
        "schema_version": APPROVAL_SCHEMA_VERSION,
        "resolution_id": request["resolution_id"],
        "scope": RESOLUTION_SCOPE,
        "decision": "approve",
        "deployment_id": request["deployment_id"],
        "request_sha256": request_sha256,
        "approved_by": approved_by,
        "approver_key_id": signing_key_id,
        "issued_at": _format_timestamp(current_time),
        "expires_at": _format_timestamp(expiry),
    }
    approval = dict(payload)
    approval["signature"] = _sign(signing_key, _APPROVAL_DOMAIN, payload)
    approval = _validate_approval_contract(approval)
    _ensure_private_state_directory(state_dir)
    path = state_dir / f"{request['resolution_id']}.approval.json"
    try:
        _write_exclusive_private_json(path, approval)
        return approval, path, True
    except FileExistsError:
        existing = _verify_approval_signature(
            request,
            path,
            signing_key,
            signing_key_id,
            now=current_time,
        )
        if existing["approved_by"] != approved_by:
            raise DeploymentResolutionError(
                "resolution_id is already bound to a different approval"
            )
        return existing, path, False


def check_manual_resolution(
    ledger: DeploymentLedger,
    evidence_path: Path,
    request_path: Path,
    approval_path: Path,
    request_key: bytes,
    request_key_id: str,
    approval_key: bytes,
    approval_key_id: str,
    *,
    now: datetime | None = None,
) -> tuple[ManualResolution, str]:
    current_time = _normalize_now(now)
    trusted_request_key = _validate_key(request_key, "request key")
    trusted_approval_key = _validate_key(approval_key, "approval key")
    if hmac.compare_digest(trusted_request_key, trusted_approval_key):
        raise DeploymentResolutionConfigError(
            "request and approval keys must be different"
        )
    evidence = _read_contract(evidence_path, _validate_evidence_contract)
    request = verify_resolution_request(
        evidence_path,
        request_path,
        trusted_request_key,
        request_key_id,
        now=current_time,
    )
    approval = _verify_approval_signature(
        request,
        approval_path,
        trusted_approval_key,
        approval_key_id,
        now=current_time,
    )
    if request["requester_key_id"] == approval["approver_key_id"]:
        raise DeploymentResolutionConfigError(
            "request and approval key IDs must be different"
        )
    if request["requested_by"] == approval["approved_by"]:
        raise DeploymentResolutionConfigError(
            "requester and approver must be different"
        )
    resolution = ManualResolution(
        resolution_id=str(request["resolution_id"]),
        deployment_id=str(request["deployment_id"]),
        proposed_status=request["proposed_status"],
        adapter=str(request["adapter"]),
        external_operation_id=request["external_operation_id"],
        reason_code=str(request["reason_code"]),
        expected_updated_at=str(request["expected_updated_at"]),
        expected_error_code=request["expected_error_code"],
        evidence_sha256=_digest(_canonical_json(evidence)),
        provider_evidence_sha256=str(request["provider_evidence_sha256"]),
        request_sha256=_digest(_canonical_json(request)),
        approval_sha256=_digest(_canonical_json(approval)),
        requested_by=str(request["requested_by"]),
        approved_by=str(approval["approved_by"]),
        requester_key_id=str(request["requester_key_id"]),
        approver_key_id=str(approval["approver_key_id"]),
    )
    existing = ledger.resolution(resolution.deployment_id)
    operation = ledger.get(resolution.deployment_id)
    if existing is not None:
        if existing["resolution_id"] != resolution.resolution_id:
            raise DeploymentResolutionError(
                "deployment already consumed a different manual resolution"
            )
        if operation["status"] != resolution.proposed_status:
            raise DeploymentResolutionError(
                "consumed resolution does not match deployment status"
            )
        return resolution, "consumed"
    if operation["status"] != "indeterminate":
        raise DeploymentResolutionError(
            "only an indeterminate deployment can be manually resolved"
        )
    expected_bindings = {
        "adapter": resolution.adapter,
        "external_operation_id": resolution.external_operation_id,
        "updated_at": resolution.expected_updated_at,
        "error_code": resolution.expected_error_code,
    }
    if any(operation.get(field) != value for field, value in expected_bindings.items()):
        raise DeploymentBusyError(
            "deployment changed after the manual resolution request was signed"
        )
    return resolution, "ready"


def apply_manual_resolution(
    ledger: DeploymentLedger,
    evidence_path: Path,
    request_path: Path,
    approval_path: Path,
    request_key: bytes,
    request_key_id: str,
    approval_key: bytes,
    approval_key_id: str,
    *,
    now: datetime | None = None,
) -> tuple[dict[str, Any], bool]:
    current_time = _normalize_now(now)
    resolution, _status = check_manual_resolution(
        ledger,
        evidence_path,
        request_path,
        approval_path,
        request_key,
        request_key_id,
        approval_key,
        approval_key_id,
        now=current_time,
    )
    return ledger.apply_manual_resolution(resolution, current_time)


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Create, dual-sign, check, or apply a manual deployment resolution"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)
    evidence_parser = subparsers.add_parser("evidence")
    evidence_parser.add_argument("--ledger", type=Path, required=True)
    evidence_parser.add_argument("--state-dir", type=Path, required=True)
    evidence_parser.add_argument("--deployment-id", required=True)
    evidence_parser.add_argument(
        "--proposed-status",
        choices=("completed", "failed"),
        required=True,
    )
    evidence_parser.add_argument("--reason-code", required=True)
    evidence_parser.add_argument("--requested-by", required=True)
    evidence_parser.add_argument("--provider-evidence-sha256", required=True)

    request_parser = subparsers.add_parser("request")
    request_parser.add_argument("--ledger", type=Path, required=True)
    request_parser.add_argument("--evidence", type=Path, required=True)
    request_parser.add_argument("--state-dir", type=Path, required=True)
    request_parser.add_argument("--ttl-seconds", type=int, default=300)

    approval_parser = subparsers.add_parser("approve")
    approval_parser.add_argument("--request", type=Path, required=True)
    approval_parser.add_argument("--state-dir", type=Path, required=True)
    approval_parser.add_argument("--approved-by", required=True)
    approval_parser.add_argument("--ttl-seconds", type=int, default=180)

    for command in ("check", "apply"):
        command_parser = subparsers.add_parser(command)
        command_parser.add_argument("--ledger", type=Path, required=True)
        command_parser.add_argument("--evidence", type=Path, required=True)
        command_parser.add_argument("--request", type=Path, required=True)
        command_parser.add_argument("--approval", type=Path, required=True)
    args = parser.parse_args(argv)

    try:
        if args.command == "evidence":
            ledger = DeploymentLedger(args.ledger, create=False)
            evidence, path, created = create_resolution_evidence(
                ledger,
                args.state_dir,
                args.deployment_id,
                args.proposed_status,
                args.reason_code,
                args.requested_by,
                args.provider_evidence_sha256,
            )
            output = {
                "deployment_id": evidence["deployment_id"],
                "evidence": str(path),
                "status": "created" if created else "reused",
            }
        elif args.command == "request":
            key, key_id = _key_from_environment(REQUEST_KEY_ENV, REQUEST_KEY_ID_ENV)
            ledger = DeploymentLedger(args.ledger, create=False)
            request, path, created = issue_resolution_request(
                ledger,
                args.evidence,
                args.state_dir,
                key,
                key_id,
                ttl_seconds=args.ttl_seconds,
            )
            output = {
                "deployment_id": request["deployment_id"],
                "expires_at": request["expires_at"],
                "request": str(path),
                "resolution_id": request["resolution_id"],
                "status": "issued" if created else "reused",
            }
        elif args.command == "approve":
            key, key_id = _key_from_environment(APPROVAL_KEY_ENV, APPROVAL_KEY_ID_ENV)
            approval, path, created = approve_resolution_request(
                args.request,
                args.state_dir,
                key,
                key_id,
                args.approved_by,
                ttl_seconds=args.ttl_seconds,
            )
            output = {
                "approval": str(path),
                "deployment_id": approval["deployment_id"],
                "expires_at": approval["expires_at"],
                "resolution_id": approval["resolution_id"],
                "status": "approved" if created else "reused",
            }
        else:
            request_key, request_key_id = _key_from_environment(
                REQUEST_KEY_ENV,
                REQUEST_KEY_ID_ENV,
            )
            approval_key, approval_key_id = _key_from_environment(
                APPROVAL_KEY_ENV,
                APPROVAL_KEY_ID_ENV,
            )
            ledger = DeploymentLedger(args.ledger, create=False)
            if args.command == "check":
                resolution, status_value = check_manual_resolution(
                    ledger,
                    args.evidence,
                    args.request,
                    args.approval,
                    request_key,
                    request_key_id,
                    approval_key,
                    approval_key_id,
                )
                output = {
                    "deployment_id": resolution.deployment_id,
                    "proposed_status": resolution.proposed_status,
                    "resolution_id": resolution.resolution_id,
                    "status": status_value,
                }
            else:
                operation, created = apply_manual_resolution(
                    ledger,
                    args.evidence,
                    args.request,
                    args.approval,
                    request_key,
                    request_key_id,
                    approval_key,
                    approval_key_id,
                )
                consumed_resolution = ledger.resolution(str(operation["deployment_id"]))
                if consumed_resolution is None:
                    raise DeploymentResolutionError(
                        "applied resolution audit record is missing"
                    )
                output = {
                    "deployment_id": operation["deployment_id"],
                    "resolution_id": consumed_resolution["resolution_id"],
                    "status": operation["status"],
                    "result": "applied" if created else "reused",
                }
    except (
        DeploymentResolutionConfigError,
        DeploymentControllerConfigError,
    ) as exc:
        print(f"deployment resolution configuration error: {exc}", file=sys.stderr)
        return 2
    except (
        DeploymentResolutionError,
        DeploymentControllerError,
        OSError,
    ) as exc:
        print(f"deployment resolution error: {exc}", file=sys.stderr)
        return 1

    print(json.dumps(output, sort_keys=True))
    return 0


def _require_evidence_matches_ledger(
    ledger: DeploymentLedger,
    evidence: Mapping[str, Any],
) -> dict[str, Any]:
    operation = ledger.get(str(evidence["deployment_id"]))
    if operation["status"] != "indeterminate":
        raise DeploymentResolutionError(
            "resolution request requires an indeterminate deployment"
        )
    bindings = {
        "adapter": evidence["adapter"],
        "external_operation_id": evidence["external_operation_id"],
    }
    if any(operation.get(field) != value for field, value in bindings.items()):
        raise DeploymentResolutionError("resolution evidence does not match the ledger")
    if evidence["proposed_status"] == "completed" and (
        operation["external_operation_id"] is None
    ):
        raise DeploymentResolutionError(
            "completed resolution requires an external operation identifier"
        )
    return operation


def _verify_approval_signature(
    request: Mapping[str, Any],
    approval_path: Path,
    key: bytes,
    expected_key_id: str,
    *,
    now: datetime,
) -> dict[str, Any]:
    verification_key = _validate_key(key, "approval key")
    key_id = _validate_key_id(expected_key_id, "approval key_id")
    approval = _read_contract(approval_path, _validate_approval_contract)
    if approval["approver_key_id"] != key_id:
        raise DeploymentResolutionError("resolution approval key_id is not trusted")
    _verify_signature(approval, verification_key, _APPROVAL_DOMAIN, "resolution approval")
    expected_bindings = {
        "resolution_id": request["resolution_id"],
        "deployment_id": request["deployment_id"],
        "request_sha256": _digest(_canonical_json(request)),
    }
    if any(approval.get(field) != value for field, value in expected_bindings.items()):
        raise DeploymentResolutionError(
            "resolution approval does not match the signed request"
        )
    _validate_signed_lifetime(
        approval,
        now,
        MAX_APPROVAL_TTL_SECONDS,
        "resolution approval",
    )
    approval_expiry = _parse_timestamp(str(approval["expires_at"]), "approval expires_at")
    request_expiry = _parse_timestamp(str(request["expires_at"]), "request expires_at")
    if approval_expiry > request_expiry:
        raise DeploymentResolutionError(
            "resolution approval outlives the signed request"
        )
    return approval


def _validate_evidence_time(evidence: Mapping[str, Any], now: datetime) -> None:
    observed_at = _parse_timestamp(str(evidence["observed_at"]), "evidence observed_at")
    if observed_at > now + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DeploymentResolutionError("resolution evidence timestamp is in the future")
    if now - observed_at > timedelta(seconds=MAX_EVIDENCE_AGE_SECONDS):
        raise DeploymentResolutionError("resolution evidence is too old")


def _validate_signed_lifetime(
    value: Mapping[str, Any],
    now: datetime,
    maximum_ttl: int,
    label: str,
) -> None:
    issued_at = _parse_timestamp(str(value["issued_at"]), f"{label} issued_at")
    expires_at = _parse_timestamp(str(value["expires_at"]), f"{label} expires_at")
    if issued_at > now + timedelta(seconds=MAX_CLOCK_SKEW_SECONDS):
        raise DeploymentResolutionError(f"{label} timestamp is in the future")
    if expires_at <= issued_at:
        raise DeploymentResolutionError(f"{label} lifetime is invalid")
    if expires_at - issued_at > timedelta(seconds=maximum_ttl):
        raise DeploymentResolutionError(f"{label} lifetime exceeds policy")
    if now >= expires_at:
        raise DeploymentResolutionError(f"{label} has expired")


def _validate_evidence_contract(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "deployment_id",
        "proposed_status",
        "adapter",
        "external_operation_id",
        "reason_code",
        "observed_at",
        "requested_by",
        "provider_evidence_sha256",
    }
    result = _require_fields(value, fields, "resolution evidence")
    if result["schema_version"] != EVIDENCE_SCHEMA_VERSION:
        raise DeploymentResolutionError("resolution evidence schema_version is invalid")
    _validate_identifier(result["deployment_id"], "deployment_id")
    _validate_proposed_status(result["proposed_status"])
    _validate_identifier(result["adapter"], "adapter")
    _validate_external_id(result["external_operation_id"])
    _validate_reason_code(result["reason_code"])
    _validate_identifier(result["requested_by"], "requested_by")
    _validate_digest(result["provider_evidence_sha256"], "provider_evidence_sha256")
    _parse_timestamp_string(result["observed_at"], "evidence observed_at")
    return result


def _validate_request_contract(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "resolution_id",
        "scope",
        "deployment_id",
        "proposed_status",
        "adapter",
        "external_operation_id",
        "reason_code",
        "expected_updated_at",
        "expected_error_code",
        "evidence_sha256",
        "provider_evidence_sha256",
        "requested_by",
        "requester_key_id",
        "issued_at",
        "expires_at",
        "signature",
    }
    result = _require_fields(value, fields, "resolution request")
    if (
        result["schema_version"] != REQUEST_SCHEMA_VERSION
        or result["scope"] != RESOLUTION_SCOPE
    ):
        raise DeploymentResolutionError("resolution request scope is invalid")
    _validate_resolution_id(result["resolution_id"])
    _validate_identifier(result["deployment_id"], "deployment_id")
    _validate_proposed_status(result["proposed_status"])
    _validate_identifier(result["adapter"], "adapter")
    _validate_external_id(result["external_operation_id"])
    _validate_reason_code(result["reason_code"])
    _parse_timestamp_string(result["expected_updated_at"], "expected_updated_at")
    if result["expected_error_code"] is not None:
        _validate_reason_code(result["expected_error_code"])
    _validate_digest(result["evidence_sha256"], "evidence_sha256")
    _validate_digest(result["provider_evidence_sha256"], "provider_evidence_sha256")
    _validate_identifier(result["requested_by"], "requested_by")
    _validate_key_id(result["requester_key_id"], "requester_key_id")
    _parse_timestamp_string(result["issued_at"], "request issued_at")
    _parse_timestamp_string(result["expires_at"], "request expires_at")
    _validate_digest(result["signature"], "request signature")
    return result


def _validate_approval_contract(value: Any) -> dict[str, Any]:
    fields = {
        "schema_version",
        "resolution_id",
        "scope",
        "decision",
        "deployment_id",
        "request_sha256",
        "approved_by",
        "approver_key_id",
        "issued_at",
        "expires_at",
        "signature",
    }
    result = _require_fields(value, fields, "resolution approval")
    if (
        result["schema_version"] != APPROVAL_SCHEMA_VERSION
        or result["scope"] != RESOLUTION_SCOPE
        or result["decision"] != "approve"
    ):
        raise DeploymentResolutionError("resolution approval scope is invalid")
    _validate_resolution_id(result["resolution_id"])
    _validate_identifier(result["deployment_id"], "deployment_id")
    _validate_digest(result["request_sha256"], "request_sha256")
    _validate_identifier(result["approved_by"], "approved_by")
    _validate_key_id(result["approver_key_id"], "approver_key_id")
    _parse_timestamp_string(result["issued_at"], "approval issued_at")
    _parse_timestamp_string(result["expires_at"], "approval expires_at")
    _validate_digest(result["signature"], "approval signature")
    return result


def _require_fields(value: Any, fields: set[str], label: str) -> dict[str, Any]:
    if not isinstance(value, Mapping) or set(value) != fields:
        raise DeploymentResolutionError(f"{label} contract is invalid")
    return dict(value)


def _verify_signature(
    value: Mapping[str, Any],
    key: bytes,
    domain: bytes,
    label: str,
) -> None:
    payload = {field: content for field, content in value.items() if field != "signature"}
    expected = _sign(key, domain, payload)
    if not hmac.compare_digest(str(value["signature"]), expected):
        raise DeploymentResolutionError(f"{label} signature is invalid")


def _sign(key: bytes, domain: bytes, payload: Mapping[str, Any]) -> str:
    derived_key = hmac.new(key, domain, hashlib.sha256).digest()
    return hmac.new(derived_key, _canonical_json(payload), hashlib.sha256).hexdigest()


def _key_from_environment(key_name: str, key_id_name: str) -> tuple[bytes, str]:
    key = os.environ.get(key_name)
    key_id = os.environ.get(key_id_name)
    if key is None:
        raise DeploymentResolutionConfigError(f"{key_name} is required")
    if key_id is None:
        raise DeploymentResolutionConfigError(f"{key_id_name} is required")
    return key.encode("utf-8"), key_id


def _validate_key(value: bytes, label: str) -> bytes:
    if not isinstance(value, bytes) or not 32 <= len(value) <= 4096:
        raise DeploymentResolutionConfigError(
            f"{label} must contain between 32 and 4096 bytes"
        )
    return value


def _validate_key_id(value: Any, label: str) -> str:
    if not isinstance(value, str) or _KEY_ID_RE.fullmatch(value) is None:
        raise DeploymentResolutionConfigError(f"{label} is invalid")
    return value


def _validate_identifier(value: Any, label: str) -> str:
    if not isinstance(value, str) or _IDENTIFIER_RE.fullmatch(value) is None:
        raise DeploymentResolutionConfigError(f"{label} is invalid")
    return value


def _validate_resolution_id(value: Any) -> str:
    if not isinstance(value, str) or _RESOLUTION_ID_RE.fullmatch(value) is None:
        raise DeploymentResolutionError("resolution_id is invalid")
    return value


def _validate_proposed_status(value: Any) -> Literal["completed", "failed"]:
    if not isinstance(value, str) or value not in {"completed", "failed"}:
        raise DeploymentResolutionConfigError("proposed_status is invalid")
    return cast(Literal["completed", "failed"], value)


def _validate_reason_code(value: Any) -> str:
    if not isinstance(value, str) or _ERROR_CODE_RE.fullmatch(value) is None:
        raise DeploymentResolutionConfigError("reason_code is invalid")
    return value


def _validate_external_id(value: Any) -> str | None:
    if value is not None and (
        not isinstance(value, str) or _EXTERNAL_ID_RE.fullmatch(value) is None
    ):
        raise DeploymentResolutionError("external_operation_id is invalid")
    return value


def _validate_digest(value: Any, label: str) -> str:
    if not isinstance(value, str) or _DIGEST_RE.fullmatch(value) is None:
        raise DeploymentResolutionConfigError(f"{label} is invalid")
    return value


def _validate_ttl(value: int, maximum: int, label: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise DeploymentResolutionConfigError(f"{label} TTL must be an integer")
    if not 1 <= value <= maximum:
        raise DeploymentResolutionConfigError(
            f"{label} TTL must be between 1 and {maximum} seconds"
        )
    return value


def _normalize_now(value: datetime | None) -> datetime:
    result = value or datetime.now(UTC)
    if result.tzinfo is None:
        raise DeploymentResolutionConfigError("current time must include a timezone")
    return result.astimezone(UTC)


def _parse_timestamp_string(value: Any, label: str) -> datetime:
    if not isinstance(value, str):
        raise DeploymentResolutionError(f"{label} is invalid")
    return _parse_timestamp(value, label)


def _parse_timestamp(value: str, label: str) -> datetime:
    try:
        result = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise DeploymentResolutionError(f"{label} is invalid") from exc
    if result.tzinfo is None:
        raise DeploymentResolutionError(f"{label} must include a timezone")
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


def _ensure_private_state_directory(path: Path) -> None:
    try:
        path.mkdir(mode=0o700, parents=False, exist_ok=False)
    except FileExistsError:
        pass
    except OSError as exc:
        raise DeploymentResolutionError(
            f"cannot create resolution state directory: {exc}"
        ) from exc
    _validate_private_state_directory(path)


def _validate_private_state_directory(path: Path) -> None:
    try:
        metadata = path.lstat()
    except OSError as exc:
        raise DeploymentResolutionError(
            f"cannot inspect resolution state directory: {exc}"
        ) from exc
    if not stat.S_ISDIR(metadata.st_mode) or path.is_symlink():
        raise DeploymentResolutionError("resolution state directory must be a real directory")
    if stat.S_IMODE(metadata.st_mode) != 0o700:
        raise DeploymentResolutionError("resolution state directory permissions must be 0700")
    if hasattr(os, "getuid") and metadata.st_uid != os.getuid():
        raise DeploymentResolutionError(
            "resolution state directory must be owned by the current user"
        )


def _read_contract(
    path: Path,
    validator: Callable[[Any], dict[str, Any]],
) -> dict[str, Any]:
    _validate_private_state_directory(path.parent)
    data = _read_private_regular_file(path)
    try:
        value = json.loads(data, object_pairs_hook=_reject_duplicate_keys)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        raise DeploymentResolutionError(f"invalid resolution JSON {path.name}: {exc}") from exc
    return validator(value)


def _read_private_regular_file(path: Path) -> bytes:
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(path, flags)
    except OSError as exc:
        if exc.errno == errno.ELOOP:
            raise DeploymentResolutionError(
                "resolution artifact must be a regular file"
            ) from exc
        raise DeploymentResolutionError(f"cannot open resolution artifact: {exc}") from exc
    try:
        metadata = os.fstat(descriptor)
        if not stat.S_ISREG(metadata.st_mode):
            raise DeploymentResolutionError("resolution artifact must be a regular file")
        if stat.S_IMODE(metadata.st_mode) != 0o600:
            raise DeploymentResolutionError("resolution artifact permissions must be 0600")
        if metadata.st_nlink != 1:
            raise DeploymentResolutionError("resolution artifact must not be hard-linked")
        if hasattr(os, "getuid") and metadata.st_uid != os.getuid():
            raise DeploymentResolutionError(
                "resolution artifact must be owned by the current user"
            )
        if metadata.st_size <= 0 or metadata.st_size > MAX_FILE_BYTES:
            raise DeploymentResolutionError("resolution artifact size is invalid")
        data = os.read(descriptor, MAX_FILE_BYTES + 1)
        if len(data) != metadata.st_size or len(data) > MAX_FILE_BYTES:
            raise DeploymentResolutionError(
                "resolution artifact changed while being read"
            )
        return data
    finally:
        os.close(descriptor)


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


def _reject_duplicate_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


if __name__ == "__main__":
    raise SystemExit(main())
