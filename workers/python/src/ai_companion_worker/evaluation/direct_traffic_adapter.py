from __future__ import annotations

from dataclasses import dataclass
from typing import Literal, Protocol


ADAPTER_CONTRACT_VERSION = "agent-direct-traffic-adapter-contract-v1"
CERTIFICATION_ISOLATION_MODE = "isolated-certification-namespace-v1"


@dataclass(frozen=True)
class AdapterTrafficState:
    challenge_id: str
    current_traffic_percent: int
    revision: int


@dataclass(frozen=True)
class AdapterChangeRequest:
    schema_version: str
    challenge_id: str
    idempotency_key: str
    expected_traffic_percent: int
    target_traffic_percent: int
    expected_revision: int
    request_sha256: str


@dataclass(frozen=True)
class AdapterChangeResult:
    status: Literal["applied", "conflict"]
    external_operation_id: str
    current_traffic_percent: int
    revision: int


class DirectTrafficAdapter(Protocol):
    name: str
    implementation_version: str
    provider_instance_sha256: str
    environment_sha256: str
    supports_idempotent_apply: bool
    supports_lookup: bool
    supports_compare_and_swap: bool
    certification_isolation_mode: str

    def operational_state_sha256(self) -> str: ...

    def certification_namespace_sha256(self) -> str: ...

    def initialize_certification_challenge(
        self, challenge_id: str, initial_traffic_percent: int
    ) -> AdapterTrafficState: ...

    def read_certification_state(self, challenge_id: str) -> AdapterTrafficState: ...

    def apply_certification_change(
        self, request: AdapterChangeRequest
    ) -> AdapterChangeResult: ...

    def lookup_certification_change(
        self, idempotency_key: str
    ) -> AdapterChangeResult | None: ...
