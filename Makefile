SHELL := /bin/sh
PYTHON := $(if $(wildcard workers/python/.venv/bin/python),workers/python/.venv/bin/python,python3)
GOCACHE ?= /tmp/ai-companion-go-cache
ENV_FILE ?= .env
API_BASE_URL ?= http://127.0.0.1:8080
AGENT_PERFORMANCE_LOG ?= evals/agent/performance/canary.sample.jsonl
AGENT_RETRY_LOG ?= evals/agent/retry/canary.sample.jsonl
AGENT_RETRY_TEST_COUNT ?= 1
OBSERVABILITY_BASELINE ?= evals/observability/baselines/release.v1.json
OBSERVABILITY_BEFORE ?= evals/observability/snapshots/before.sample.json
OBSERVABILITY_AFTER ?= evals/observability/snapshots/after.sample.json
OBSERVABILITY_SNAPSHOT ?= artifacts/observability-eval/snapshot.json
OBSERVABILITY_CANARY_SPEC ?= evals/observability/canaries/api-health.v1.json
OBSERVABILITY_GATE_ROOT ?= artifacts/observability-eval
OBSERVABILITY_CANARY_LEASE_ROOT ?= $(OBSERVABILITY_GATE_ROOT)/.canary-leases
OBSERVABILITY_GATE_HISTORY_LEDGER ?= $(OBSERVABILITY_GATE_ROOT)/history.sqlite3
OBSERVABILITY_GATE_TREND_REPORT ?= $(OBSERVABILITY_GATE_ROOT)/history-trend.json
OBSERVABILITY_ENVIRONMENT_ID ?= $(METRICS_URL)
OBSERVABILITY_DIRECT_ROLLOUT_POLICY ?= evals/agent/baselines/direct-rollout.v1.json
OBSERVABILITY_DIRECT_ROLLOUT_CURRENT_TRAFFIC ?= 0
OBSERVABILITY_DIRECT_ROLLOUT_ROOT ?= artifacts/agent-direct-rollout
OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR ?=
OBSERVABILITY_DIRECT_ROLLOUT_ATTESTATION_TTL ?= 900
OBSERVABILITY_DIRECT_ROLLOUT_ATTESTATION_MAX_AGE ?= 900
OBSERVABILITY_DIRECT_ROLLOUT_REQUIRED_DECISION ?= expand
OBSERVABILITY_DIRECT_ROLLOUT_SANDBOX_LEDGER ?= artifacts/agent-direct-rollout-sandbox/traffic.sqlite3
OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE ?= local-direct-traffic-sandbox
OBSERVABILITY_DIRECT_ROLLOUT_INITIAL_TRAFFIC ?= 0
OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_LEDGER ?= artifacts/agent-direct-traffic-certification/provider.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_NAMESPACE ?= isolated-certification-v1
OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_ROOT ?= artifacts/agent-direct-traffic-certificates
OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_NONCE ?=
OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_TTL ?= 3600
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_HISTORY_LEDGER ?= artifacts/agent-direct-traffic-shadow/history.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_NAME ?=
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_BASE_URL ?=
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_ALLOWED_HOST ?=
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_STATE_PATH ?= /v1/direct-traffic/state
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_TIMEOUT ?= 5
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_MAX_ATTEMPTS ?= 2
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_RETRY_BASE ?= 0.25
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_MAX_SNAPSHOT_AGE ?= 90
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_DIR ?=
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_FAST_RUNS ?= 3
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_STABLE_RUNS ?= 30
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_MINIMUM_WINDOW ?= 900
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_MAXIMUM_GAP ?= 90
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_MAXIMUM_AGE ?= 90
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_MAXIMUM_RETRIES ?= 3
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_ATTESTATION_TTL ?= 900
OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_ATTESTATION_MAX_AGE ?= 900
OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_NAMESPACE ?=
OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_BASE_URL ?=
OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_ALLOWED_HOST ?=
OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_TIMEOUT ?= 5
OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_DRILL_ROOT ?= artifacts/agent-direct-traffic-preproduction-drills
OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_DRILL_BUNDLE ?=
OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_DRILL_TTL ?= 3600
OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_DRILL_MAX_AGE ?= 3600
OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_DRILL_REQUIRE_MODE ?= any
OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_CERTIFICATION_LEDGER ?= artifacts/agent-direct-traffic-preproduction-certification/provider.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_CERTIFICATION_NAMESPACE ?= isolated-preproduction-certification-v1
OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_CERTIFICATION_ROOT ?= artifacts/agent-direct-traffic-preproduction-certificates
OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_CERTIFICATION_NONCE ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_BASE_URL ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ALLOWED_HOST ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_TIMEOUT ?= 5
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SANDBOX_LEDGER ?= artifacts/agent-direct-traffic-production/traffic.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION_LEDGER ?= artifacts/agent-direct-traffic-production-certification/provider.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION_NAMESPACE ?= isolated-production-certification-v1
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION_ROOT ?= artifacts/agent-direct-traffic-production-certificates
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION_NONCE ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SHADOW_HISTORY_LEDGER ?= artifacts/agent-direct-traffic-production-shadow/history.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SHADOW_GATE_DIR ?= artifacts/agent-direct-traffic-production-shadow-gate
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_LOCAL_DRILL_BUNDLE ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_LIVE_PROBE_BUNDLE ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SHADOW_ATTESTATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ROLLOUT_ATTESTATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_ROOT ?= artifacts/agent-direct-traffic-production-gate
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_DIR ?= $(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_ROOT)
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_TTL ?= 900
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_MAX_AGE ?= 900
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ACTIVATION_RECEIPT ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION_LEDGER ?= artifacts/agent-direct-traffic-production-emergency-certification/provider.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION_NAMESPACE ?= isolated-production-emergency-certification-v1
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION_ROOT ?= artifacts/agent-direct-traffic-production-emergency-certificates
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION_NONCE ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_ROLLOUT_ATTESTATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_ROOT ?= artifacts/agent-direct-traffic-production-emergency-gate
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_DIR ?= $(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_ROOT)
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_TTL ?= 300
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_MAX_AGE ?= 300
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_INCIDENT_ID ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_AUTHORIZED_BY ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_ROLLBACK_RECEIPT ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION_LEDGER ?= artifacts/agent-direct-traffic-production-recovery-certification/provider.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION_NAMESPACE ?= isolated-production-recovery-certification-v1
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION_ROOT ?= artifacts/agent-direct-traffic-production-recovery-certificates
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION_NONCE ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_ROLLOUT_ATTESTATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_SHADOW_HISTORY_LEDGER ?= artifacts/agent-direct-traffic-production-recovery-shadow/history.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_SHADOW_GATE_DIR ?= artifacts/agent-direct-traffic-production-recovery-shadow-gate
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_SHADOW_ATTESTATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_ROOT ?= artifacts/agent-direct-traffic-production-recovery-gate
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_DIR ?= $(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_ROOT)
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_TTL ?= 300
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_MAX_AGE ?= 900
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_MINIMUM_COOLDOWN ?= 600
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_RECOVERY_RECEIPT ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION_LEDGER ?= artifacts/agent-direct-traffic-production-expansion-certification/provider.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION_NAMESPACE ?= isolated-production-expansion-certification-v1
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION_ROOT ?= artifacts/agent-direct-traffic-production-expansion-certificates
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION_NONCE ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_ROLLOUT_ATTESTATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_SHADOW_HISTORY_LEDGER ?= artifacts/agent-direct-traffic-production-expansion-shadow/history.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_SHADOW_GATE_DIR ?= artifacts/agent-direct-traffic-production-expansion-shadow-gate
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_SHADOW_ATTESTATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_ROOT ?= artifacts/agent-direct-traffic-production-expansion-gate
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_DIR ?= $(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_ROOT)
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_TTL ?= 300
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_MAX_AGE ?= 900
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_MINIMUM_HEALTH_WINDOW ?= 600
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_PRIOR_RECEIPT ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION_LEDGER ?= artifacts/agent-direct-traffic-production-expansion-25-certification/provider.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION_NAMESPACE ?= isolated-production-expansion-25-certification-v1
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION_ROOT ?= artifacts/agent-direct-traffic-production-expansion-25-certificates
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION_NONCE ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_ROLLOUT_ATTESTATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_SHADOW_HISTORY_LEDGER ?= artifacts/agent-direct-traffic-production-expansion-25-shadow/history.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_SHADOW_GATE_DIR ?= artifacts/agent-direct-traffic-production-expansion-25-shadow-gate
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_SHADOW_ATTESTATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_GATE_ROOT ?= artifacts/agent-direct-traffic-production-expansion-25-gate
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_GATE_DIR ?= $(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_GATE_ROOT)
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_GATE_TTL ?= 300
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_GATE_MAX_AGE ?= 900
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_MINIMUM_HEALTH_WINDOW ?= 600
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_PRIOR_RECEIPT ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION_LEDGER ?= artifacts/agent-direct-traffic-production-expansion-50-certification/provider.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION_NAMESPACE ?= isolated-production-expansion-50-certification-v1
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION_ROOT ?= artifacts/agent-direct-traffic-production-expansion-50-certificates
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION_NONCE ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_ROLLOUT_ATTESTATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_SHADOW_HISTORY_LEDGER ?= artifacts/agent-direct-traffic-production-expansion-50-shadow/history.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_SHADOW_GATE_DIR ?= artifacts/agent-direct-traffic-production-expansion-50-shadow-gate
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_SHADOW_ATTESTATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_GATE_ROOT ?= artifacts/agent-direct-traffic-production-expansion-50-gate
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_GATE_DIR ?= $(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_GATE_ROOT)
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_GATE_TTL ?= 300
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_GATE_MAX_AGE ?= 900
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_MINIMUM_HEALTH_WINDOW ?= 600
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_PRIOR_RECEIPT ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION_LEDGER ?= artifacts/agent-direct-traffic-production-expansion-100-certification/provider.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION_NAMESPACE ?= isolated-production-expansion-100-certification-v1
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION_ROOT ?= artifacts/agent-direct-traffic-production-expansion-100-certificates
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION_NONCE ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_ROLLOUT_ATTESTATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_SHADOW_HISTORY_LEDGER ?= artifacts/agent-direct-traffic-production-expansion-100-shadow/history.sqlite3
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_SHADOW_GATE_DIR ?= artifacts/agent-direct-traffic-production-expansion-100-shadow-gate
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_SHADOW_ATTESTATION ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_GATE_ROOT ?= artifacts/agent-direct-traffic-production-expansion-100-gate
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_GATE_DIR ?= $(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_GATE_ROOT)
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_GATE_TTL ?= 300
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_GATE_MAX_AGE ?= 900
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_MINIMUM_HEALTH_WINDOW ?= 600
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_DRILL_ROOT ?= artifacts/agent-direct-traffic-production-drills
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_DRILL_BUNDLE ?=
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_DRILL_TTL ?= 3600
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_DRILL_MAX_AGE ?= 3600
OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_DRILL_TIMEOUT ?= 120
OBSERVABILITY_GATE_DIR ?=
OBSERVABILITY_ATTESTATION_MAX_AGE ?= 900
OBSERVABILITY_REQUIRED_DECISION ?= promote
OBSERVABILITY_DEPLOYMENT_ID ?=
OBSERVABILITY_AUTHORIZATION_STATE_DIR ?= artifacts/observability-deployment-authorizations
OBSERVABILITY_AUTHORIZATION_TTL ?= 300
OBSERVABILITY_AUTHORIZATION ?= $(OBSERVABILITY_AUTHORIZATION_STATE_DIR)/$(OBSERVABILITY_DEPLOYMENT_ID).authorization.json
OBSERVABILITY_DEPLOYMENT_LEDGER ?= artifacts/observability-deployment-controller/ledger.sqlite3
OBSERVABILITY_DEPLOYMENT_TARGET ?= local-dry-run
OBSERVABILITY_DEPLOYMENT_LEASE ?= 30
OBSERVABILITY_RESOLUTION_STATE_DIR ?= artifacts/observability-deployment-resolutions
OBSERVABILITY_RESOLUTION_EVIDENCE ?=
OBSERVABILITY_RESOLUTION_REQUEST ?=
OBSERVABILITY_RESOLUTION_APPROVAL ?=
OBSERVABILITY_RESOLUTION_STATUS ?=
OBSERVABILITY_RESOLUTION_REASON ?=
OBSERVABILITY_RESOLUTION_REQUESTED_BY ?=
OBSERVABILITY_RESOLUTION_APPROVED_BY ?=
OBSERVABILITY_PROVIDER_EVIDENCE_SHA256 ?=
OBSERVABILITY_RESOLUTION_REQUEST_TTL ?= 300
OBSERVABILITY_RESOLUTION_APPROVAL_TTL ?= 180
OBSERVABILITY_SANDBOX_PROVIDER_LEDGER ?= artifacts/observability-provider-sandbox/provider.sqlite3
OBSERVABILITY_ADAPTER_CERTIFICATION_STATE_DIR ?= artifacts/observability-adapter-certifications
OBSERVABILITY_ADAPTER_CERTIFICATION ?=
OBSERVABILITY_ADAPTER_CERTIFICATION_NONCE ?=
OBSERVABILITY_ADAPTER_CERTIFICATION_TTL ?= 3600
OBSERVABILITY_RECONCILIATION_BATCH_LIMIT ?= 100
OBSERVABILITY_DEPLOYMENT_SCHEDULER_INTERVAL ?= 5
OBSERVABILITY_DEPLOYMENT_SCHEDULER_MAX_FAILURES ?= 3
OBSERVABILITY_DEPLOYMENT_SCHEDULER_PORT ?= 9465
OBSERVABILITY_SHADOW_PROVIDER_NAME ?=
OBSERVABILITY_SHADOW_PROVIDER_BASE_URL ?=
OBSERVABILITY_SHADOW_PROVIDER_ALLOWED_HOST ?=
OBSERVABILITY_SHADOW_PROVIDER_INSTANCE ?=
OBSERVABILITY_SHADOW_PROVIDER_TIMEOUT ?= 5
OBSERVABILITY_SHADOW_PROVIDER_MAX_ATTEMPTS ?= 2
OBSERVABILITY_SHADOW_PROVIDER_RETRY_BASE ?= 0.25
OBSERVABILITY_SHADOW_INTERVAL ?= 30
OBSERVABILITY_SHADOW_LIMIT ?= 100
OBSERVABILITY_SHADOW_MAX_FAILURES ?= 3
OBSERVABILITY_SHADOW_PORT ?= 9466
OBSERVABILITY_SHADOW_STATE_DIR ?= artifacts/observability-deployment-shadow
OBSERVABILITY_SHADOW_HISTORY_LEDGER ?= $(OBSERVABILITY_SHADOW_STATE_DIR)/history.sqlite3
OBSERVABILITY_SHADOW_PROVIDER_INSTANCE_SHA256 ?=
OBSERVABILITY_SHADOW_GATE_DIR ?=
OBSERVABILITY_SHADOW_GATE_MINIMUM_RUNS ?= 30
OBSERVABILITY_SHADOW_GATE_MINIMUM_WINDOW ?= 900
OBSERVABILITY_SHADOW_GATE_MINIMUM_SELECTED ?= 10
OBSERVABILITY_SHADOW_GATE_MAXIMUM_GAP ?= 90
OBSERVABILITY_SHADOW_GATE_MAXIMUM_AGE ?= 90
OBSERVABILITY_SHADOW_GATE_ATTESTATION_TTL ?= 900
OBSERVABILITY_SHADOW_GATE_ATTESTATION_MAX_AGE ?= 900
AGENT_METRICS_URL ?= http://127.0.0.1:9467/metrics
AGENT_OBSERVABILITY_CANARY_CYCLES ?= 5
AGENT_OBSERVABILITY_CANARY_INTERVAL ?= 100ms
AGENT_OBSERVABILITY_CANARY_BASELINE ?= evals/agent/baselines/observability-canary.v1.json
AGENT_OBSERVABILITY_CANARY_JSON_REPORT ?= artifacts/agent-eval/observability-canary.json
AGENT_OBSERVABILITY_CANARY_JUNIT_REPORT ?= artifacts/agent-eval/observability-canary.xml
AGENT_OBSERVABILITY_CANARY_BIN ?= bin/agent-observability-canary
AGENT_DIRECT_CANARY_BASELINE ?= evals/agent/baselines/direct-canary.v1.json
AGENT_DIRECT_CANARY_SUITE ?= evals/agent/suites/direct-canary.v1.json
AGENT_DIRECT_CANARY_JSON_REPORT ?= artifacts/agent-eval/direct-canary.json
AGENT_DIRECT_CANARY_JUNIT_REPORT ?= artifacts/agent-eval/direct-canary.xml
METRICS_URL ?=
METRICS_INPUT ?=

.DEFAULT_GOAL := help

.PHONY: help fmt test test-go test-python test-web eval-m2 eval-m4 build build-go run-api run-worker migrate infra-up infra-up-kafka-scale infra-down infra-config release-check release-evidence validate-release-evidence test-release-evidence-validator check docker-up-kafka-scale
.PHONY: certify-agent-direct-traffic-production-recovery-adapter verify-agent-direct-traffic-production-recovery-adapter attest-agent-direct-traffic-production-recovery-rollout evaluate-agent-direct-traffic-production-recovery-gate attest-agent-direct-traffic-production-recovery-gate verify-agent-direct-traffic-production-recovery-gate apply-agent-direct-traffic-production-recovery
.PHONY: certify-agent-direct-traffic-production-expansion-adapter verify-agent-direct-traffic-production-expansion-adapter attest-agent-direct-traffic-production-expansion-rollout evaluate-agent-direct-traffic-production-expansion-gate attest-agent-direct-traffic-production-expansion-gate verify-agent-direct-traffic-production-expansion-gate apply-agent-direct-traffic-production-expansion
.PHONY: certify-agent-direct-traffic-production-expansion-25-adapter verify-agent-direct-traffic-production-expansion-25-adapter attest-agent-direct-traffic-production-expansion-25-rollout evaluate-agent-direct-traffic-production-expansion-25-gate attest-agent-direct-traffic-production-expansion-25-gate verify-agent-direct-traffic-production-expansion-25-gate apply-agent-direct-traffic-production-expansion-25
.PHONY: certify-agent-direct-traffic-production-expansion-50-adapter verify-agent-direct-traffic-production-expansion-50-adapter attest-agent-direct-traffic-production-expansion-50-rollout evaluate-agent-direct-traffic-production-expansion-50-gate attest-agent-direct-traffic-production-expansion-50-gate verify-agent-direct-traffic-production-expansion-50-gate apply-agent-direct-traffic-production-expansion-50
.PHONY: certify-agent-direct-traffic-production-expansion-100-adapter verify-agent-direct-traffic-production-expansion-100-adapter attest-agent-direct-traffic-production-expansion-100-rollout evaluate-agent-direct-traffic-production-expansion-100-gate attest-agent-direct-traffic-production-expansion-100-gate verify-agent-direct-traffic-production-expansion-100-gate apply-agent-direct-traffic-production-expansion-100
.PHONY: drill-agent-direct-traffic-production-full verify-agent-direct-traffic-production-drill

help: ## Show available commands
	@awk 'BEGIN {FS = ":.*## "; printf "Usage: make <target>\n\n"} /^[a-zA-Z_-]+:.*## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: ## Format Go and Python source
	gofmt -w $$(find cmd internal -name '*.go' -type f)
	$(PYTHON) -m compileall -q workers/python/src workers/python/tests

test: test-go test-python ## Run tests that do not require platform SDKs

test-go: ## Run Go tests
	GOCACHE=$(GOCACHE) go test ./...

test-python: ## Run Python worker tests
	PYTHONPATH=workers/python/src $(PYTHON) -m unittest discover -s workers/python/tests -v

test-web: ## Type-check and lint the web app
	cd web && pnpm check

eval-m2: ## Run the 100-case M2 retrieval quality gate
	GOCACHE=$(GOCACHE) go test -count=1 -v ./internal/evaluation

eval-m4: ## Run the M4 intent-routing quality gate
	GOCACHE=$(GOCACHE) go test -count=1 -v ./internal/router

eval-agent-contract: ## Run deterministic Agent path and result contracts
	mkdir -p artifacts/agent-eval
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.runner --mode contract --suite evals/agent/suites/pr.jsonl --baseline evals/agent/baselines/pr.v1.json --json-report artifacts/agent-eval/contract.json --junit-report artifacts/agent-eval/contract.xml

eval-agent-replay: ## Replay the pinned Agent decision and tool tapes
	mkdir -p artifacts/agent-eval
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.runner --mode replay --suite evals/agent/suites/pr.jsonl --baseline evals/agent/baselines/pr.v1.json --json-report artifacts/agent-eval/replay.json --junit-report artifacts/agent-eval/replay.xml

eval-agent-performance: ## Evaluate queue, runtime-pool and direct-path performance logs
	mkdir -p artifacts/agent-eval
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.performance --input $(AGENT_PERFORMANCE_LOG) --baseline evals/agent/baselines/performance.v2.json --json-report artifacts/agent-eval/performance.json --junit-report artifacts/agent-eval/performance.xml

eval-agent-retry: ## Evaluate bounded retry scheduling and recovery logs
	mkdir -p artifacts/agent-eval
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.retry --input $(AGENT_RETRY_LOG) --baseline evals/agent/baselines/retry.v1.json --json-report artifacts/agent-eval/retry.json --junit-report artifacts/agent-eval/retry.xml

eval-agent: eval-agent-contract eval-agent-replay eval-agent-performance eval-agent-retry ## Run all offline Agent quality, performance and retry gates

eval-observability-release: ## Compare sanitized before/after release metrics snapshots
	mkdir -p artifacts/observability-eval
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.observability compare --before $(OBSERVABILITY_BEFORE) --after $(OBSERVABILITY_AFTER) --baseline $(OBSERVABILITY_BASELINE) --json-report artifacts/observability-eval/report.json --junit-report artifacts/observability-eval/report.xml

capture-observability: ## Capture a sanitized snapshot from METRICS_URL or METRICS_INPUT
	@test -n "$(METRICS_URL)" || test -n "$(METRICS_INPUT)" || (echo "METRICS_URL or METRICS_INPUT is required" >&2; exit 2)
	@test -z "$(METRICS_URL)" || test -z "$(METRICS_INPUT)" || (echo "set only one of METRICS_URL or METRICS_INPUT" >&2; exit 2)
	@if [ -n "$(METRICS_INPUT)" ]; then \
		PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.observability capture --input "$(METRICS_INPUT)" --output "$(OBSERVABILITY_SNAPSHOT)"; \
	else \
		PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.observability capture --url "$(METRICS_URL)" --output "$(OBSERVABILITY_SNAPSHOT)"; \
	fi

observability-release-gate: ## Run before snapshot, Canary, after snapshot and promote/rollback decision
	@test -n "$(METRICS_URL)" || (echo "METRICS_URL is required" >&2; exit 2)
	mkdir -p "$(OBSERVABILITY_GATE_ROOT)"
	chmod 700 "$(OBSERVABILITY_GATE_ROOT)"
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.release_gate --metrics-url "$(METRICS_URL)" --baseline "$(OBSERVABILITY_BASELINE)" --canary-spec "$(OBSERVABILITY_CANARY_SPEC)" --output-root "$(OBSERVABILITY_GATE_ROOT)" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)"

observability-release-trend: ## Verify the Gate history hash chain and emit a recent trend report
	@test -n "$(OBSERVABILITY_ENVIRONMENT_ID)" || (echo "OBSERVABILITY_ENVIRONMENT_ID is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.release_gate_history --ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --output "$(OBSERVABILITY_GATE_TREND_REPORT)"

evaluate-agent-direct-rollout: ## Recommend one auditable direct-traffic stage from multi-window Gate history
	@test -n "$(OBSERVABILITY_ENVIRONMENT_ID)" || (echo "OBSERVABILITY_ENVIRONMENT_ID is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_rollout evaluate --history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --current-traffic-percent "$(OBSERVABILITY_DIRECT_ROLLOUT_CURRENT_TRAFFIC)" --output-root "$(OBSERVABILITY_DIRECT_ROLLOUT_ROOT)"

attest-agent-direct-rollout: ## Sign an immutable direct rollout decision using a dedicated secret-manager key
	@test -n "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" || (echo "OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_rollout attest --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --ttl-seconds "$(OBSERVABILITY_DIRECT_ROLLOUT_ATTESTATION_TTL)"

verify-agent-direct-rollout: ## Recompute history and verify a fresh signed direct rollout decision
	@test -n "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" || (echo "OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_rollout verify --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --max-age-seconds "$(OBSERVABILITY_DIRECT_ROLLOUT_ATTESTATION_MAX_AGE)" --require-decision "$(OBSERVABILITY_DIRECT_ROLLOUT_REQUIRED_DECISION)"

initialize-agent-direct-rollout-sandbox: ## Initialize the local-only direct traffic adapter at an exact policy stage
	@test -n "$(OBSERVABILITY_ENVIRONMENT_ID)" || (echo "OBSERVABILITY_ENVIRONMENT_ID is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_rollout_traffic initialize --sandbox-ledger "$(OBSERVABILITY_DIRECT_ROLLOUT_SANDBOX_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --initial-traffic-percent "$(OBSERVABILITY_DIRECT_ROLLOUT_INITIAL_TRAFFIC)"

status-agent-direct-rollout-sandbox: ## Verify the local traffic hash chain and show its materialized state
	@test -n "$(OBSERVABILITY_ENVIRONMENT_ID)" || (echo "OBSERVABILITY_ENVIRONMENT_ID is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_rollout_traffic status --sandbox-ledger "$(OBSERVABILITY_DIRECT_ROLLOUT_SANDBOX_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)"

plan-agent-direct-rollout-traffic: ## Reverify and preview a signed decision without changing sandbox traffic
	@test -n "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" || (echo "OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_rollout_traffic plan --sandbox-ledger "$(OBSERVABILITY_DIRECT_ROLLOUT_SANDBOX_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --max-age-seconds "$(OBSERVABILITY_DIRECT_ROLLOUT_ATTESTATION_MAX_AGE)" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)"

certify-agent-direct-traffic-adapter: ## Prove and sign the direct traffic adapter contract in an isolated namespace
	@test -n "$(OBSERVABILITY_ENVIRONMENT_ID)" || (echo "OBSERVABILITY_ENVIRONMENT_ID is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_NONCE)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_NONCE is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_certification certify-sandbox --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_NAMESPACE)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_ROOT)" --nonce "$(OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_NONCE)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_TTL)"

verify-agent-direct-traffic-adapter: ## Verify a fresh direct traffic adapter certificate
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_certification verify-sandbox --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_NAMESPACE)" --certification "$(OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION)"

apply-agent-direct-rollout-sandbox: ## Apply a fresh signed non-hold decision to the local sandbox atomically
	@test -n "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" || (echo "OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_rollout_traffic apply --sandbox-ledger "$(OBSERVABILITY_DIRECT_ROLLOUT_SANDBOX_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --max-age-seconds "$(OBSERVABILITY_DIRECT_ROLLOUT_ATTESTATION_MAX_AGE)" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION)"

check-agent-direct-traffic-shadow: ## Compare local desired traffic with one read-only HTTPS platform snapshot
	@test -n "$(OBSERVABILITY_ENVIRONMENT_ID)" || (echo "OBSERVABILITY_ENVIRONMENT_ID is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_NAME)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_NAME is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_BASE_URL)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_BASE_URL is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_ALLOWED_HOST)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_ALLOWED_HOST is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_TOKEN)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_TOKEN is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_shadow check --history-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_HISTORY_LEDGER)" --sandbox-ledger "$(OBSERVABILITY_DIRECT_ROLLOUT_SANDBOX_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-name "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_NAME)" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_ALLOWED_HOST)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --state-path "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_STATE_PATH)" --timeout-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_TIMEOUT)" --max-attempts "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_MAX_ATTEMPTS)" --retry-base-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_RETRY_BASE)" --max-snapshot-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_MAX_SNAPSHOT_AGE)" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)" --require-match

status-agent-direct-traffic-shadow: ## Verify and summarize the complete direct-traffic shadow hash chain
	@test -n "$(OBSERVABILITY_ENVIRONMENT_ID)" || (echo "OBSERVABILITY_ENVIRONMENT_ID is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_NAME)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_NAME is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_BASE_URL)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_BASE_URL is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_ALLOWED_HOST)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_ALLOWED_HOST is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_shadow status --history-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-name "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_NAME)" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_ALLOWED_HOST)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --state-path "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_STATE_PATH)"

evaluate-agent-direct-traffic-shadow-gate: ## Evaluate fast and stable zero-drift windows without signing
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_DIR)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_DIR is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_shadow_gate evaluate --history-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-name "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_NAME)" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_ALLOWED_HOST)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --state-path "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_STATE_PATH)" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_DIR)" --fast-window-runs "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_FAST_RUNS)" --stable-window-runs "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_STABLE_RUNS)" --minimum-stable-window-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_MINIMUM_WINDOW)" --maximum-gap-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_MAXIMUM_GAP)" --maximum-report-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_MAXIMUM_AGE)" --maximum-retried-observations "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_MAXIMUM_RETRIES)"

attest-agent-direct-traffic-shadow-gate: ## Sign a passing Gate after replaying it against the live shadow chain
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_DIR)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_DIR is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_KEY)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_KEY is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_KEY_ID)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_KEY_ID is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_shadow_gate attest --history-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-name "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_NAME)" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_ALLOWED_HOST)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --state-path "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_STATE_PATH)" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)" --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_DIR)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_ATTESTATION_TTL)" --max-gate-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_ATTESTATION_MAX_AGE)"

verify-agent-direct-traffic-shadow-gate: ## Verify signature, freshness, identities and the unchanged live chain head
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_DIR)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_DIR is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_KEY)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_KEY is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_KEY_ID)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_KEY_ID is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_shadow_gate verify --history-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-name "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_NAME)" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_ALLOWED_HOST)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --state-path "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_STATE_PATH)" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)" --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_DIR)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_ATTESTATION_MAX_AGE)" --require-decision pass

certify-agent-direct-traffic-preproduction-adapter: ## Certify the HTTPS writer contract in an isolated local namespace
	@test -n "$(OBSERVABILITY_ENVIRONMENT_ID)" || (echo "OBSERVABILITY_ENVIRONMENT_ID is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_CERTIFICATION_NONCE)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_CERTIFICATION_NONCE is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_preproduction_sandbox certify --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_CERTIFICATION_NAMESPACE)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_CERTIFICATION_ROOT)" --nonce "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_CERTIFICATION_NONCE)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_TTL)"

apply-agent-direct-traffic-preproduction: ## Apply one triple-proven CAS change to an explicit preproduction namespace
	@test -n "$(OBSERVABILITY_ENVIRONMENT_ID)" || (echo "OBSERVABILITY_ENVIRONMENT_ID is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_NAMESPACE)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_NAMESPACE is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_BASE_URL)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_BASE_URL is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_ALLOWED_HOST)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_ALLOWED_HOST is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" || (echo "OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_DIR)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_DIR is required" >&2; exit 2)
	@case "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_NAMESPACE)" in preproduction-*) ;; *) echo "namespace must begin with preproduction-" >&2; exit 2;; esac
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_preproduction --sandbox-ledger "$(OBSERVABILITY_DIRECT_ROLLOUT_SANDBOX_LEDGER)" --release-history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION)" --shadow-history-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_HISTORY_LEDGER)" --shadow-gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_DIR)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_NAMESPACE)" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_ALLOWED_HOST)" --timeout-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_TIMEOUT)" --max-age-seconds "$(OBSERVABILITY_DIRECT_ROLLOUT_ATTESTATION_MAX_AGE)" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)"

drill-agent-direct-traffic-preproduction: ## Exercise expansion, rollback and failure recovery with an attested local Provider contract
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_preproduction_drill run-local --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --output-root "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_DRILL_ROOT)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_DRILL_TTL)"

probe-agent-direct-traffic-preproduction: ## Run the real HTTPS Provider state and unknown-key lookup contract without writes
	@test -n "$(OBSERVABILITY_ENVIRONMENT_ID)" || (echo "OBSERVABILITY_ENVIRONMENT_ID is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" || (echo "OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_NAMESPACE)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_NAMESPACE is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_BASE_URL)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_BASE_URL is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_ALLOWED_HOST)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_ALLOWED_HOST is required" >&2; exit 2)
	@case "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_NAMESPACE)" in preproduction-*) ;; *) echo "namespace must begin with preproduction-" >&2; exit 2;; esac
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_preproduction_drill probe --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_NAMESPACE)" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_ALLOWED_HOST)" --timeout-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_TIMEOUT)" --output-root "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_DRILL_ROOT)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_DRILL_TTL)"

verify-agent-direct-traffic-preproduction-drill: ## Verify a signed Provider drill bundle and its freshness
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_DRILL_BUNDLE)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_DRILL_BUNDLE is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_preproduction_drill verify --bundle "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_DRILL_BUNDLE)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_DRILL_MAX_AGE)" --require-mode "$(OBSERVABILITY_DIRECT_TRAFFIC_PREPRODUCTION_DRILL_REQUIRE_MODE)"

initialize-agent-direct-traffic-production-sandbox: ## Initialize the isolated production ledger at exactly 0%
	@case "$(OBSERVABILITY_ENVIRONMENT_ID)" in production-*) ;; *) echo "environment must begin with production-" >&2; exit 2;; esac
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_rollout_traffic initialize --sandbox-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SANDBOX_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --initial-traffic-percent 0

status-agent-direct-traffic-production-sandbox: ## Verify the isolated production ledger hash chain
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_rollout_traffic status --sandbox-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SANDBOX_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)"

certify-agent-direct-traffic-production-adapter: ## Certify the production writer identity with a production-only key
	@test -n "$(OBSERVABILITY_ENVIRONMENT_ID)" || (echo "OBSERVABILITY_ENVIRONMENT_ID is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION_NONCE)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION_NONCE is required" >&2; exit 2)
	@case "$(OBSERVABILITY_ENVIRONMENT_ID)" in production-*) ;; *) echo "environment must begin with production-" >&2; exit 2;; esac
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_sandbox certify --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION_NAMESPACE)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION_ROOT)" --nonce "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION_NONCE)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_CERTIFICATION_TTL)"

verify-agent-direct-traffic-production-adapter: ## Verify a fresh production adapter certificate
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_sandbox verify --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION_NAMESPACE)" --certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION)"

attest-agent-direct-traffic-production-rollout: ## Sign the exact production 0%-to-5% rollout with a production-only key
	@test -n "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" || (echo "OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_rollout attest --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --ttl-seconds "$(OBSERVABILITY_DIRECT_ROLLOUT_ATTESTATION_TTL)" --key-profile production

check-agent-direct-traffic-production-shadow: ## Collect one read-only production namespace comparison
	@case "$(OBSERVABILITY_ENVIRONMENT_ID)" in production-*) ;; *) echo "environment must begin with production-" >&2; exit 2;; esac
	@case "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" in production-*) ;; *) echo "namespace must begin with production-" >&2; exit 2;; esac
	@OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_TOKEN="$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_TOKEN)" PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_shadow check --history-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SHADOW_HISTORY_LEDGER)" --sandbox-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SANDBOX_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-name "https-direct-traffic-production" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ALLOWED_HOST)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --state-path "/v1/namespaces/$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)/direct-traffic/shadow-state" --timeout-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_TIMEOUT)" --max-attempts "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_MAX_ATTEMPTS)" --retry-base-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_PROVIDER_RETRY_BASE)" --max-snapshot-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_MAX_SNAPSHOT_AGE)" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)" --require-match

evaluate-agent-direct-traffic-production-shadow-gate: ## Evaluate the production zero-drift window without signing
	@case "$(OBSERVABILITY_ENVIRONMENT_ID)" in production-*) ;; *) echo "environment must begin with production-" >&2; exit 2;; esac
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_shadow_gate evaluate --history-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SHADOW_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-name "https-direct-traffic-production" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ALLOWED_HOST)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --state-path "/v1/namespaces/$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)/direct-traffic/shadow-state" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SHADOW_GATE_DIR)" --fast-window-runs "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_FAST_RUNS)" --stable-window-runs "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_STABLE_RUNS)" --minimum-stable-window-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_MINIMUM_WINDOW)" --maximum-gap-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_MAXIMUM_GAP)" --maximum-report-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_MAXIMUM_AGE)" --maximum-retried-observations "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_MAXIMUM_RETRIES)"

attest-agent-direct-traffic-production-shadow-gate: ## Sign the production shadow Gate with a production-only key
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_DIR)" || (echo "OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_DIR is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_shadow_gate attest --history-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SHADOW_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-name "https-direct-traffic-production" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ALLOWED_HOST)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --state-path "/v1/namespaces/$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)/direct-traffic/shadow-state" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)" --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SHADOW_GATE_DIR)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_ATTESTATION_TTL)" --max-gate-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_SHADOW_GATE_ATTESTATION_MAX_AGE)" --key-profile production

evaluate-agent-direct-traffic-production-gate: ## Bind both preproduction drills and three production proofs to 0%-to-5%
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_LOCAL_DRILL_BUNDLE)" || (echo "production local drill bundle is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_LIVE_PROBE_BUNDLE)" || (echo "production live probe bundle is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION)" || (echo "production adapter certification is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SHADOW_ATTESTATION)" || (echo "production shadow attestation is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ROLLOUT_ATTESTATION)" || (echo "production rollout attestation is required" >&2; exit 2)
	@case "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" in production-*) ;; *) echo "namespace must begin with production-" >&2; exit 2;; esac
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_gate evaluate --local-drill-bundle "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_LOCAL_DRILL_BUNDLE)" --live-probe-bundle "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_LIVE_PROBE_BUNDLE)" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION)" --shadow-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SHADOW_ATTESTATION)" --rollout-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ROLLOUT_ATTESTATION)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_ROOT)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_MAX_AGE)"

attest-agent-direct-traffic-production-gate: ## Sign a passing fourth production Gate with its isolated key
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_DIR)" || (echo "production Gate directory is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_gate attest --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_DIR)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_TTL)"

verify-agent-direct-traffic-production-gate: ## Verify the fourth Gate signature, bindings and freshness
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_DIR)" || (echo "production Gate directory is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_gate verify --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_DIR)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_MAX_AGE)"

apply-agent-direct-traffic-production: ## Apply the one-time four-proof 0%-to-5% production CAS
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_DIR)" || (echo "production Gate directory is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" || (echo "production rollout decision directory is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION)" || (echo "production adapter certification is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SHADOW_GATE_DIR)" || (echo "production shadow Gate directory is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_BASE_URL)" || (echo "production Provider base URL is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ALLOWED_HOST)" || (echo "production Provider allowed host is required" >&2; exit 2)
	@case "$(OBSERVABILITY_ENVIRONMENT_ID)" in production-*) ;; *) echo "environment must begin with production-" >&2; exit 2;; esac
	@case "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" in production-*) ;; *) echo "namespace must begin with production-" >&2; exit 2;; esac
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production --sandbox-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SANDBOX_LEDGER)" --release-history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_CERTIFICATION)" --shadow-history-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SHADOW_HISTORY_LEDGER)" --shadow-gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SHADOW_GATE_DIR)" --production-gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_DIR)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ALLOWED_HOST)" --timeout-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_TIMEOUT)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_GATE_MAX_AGE)" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)"

certify-agent-direct-traffic-production-emergency-adapter: ## Certify the isolated production 5%-to-0% writer
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION_NONCE)" || (echo "emergency certification nonce is required" >&2; exit 2)
	@case "$(OBSERVABILITY_ENVIRONMENT_ID)" in production-*) ;; *) echo "environment must begin with production-" >&2; exit 2;; esac
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_emergency_sandbox certify --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION_NAMESPACE)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION_ROOT)" --nonce "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION_NONCE)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_TTL)"

verify-agent-direct-traffic-production-emergency-adapter: ## Verify a fresh isolated emergency adapter certificate
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION)" || (echo "emergency adapter certification is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_emergency_sandbox verify --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION_NAMESPACE)" --certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION)"

attest-agent-direct-traffic-production-emergency-rollout: ## Sign a rollback or disable decision with the emergency rollout key
	@test -n "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" || (echo "emergency rollout decision directory is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_rollout attest --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_TTL)" --key-profile production_emergency

evaluate-agent-direct-traffic-production-emergency-gate: ## Bind 5%-to-0%, activation receipt, incident and two emergency proofs
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_ROLLOUT_ATTESTATION)" || (echo "emergency rollout attestation is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION)" || (echo "emergency adapter certification is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ACTIVATION_RECEIPT)" || (echo "production activation receipt is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_INCIDENT_ID)" || (echo "emergency incident ID is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_AUTHORIZED_BY)" || (echo "emergency authorized actor is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_emergency_gate evaluate --rollout-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_ROLLOUT_ATTESTATION)" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION)" --activation-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ACTIVATION_RECEIPT)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" --incident-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_INCIDENT_ID)" --authorized-by "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_AUTHORIZED_BY)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_ROOT)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_MAX_AGE)"

attest-agent-direct-traffic-production-emergency-gate: ## Sign a passing emergency Gate with its third independent proof key
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_emergency_gate attest --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_DIR)" --rollout-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_ROLLOUT_ATTESTATION)" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION)" --activation-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ACTIVATION_RECEIPT)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_TTL)" --max-source-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_MAX_AGE)"

verify-agent-direct-traffic-production-emergency-gate: ## Verify the emergency Gate signature, bindings and freshness
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_emergency_gate verify --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_DIR)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_MAX_AGE)"

apply-agent-direct-traffic-production-emergency: ## Apply the independent no-shadow 5%-to-0% production CAS
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ACTIVATION_RECEIPT)" || (echo "production activation receipt is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION)" || (echo "emergency adapter certification is required" >&2; exit 2)
	@case "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" in production-*) ;; *) echo "namespace must begin with production-" >&2; exit 2;; esac
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_emergency --sandbox-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SANDBOX_LEDGER)" --release-history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_CERTIFICATION)" --emergency-gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_DIR)" --activation-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ACTIVATION_RECEIPT)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ALLOWED_HOST)" --timeout-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_TIMEOUT)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EMERGENCY_GATE_MAX_AGE)" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)"

certify-agent-direct-traffic-production-recovery-adapter: ## Certify the post-rollback production recovery writer
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION_NONCE)" || (echo "recovery certification nonce is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_recovery_sandbox certify --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION_NAMESPACE)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION_ROOT)" --nonce "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION_NONCE)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_MAX_AGE)"

verify-agent-direct-traffic-production-recovery-adapter: ## Verify the isolated recovery adapter certificate
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION)" || (echo "recovery certification is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_recovery_sandbox verify --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION_NAMESPACE)" --certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION)"

attest-agent-direct-traffic-production-recovery-rollout: ## Sign a fresh post-cooldown 0%-to-5% decision
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_rollout attest --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_MAX_AGE)" --key-profile production_recovery

evaluate-agent-direct-traffic-production-recovery-gate: ## Bind cooldown, healthy rollout, recovered shadow and rollback receipt
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_recovery_gate evaluate --rollout-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_ROLLOUT_ATTESTATION)" --rollout-report "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)/direct-rollout-report.json" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION)" --shadow-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_SHADOW_ATTESTATION)" --rollback-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_ROLLBACK_RECEIPT)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_ROOT)" --minimum-cooldown-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_MINIMUM_COOLDOWN)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_MAX_AGE)"

attest-agent-direct-traffic-production-recovery-gate: ## Replay sources and sign a passing recovery Gate
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_recovery_gate attest --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_DIR)" --rollout-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_ROLLOUT_ATTESTATION)" --rollout-report "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)/direct-rollout-report.json" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION)" --shadow-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_SHADOW_ATTESTATION)" --rollback-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_ROLLBACK_RECEIPT)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_TTL)" --max-source-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_MAX_AGE)"

verify-agent-direct-traffic-production-recovery-gate: ## Verify recovery Gate signature, source bindings and freshness
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_recovery_gate verify --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_DIR)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_MAX_AGE)"

apply-agent-direct-traffic-production-recovery: ## Apply only post-rollback revision-2 recovery to 5%
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_recovery --sandbox-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SANDBOX_LEDGER)" --release-history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_CERTIFICATION)" --shadow-history-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_SHADOW_HISTORY_LEDGER)" --shadow-gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_SHADOW_GATE_DIR)" --recovery-gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_DIR)" --rollback-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_ROLLBACK_RECEIPT)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ALLOWED_HOST)" --timeout-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_TIMEOUT)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_RECOVERY_GATE_MAX_AGE)" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)"

certify-agent-direct-traffic-production-expansion-adapter: ## Certify the isolated production 5%-to-10% writer
	@test -n "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION_NONCE)" || (echo "expansion certification nonce is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_sandbox certify --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION_NAMESPACE)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION_ROOT)" --nonce "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION_NONCE)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_MAX_AGE)"

verify-agent-direct-traffic-production-expansion-adapter: ## Verify the isolated production expansion certificate
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_sandbox verify --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION_NAMESPACE)" --certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION)"

attest-agent-direct-traffic-production-expansion-rollout: ## Sign a fresh production 5%-to-10% decision
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_rollout attest --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_MAX_AGE)" --key-profile production_expansion

evaluate-agent-direct-traffic-production-expansion-gate: ## Bind recovery receipt and a complete post-recovery health window
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_gate evaluate --rollout-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_ROLLOUT_ATTESTATION)" --rollout-report "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)/direct-rollout-report.json" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION)" --shadow-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_SHADOW_ATTESTATION)" --shadow-gate-report "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_SHADOW_GATE_DIR)/direct-traffic-shadow-gate-report.json" --recovery-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_RECOVERY_RECEIPT)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_ROOT)" --minimum-health-window-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_MINIMUM_HEALTH_WINDOW)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_MAX_AGE)"

attest-agent-direct-traffic-production-expansion-gate: ## Replay and sign a passing production 5%-to-10% Gate
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_gate attest --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_DIR)" --rollout-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_ROLLOUT_ATTESTATION)" --rollout-report "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)/direct-rollout-report.json" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION)" --shadow-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_SHADOW_ATTESTATION)" --shadow-gate-report "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_SHADOW_GATE_DIR)/direct-traffic-shadow-gate-report.json" --recovery-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_RECOVERY_RECEIPT)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_TTL)" --max-source-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_MAX_AGE)"

verify-agent-direct-traffic-production-expansion-gate: ## Verify production expansion Gate signature and bindings
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_gate verify --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_DIR)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_MAX_AGE)"

apply-agent-direct-traffic-production-expansion: ## Apply only revision-3 production expansion from 5% to 10%
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion --sandbox-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SANDBOX_LEDGER)" --release-history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_CERTIFICATION)" --shadow-history-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_SHADOW_HISTORY_LEDGER)" --shadow-gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_SHADOW_GATE_DIR)" --expansion-gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_DIR)" --recovery-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_RECOVERY_RECEIPT)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ALLOWED_HOST)" --timeout-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_TIMEOUT)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_GATE_MAX_AGE)" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)"

certify-agent-direct-traffic-production-expansion-25-adapter: ## Certify the isolated production 10%-to-25% writer
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_25_sandbox certify --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION_NAMESPACE)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION_ROOT)" --nonce "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION_NONCE)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_GATE_MAX_AGE)"

verify-agent-direct-traffic-production-expansion-25-adapter: ## Verify the production 25% stage certificate
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_25_sandbox verify --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION_NAMESPACE)" --certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION)"

attest-agent-direct-traffic-production-expansion-25-rollout: ## Sign a fresh production 10%-to-25% decision
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_rollout attest --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_GATE_MAX_AGE)" --key-profile production_expansion_25

evaluate-agent-direct-traffic-production-expansion-25-gate: ## Bind the 10% receipt and new health/shadow windows
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_25_gate evaluate --rollout-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_ROLLOUT_ATTESTATION)" --rollout-report "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)/direct-rollout-report.json" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION)" --shadow-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_SHADOW_ATTESTATION)" --shadow-gate-report "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_SHADOW_GATE_DIR)/direct-traffic-shadow-gate-report.json" --prior-expansion-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_PRIOR_RECEIPT)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_GATE_ROOT)" --minimum-health-window-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_MINIMUM_HEALTH_WINDOW)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_GATE_MAX_AGE)"

attest-agent-direct-traffic-production-expansion-25-gate: ## Replay and sign a passing 10%-to-25% Gate
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_25_gate attest --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_GATE_DIR)" --rollout-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_ROLLOUT_ATTESTATION)" --rollout-report "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)/direct-rollout-report.json" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION)" --shadow-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_SHADOW_ATTESTATION)" --shadow-gate-report "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_SHADOW_GATE_DIR)/direct-traffic-shadow-gate-report.json" --prior-expansion-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_PRIOR_RECEIPT)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_GATE_TTL)" --max-source-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_GATE_MAX_AGE)"

verify-agent-direct-traffic-production-expansion-25-gate: ## Verify the 10%-to-25% Gate signature and bindings
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_25_gate verify --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_GATE_DIR)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_GATE_MAX_AGE)"

apply-agent-direct-traffic-production-expansion-25: ## Apply only revision-4 production expansion from 10% to 25%
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_25 --sandbox-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SANDBOX_LEDGER)" --release-history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_CERTIFICATION)" --shadow-history-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_SHADOW_HISTORY_LEDGER)" --shadow-gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_SHADOW_GATE_DIR)" --expansion-gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_GATE_DIR)" --prior-expansion-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_PRIOR_RECEIPT)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ALLOWED_HOST)" --timeout-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_TIMEOUT)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_25_GATE_MAX_AGE)" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)"

certify-agent-direct-traffic-production-expansion-50-adapter: ## Certify the isolated production 25%-to-50% writer
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_50_sandbox certify --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION_NAMESPACE)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION_ROOT)" --nonce "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION_NONCE)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_GATE_MAX_AGE)"

verify-agent-direct-traffic-production-expansion-50-adapter: ## Verify the production 50% stage certificate
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_50_sandbox verify --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION_NAMESPACE)" --certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION)"

attest-agent-direct-traffic-production-expansion-50-rollout: ## Sign a fresh production 25%-to-50% decision
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_rollout attest --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_GATE_MAX_AGE)" --key-profile production_expansion_50

evaluate-agent-direct-traffic-production-expansion-50-gate: ## Bind the 25% receipt and new health/shadow windows
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_50_gate evaluate --rollout-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_ROLLOUT_ATTESTATION)" --rollout-report "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)/direct-rollout-report.json" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION)" --shadow-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_SHADOW_ATTESTATION)" --shadow-gate-report "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_SHADOW_GATE_DIR)/direct-traffic-shadow-gate-report.json" --prior-expansion-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_PRIOR_RECEIPT)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_GATE_ROOT)" --minimum-health-window-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_MINIMUM_HEALTH_WINDOW)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_GATE_MAX_AGE)"

attest-agent-direct-traffic-production-expansion-50-gate: ## Replay and sign a passing 25%-to-50% Gate
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_50_gate attest --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_GATE_DIR)" --rollout-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_ROLLOUT_ATTESTATION)" --rollout-report "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)/direct-rollout-report.json" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION)" --shadow-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_SHADOW_ATTESTATION)" --shadow-gate-report "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_SHADOW_GATE_DIR)/direct-traffic-shadow-gate-report.json" --prior-expansion-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_PRIOR_RECEIPT)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_GATE_TTL)" --max-source-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_GATE_MAX_AGE)"

verify-agent-direct-traffic-production-expansion-50-gate: ## Verify the 25%-to-50% Gate signature and bindings
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_50_gate verify --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_GATE_DIR)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_GATE_MAX_AGE)"

apply-agent-direct-traffic-production-expansion-50: ## Apply only revision-5 production expansion from 25% to 50%
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_50 --sandbox-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SANDBOX_LEDGER)" --release-history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_CERTIFICATION)" --shadow-history-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_SHADOW_HISTORY_LEDGER)" --shadow-gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_SHADOW_GATE_DIR)" --expansion-gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_GATE_DIR)" --prior-expansion-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_PRIOR_RECEIPT)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ALLOWED_HOST)" --timeout-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_TIMEOUT)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_50_GATE_MAX_AGE)" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)"

certify-agent-direct-traffic-production-expansion-100-adapter: ## Certify the isolated production 50%-to-100% writer
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_100_sandbox certify --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION_NAMESPACE)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION_ROOT)" --nonce "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION_NONCE)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_GATE_MAX_AGE)"

verify-agent-direct-traffic-production-expansion-100-adapter: ## Verify the production 100% stage certificate
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_100_sandbox verify --certification-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION_NAMESPACE)" --certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION)"

attest-agent-direct-traffic-production-expansion-100-rollout: ## Sign a fresh production 50%-to-100% decision
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_rollout attest --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_GATE_MAX_AGE)" --key-profile production_expansion_100

evaluate-agent-direct-traffic-production-expansion-100-gate: ## Bind the 50% receipt and new health/shadow windows
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_100_gate evaluate --rollout-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_ROLLOUT_ATTESTATION)" --rollout-report "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)/direct-rollout-report.json" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION)" --shadow-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_SHADOW_ATTESTATION)" --shadow-gate-report "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_SHADOW_GATE_DIR)/direct-traffic-shadow-gate-report.json" --prior-expansion-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_PRIOR_RECEIPT)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" --output-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_GATE_ROOT)" --minimum-health-window-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_MINIMUM_HEALTH_WINDOW)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_GATE_MAX_AGE)"

attest-agent-direct-traffic-production-expansion-100-gate: ## Replay and sign a passing 50%-to-100% Gate
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_100_gate attest --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_GATE_DIR)" --rollout-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_ROLLOUT_ATTESTATION)" --rollout-report "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)/direct-rollout-report.json" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION)" --shadow-attestation "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_SHADOW_ATTESTATION)" --shadow-gate-report "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_SHADOW_GATE_DIR)/direct-traffic-shadow-gate-report.json" --prior-expansion-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_PRIOR_RECEIPT)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_GATE_TTL)" --max-source-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_GATE_MAX_AGE)"

verify-agent-direct-traffic-production-expansion-100-gate: ## Verify the 50%-to-100% Gate signature and bindings
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_100_gate verify --gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_GATE_DIR)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_GATE_MAX_AGE)"

apply-agent-direct-traffic-production-expansion-100: ## Apply only revision-6 production expansion from 50% to 100%
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_expansion_100 --sandbox-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_SANDBOX_LEDGER)" --release-history-ledger "$(OBSERVABILITY_GATE_HISTORY_LEDGER)" --decision-dir "$(OBSERVABILITY_DIRECT_ROLLOUT_DECISION_DIR)" --policy "$(OBSERVABILITY_DIRECT_ROLLOUT_POLICY)" --adapter-certification "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_CERTIFICATION)" --shadow-history-ledger "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_SHADOW_HISTORY_LEDGER)" --shadow-gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_SHADOW_GATE_DIR)" --expansion-gate-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_GATE_DIR)" --prior-expansion-receipt "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_PRIOR_RECEIPT)" --environment-id "$(OBSERVABILITY_ENVIRONMENT_ID)" --provider-instance "$(OBSERVABILITY_DIRECT_ROLLOUT_PROVIDER_INSTANCE)" --namespace-id "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_NAMESPACE)" --provider-base-url "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_BASE_URL)" --allowed-host "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_ALLOWED_HOST)" --timeout-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_TIMEOUT)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_EXPANSION_100_GATE_MAX_AGE)" --canary-lease-root "$(OBSERVABILITY_CANARY_LEASE_ROOT)"

drill-agent-direct-traffic-production: ## Run the offline four-proof production contract and failure drill
	PYTHONPATH=workers/python/src $(PYTHON) -m unittest workers/python/tests/test_agent_direct_traffic_production.py

drill-agent-direct-traffic-production-full: ## Produce signed local evidence for the complete production traffic chain
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_drill simulate --repository-root . --output-root "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_DRILL_ROOT)" --timeout-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_DRILL_TIMEOUT)" --ttl-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_DRILL_TTL)"

verify-agent-direct-traffic-production-drill: ## Verify a signed production drill bundle against current sources
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_production_drill verify --repository-root . --bundle-dir "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_DRILL_BUNDLE)" --max-age-seconds "$(OBSERVABILITY_DIRECT_TRAFFIC_PRODUCTION_DRILL_MAX_AGE)"

attest-observability-release-gate: ## Sign a complete private Gate bundle using secret-manager environment values
	@test -n "$(OBSERVABILITY_GATE_DIR)" || (echo "OBSERVABILITY_GATE_DIR is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.release_attestation create --gate-dir "$(OBSERVABILITY_GATE_DIR)" --max-age-seconds "$(OBSERVABILITY_ATTESTATION_MAX_AGE)"

verify-observability-release-gate: ## Verify freshness, integrity and the required deployment decision
	@test -n "$(OBSERVABILITY_GATE_DIR)" || (echo "OBSERVABILITY_GATE_DIR is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.release_attestation verify --gate-dir "$(OBSERVABILITY_GATE_DIR)" --max-age-seconds "$(OBSERVABILITY_ATTESTATION_MAX_AGE)" --require-decision "$(OBSERVABILITY_REQUIRED_DECISION)"

check-observability-deployment: ## Dry-run a signed promote Gate without writing authorization state
	@test -n "$(OBSERVABILITY_GATE_DIR)" || (echo "OBSERVABILITY_GATE_DIR is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DEPLOYMENT_ID)" || (echo "OBSERVABILITY_DEPLOYMENT_ID is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_authorization check --gate-dir "$(OBSERVABILITY_GATE_DIR)" --deployment-id "$(OBSERVABILITY_DEPLOYMENT_ID)" --max-age-seconds "$(OBSERVABILITY_ATTESTATION_MAX_AGE)"

issue-observability-deployment-authorization: ## Explicitly issue or idempotently reuse a short-lived promotion authorization
	@test -n "$(OBSERVABILITY_GATE_DIR)" || (echo "OBSERVABILITY_GATE_DIR is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DEPLOYMENT_ID)" || (echo "OBSERVABILITY_DEPLOYMENT_ID is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_authorization issue --gate-dir "$(OBSERVABILITY_GATE_DIR)" --deployment-id "$(OBSERVABILITY_DEPLOYMENT_ID)" --state-dir "$(OBSERVABILITY_AUTHORIZATION_STATE_DIR)" --max-age-seconds "$(OBSERVABILITY_ATTESTATION_MAX_AGE)" --ttl-seconds "$(OBSERVABILITY_AUTHORIZATION_TTL)"

verify-observability-deployment-authorization: ## Verify a private promotion authorization before a platform adapter consumes it
	@test -n "$(OBSERVABILITY_GATE_DIR)" || (echo "OBSERVABILITY_GATE_DIR is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DEPLOYMENT_ID)" || (echo "OBSERVABILITY_DEPLOYMENT_ID is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_authorization verify --gate-dir "$(OBSERVABILITY_GATE_DIR)" --deployment-id "$(OBSERVABILITY_DEPLOYMENT_ID)" --authorization "$(OBSERVABILITY_AUTHORIZATION)" --max-age-seconds "$(OBSERVABILITY_ATTESTATION_MAX_AGE)"

plan-observability-deployment: ## Read-only plan for the dry-run adapter; does not create a ledger
	@test -n "$(OBSERVABILITY_GATE_DIR)" || (echo "OBSERVABILITY_GATE_DIR is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DEPLOYMENT_ID)" || (echo "OBSERVABILITY_DEPLOYMENT_ID is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_controller plan --gate-dir "$(OBSERVABILITY_GATE_DIR)" --authorization "$(OBSERVABILITY_AUTHORIZATION)" --deployment-id "$(OBSERVABILITY_DEPLOYMENT_ID)" --target "$(OBSERVABILITY_DEPLOYMENT_TARGET)" --max-age-seconds "$(OBSERVABILITY_ATTESTATION_MAX_AGE)"

prepare-observability-deployment: ## Persist an authorized dry-run request in the private transactional ledger
	@test -n "$(OBSERVABILITY_GATE_DIR)" || (echo "OBSERVABILITY_GATE_DIR is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DEPLOYMENT_ID)" || (echo "OBSERVABILITY_DEPLOYMENT_ID is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_controller prepare --gate-dir "$(OBSERVABILITY_GATE_DIR)" --authorization "$(OBSERVABILITY_AUTHORIZATION)" --deployment-id "$(OBSERVABILITY_DEPLOYMENT_ID)" --target "$(OBSERVABILITY_DEPLOYMENT_TARGET)" --ledger "$(OBSERVABILITY_DEPLOYMENT_LEDGER)" --max-age-seconds "$(OBSERVABILITY_ATTESTATION_MAX_AGE)"

simulate-observability-deployment: ## Exercise fenced dispatch through the dry-run adapter without external changes
	@test -n "$(OBSERVABILITY_GATE_DIR)" || (echo "OBSERVABILITY_GATE_DIR is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_DEPLOYMENT_ID)" || (echo "OBSERVABILITY_DEPLOYMENT_ID is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_controller simulate --gate-dir "$(OBSERVABILITY_GATE_DIR)" --authorization "$(OBSERVABILITY_AUTHORIZATION)" --deployment-id "$(OBSERVABILITY_DEPLOYMENT_ID)" --target "$(OBSERVABILITY_DEPLOYMENT_TARGET)" --ledger "$(OBSERVABILITY_DEPLOYMENT_LEDGER)" --lease-seconds "$(OBSERVABILITY_DEPLOYMENT_LEASE)" --max-age-seconds "$(OBSERVABILITY_ATTESTATION_MAX_AGE)"

migrate-observability-deployment-ledger: ## Explicitly create or transactionally upgrade the private ledger to v4
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_controller migrate --ledger "$(OBSERVABILITY_DEPLOYMENT_LEDGER)"

observability-deployment-status: ## Read a deployment operation without creating or changing the ledger
	@test -n "$(OBSERVABILITY_DEPLOYMENT_ID)" || (echo "OBSERVABILITY_DEPLOYMENT_ID is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_controller status --deployment-id "$(OBSERVABILITY_DEPLOYMENT_ID)" --ledger "$(OBSERVABILITY_DEPLOYMENT_LEDGER)"

observability-deployment-health: ## Read due, overdue, missing-schedule and indeterminate reconciliation counts
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_controller health --ledger "$(OBSERVABILITY_DEPLOYMENT_LEDGER)"

certify-observability-sandbox-adapter: ## Exercise and sign the local SQLite provider sandbox contract
	@test -n "$(OBSERVABILITY_ADAPTER_CERTIFICATION_NONCE)" || (echo "OBSERVABILITY_ADAPTER_CERTIFICATION_NONCE is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_scheduler certify-sandbox --provider-ledger "$(OBSERVABILITY_SANDBOX_PROVIDER_LEDGER)" --output-dir "$(OBSERVABILITY_ADAPTER_CERTIFICATION_STATE_DIR)" --nonce "$(OBSERVABILITY_ADAPTER_CERTIFICATION_NONCE)" --ttl-seconds "$(OBSERVABILITY_ADAPTER_CERTIFICATION_TTL)"

run-observability-sandbox-scheduler-once: ## Reconcile due sandbox rows after verifying a fresh adapter certification
	@test -n "$(OBSERVABILITY_ADAPTER_CERTIFICATION)" || (echo "OBSERVABILITY_ADAPTER_CERTIFICATION is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_scheduler run-sandbox-once --ledger "$(OBSERVABILITY_DEPLOYMENT_LEDGER)" --provider-ledger "$(OBSERVABILITY_SANDBOX_PROVIDER_LEDGER)" --certification "$(OBSERVABILITY_ADAPTER_CERTIFICATION)" --limit "$(OBSERVABILITY_RECONCILIATION_BATCH_LIMIT)" --lease-seconds "$(OBSERVABILITY_DEPLOYMENT_LEASE)"

deployment-sandbox-up: ## Build, initialize and start the isolated certified scheduler service
	@test -n "$(OBSERVABILITY_ADAPTER_CERTIFICATION_KEY)" || (echo "OBSERVABILITY_ADAPTER_CERTIFICATION_KEY is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_ADAPTER_CERTIFICATION_KEY_ID)" || (echo "OBSERVABILITY_ADAPTER_CERTIFICATION_KEY_ID is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_ADAPTER_CERTIFICATION_NONCE)" || (echo "OBSERVABILITY_ADAPTER_CERTIFICATION_NONCE is required" >&2; exit 2)
	docker compose --profile deployment-sandbox --env-file $(ENV_FILE) -f deploy/compose/compose.yml up -d --build deployment-sandbox-scheduler

deployment-sandbox-logs: ## Follow the isolated deployment scheduler service
	docker compose --profile deployment-sandbox --env-file $(ENV_FILE) -f deploy/compose/compose.yml logs -f deployment-sandbox-scheduler

deployment-sandbox-down: ## Stop the isolated scheduler while preserving other services and its data
	docker compose --profile deployment-sandbox --env-file $(ENV_FILE) -f deploy/compose/compose.yml stop deployment-sandbox-scheduler

deployment-shadow-check: ## Compare the controller ledger with a read-only HTTPS provider and require an exact match
	@test -n "$(OBSERVABILITY_SHADOW_PROVIDER_NAME)" || (echo "OBSERVABILITY_SHADOW_PROVIDER_NAME is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_SHADOW_PROVIDER_BASE_URL)" || (echo "OBSERVABILITY_SHADOW_PROVIDER_BASE_URL is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_SHADOW_PROVIDER_ALLOWED_HOST)" || (echo "OBSERVABILITY_SHADOW_PROVIDER_ALLOWED_HOST is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_SHADOW_PROVIDER_INSTANCE)" || (echo "OBSERVABILITY_SHADOW_PROVIDER_INSTANCE is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_SHADOW_PROVIDER_TOKEN)" || (echo "OBSERVABILITY_SHADOW_PROVIDER_TOKEN is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_shadow --ledger "$(OBSERVABILITY_DEPLOYMENT_LEDGER)" --state-dir "$(OBSERVABILITY_SHADOW_STATE_DIR)" --provider-name "$(OBSERVABILITY_SHADOW_PROVIDER_NAME)" --provider-base-url "$(OBSERVABILITY_SHADOW_PROVIDER_BASE_URL)" --allowed-host "$(OBSERVABILITY_SHADOW_PROVIDER_ALLOWED_HOST)" --provider-instance "$(OBSERVABILITY_SHADOW_PROVIDER_INSTANCE)" --timeout-seconds "$(OBSERVABILITY_SHADOW_PROVIDER_TIMEOUT)" --max-attempts "$(OBSERVABILITY_SHADOW_PROVIDER_MAX_ATTEMPTS)" --retry-base-seconds "$(OBSERVABILITY_SHADOW_PROVIDER_RETRY_BASE)" --limit "$(OBSERVABILITY_SHADOW_LIMIT)" --once --require-match

deployment-shadow-up: ## Start the read-only pre-production provider shadow observer
	@test -n "$(OBSERVABILITY_SHADOW_PROVIDER_NAME)" || (echo "OBSERVABILITY_SHADOW_PROVIDER_NAME is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_SHADOW_PROVIDER_BASE_URL)" || (echo "OBSERVABILITY_SHADOW_PROVIDER_BASE_URL is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_SHADOW_PROVIDER_ALLOWED_HOST)" || (echo "OBSERVABILITY_SHADOW_PROVIDER_ALLOWED_HOST is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_SHADOW_PROVIDER_INSTANCE)" || (echo "OBSERVABILITY_SHADOW_PROVIDER_INSTANCE is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_SHADOW_PROVIDER_TOKEN)" || (echo "OBSERVABILITY_SHADOW_PROVIDER_TOKEN is required" >&2; exit 2)
	docker compose --profile deployment-shadow --env-file $(ENV_FILE) -f deploy/compose/compose.yml up -d --build deployment-shadow

deployment-shadow-logs: ## Follow the read-only provider shadow observer
	docker compose --profile deployment-shadow --env-file $(ENV_FILE) -f deploy/compose/compose.yml logs -f deployment-shadow

deployment-shadow-down: ## Stop the shadow observer while preserving reports and source ledger
	docker compose --profile deployment-shadow --env-file $(ENV_FILE) -f deploy/compose/compose.yml stop deployment-shadow

evaluate-deployment-shadow-gate: ## Require a stable zero-drift shadow history window
	@test -n "$(OBSERVABILITY_SHADOW_PROVIDER_INSTANCE_SHA256)" || (echo "OBSERVABILITY_SHADOW_PROVIDER_INSTANCE_SHA256 is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_SHADOW_GATE_DIR)" || (echo "OBSERVABILITY_SHADOW_GATE_DIR is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_shadow_gate evaluate --history-ledger "$(OBSERVABILITY_SHADOW_HISTORY_LEDGER)" --provider-instance-sha256 "$(OBSERVABILITY_SHADOW_PROVIDER_INSTANCE_SHA256)" --output-dir "$(OBSERVABILITY_SHADOW_GATE_DIR)" --minimum-runs "$(OBSERVABILITY_SHADOW_GATE_MINIMUM_RUNS)" --minimum-window-seconds "$(OBSERVABILITY_SHADOW_GATE_MINIMUM_WINDOW)" --minimum-selected-total "$(OBSERVABILITY_SHADOW_GATE_MINIMUM_SELECTED)" --maximum-gap-seconds "$(OBSERVABILITY_SHADOW_GATE_MAXIMUM_GAP)" --maximum-report-age-seconds "$(OBSERVABILITY_SHADOW_GATE_MAXIMUM_AGE)" --maximum-drift-total 0 --maximum-lookup-errors-total 0

attest-deployment-shadow-gate: ## Sign a passing shadow gate using secret-manager attestation values
	@test -n "$(OBSERVABILITY_SHADOW_GATE_DIR)" || (echo "OBSERVABILITY_SHADOW_GATE_DIR is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_ATTESTATION_KEY)" || (echo "OBSERVABILITY_ATTESTATION_KEY is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_ATTESTATION_KEY_ID)" || (echo "OBSERVABILITY_ATTESTATION_KEY_ID is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_shadow_gate attest --gate-dir "$(OBSERVABILITY_SHADOW_GATE_DIR)" --ttl-seconds "$(OBSERVABILITY_SHADOW_GATE_ATTESTATION_TTL)"

verify-deployment-shadow-gate: ## Verify freshness, signature, provider binding and a pass decision
	@test -n "$(OBSERVABILITY_SHADOW_GATE_DIR)" || (echo "OBSERVABILITY_SHADOW_GATE_DIR is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_ATTESTATION_KEY)" || (echo "OBSERVABILITY_ATTESTATION_KEY is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_ATTESTATION_KEY_ID)" || (echo "OBSERVABILITY_ATTESTATION_KEY_ID is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_shadow_gate verify --gate-dir "$(OBSERVABILITY_SHADOW_GATE_DIR)" --max-age-seconds "$(OBSERVABILITY_SHADOW_GATE_ATTESTATION_MAX_AGE)" --require-decision pass

create-observability-deployment-resolution-evidence: ## Bind an indeterminate operation to a private provider evidence digest
	@test -n "$(OBSERVABILITY_DEPLOYMENT_ID)" || (echo "OBSERVABILITY_DEPLOYMENT_ID is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_RESOLUTION_STATUS)" || (echo "OBSERVABILITY_RESOLUTION_STATUS is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_RESOLUTION_REASON)" || (echo "OBSERVABILITY_RESOLUTION_REASON is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_RESOLUTION_REQUESTED_BY)" || (echo "OBSERVABILITY_RESOLUTION_REQUESTED_BY is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_PROVIDER_EVIDENCE_SHA256)" || (echo "OBSERVABILITY_PROVIDER_EVIDENCE_SHA256 is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_resolution evidence --ledger "$(OBSERVABILITY_DEPLOYMENT_LEDGER)" --state-dir "$(OBSERVABILITY_RESOLUTION_STATE_DIR)" --deployment-id "$(OBSERVABILITY_DEPLOYMENT_ID)" --proposed-status "$(OBSERVABILITY_RESOLUTION_STATUS)" --reason-code "$(OBSERVABILITY_RESOLUTION_REASON)" --requested-by "$(OBSERVABILITY_RESOLUTION_REQUESTED_BY)" --provider-evidence-sha256 "$(OBSERVABILITY_PROVIDER_EVIDENCE_SHA256)"

request-observability-deployment-resolution: ## Sign a short-lived state-fenced manual resolution request
	@test -n "$(OBSERVABILITY_RESOLUTION_EVIDENCE)" || (echo "OBSERVABILITY_RESOLUTION_EVIDENCE is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_resolution request --ledger "$(OBSERVABILITY_DEPLOYMENT_LEDGER)" --evidence "$(OBSERVABILITY_RESOLUTION_EVIDENCE)" --state-dir "$(OBSERVABILITY_RESOLUTION_STATE_DIR)" --ttl-seconds "$(OBSERVABILITY_RESOLUTION_REQUEST_TTL)"

approve-observability-deployment-resolution: ## Independently approve the exact signed resolution request
	@test -n "$(OBSERVABILITY_RESOLUTION_REQUEST)" || (echo "OBSERVABILITY_RESOLUTION_REQUEST is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_RESOLUTION_APPROVED_BY)" || (echo "OBSERVABILITY_RESOLUTION_APPROVED_BY is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_resolution approve --request "$(OBSERVABILITY_RESOLUTION_REQUEST)" --state-dir "$(OBSERVABILITY_RESOLUTION_STATE_DIR)" --approved-by "$(OBSERVABILITY_RESOLUTION_APPROVED_BY)" --ttl-seconds "$(OBSERVABILITY_RESOLUTION_APPROVAL_TTL)"

check-observability-deployment-resolution: ## Verify dual signatures, freshness, evidence and the live ledger fence
	@test -n "$(OBSERVABILITY_RESOLUTION_EVIDENCE)" || (echo "OBSERVABILITY_RESOLUTION_EVIDENCE is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_RESOLUTION_REQUEST)" || (echo "OBSERVABILITY_RESOLUTION_REQUEST is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_RESOLUTION_APPROVAL)" || (echo "OBSERVABILITY_RESOLUTION_APPROVAL is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_resolution check --ledger "$(OBSERVABILITY_DEPLOYMENT_LEDGER)" --evidence "$(OBSERVABILITY_RESOLUTION_EVIDENCE)" --request "$(OBSERVABILITY_RESOLUTION_REQUEST)" --approval "$(OBSERVABILITY_RESOLUTION_APPROVAL)"

apply-observability-deployment-resolution: ## Atomically consume one dual-signed resolution into the v4 audit ledger
	@test -n "$(OBSERVABILITY_RESOLUTION_EVIDENCE)" || (echo "OBSERVABILITY_RESOLUTION_EVIDENCE is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_RESOLUTION_REQUEST)" || (echo "OBSERVABILITY_RESOLUTION_REQUEST is required" >&2; exit 2)
	@test -n "$(OBSERVABILITY_RESOLUTION_APPROVAL)" || (echo "OBSERVABILITY_RESOLUTION_APPROVAL is required" >&2; exit 2)
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.deployment_resolution apply --ledger "$(OBSERVABILITY_DEPLOYMENT_LEDGER)" --evidence "$(OBSERVABILITY_RESOLUTION_EVIDENCE)" --request "$(OBSERVABILITY_RESOLUTION_REQUEST)" --approval "$(OBSERVABILITY_RESOLUTION_APPROVAL)"

validate-observability: ## Validate retry alerts and Grafana dashboard contracts
	ruby scripts/validate_observability.rb
	ruby scripts/validate_observability.rb --self-test
	$(PYTHON) scripts/observability_drill.py self-test
	PYTHONPATH=workers/python/src $(PYTHON) -m ai_companion_worker.evaluation.direct_traffic_preproduction_drill self-test
	ruby -ryaml -e 'ARGV.each { |path| Psych.parse_file(path) }; puts "observability_yaml=valid"' deploy/observability/prometheus.yml deploy/observability/prometheus-drill.yml deploy/observability/alertmanager-drill.yml deploy/observability/loki.yml

observability-drill: validate-observability ## Exercise Prometheus, Alertmanager, webhook firing and recovery in isolation
	sh scripts/run_observability_drill.sh

agent-observability-canary: ## Gate Agent dispatch and terminal wake metrics without model calls
	AGENT_METRICS_URL=$(AGENT_METRICS_URL) AGENT_OBSERVABILITY_CANARY_CYCLES=$(AGENT_OBSERVABILITY_CANARY_CYCLES) AGENT_OBSERVABILITY_CANARY_INTERVAL=$(AGENT_OBSERVABILITY_CANARY_INTERVAL) AGENT_OBSERVABILITY_CANARY_BASELINE=$(AGENT_OBSERVABILITY_CANARY_BASELINE) AGENT_OBSERVABILITY_CANARY_JSON_REPORT=$(AGENT_OBSERVABILITY_CANARY_JSON_REPORT) AGENT_OBSERVABILITY_CANARY_JUNIT_REPORT=$(AGENT_OBSERVABILITY_CANARY_JUNIT_REPORT) AGENT_OBSERVABILITY_CANARY_BIN=$(AGENT_OBSERVABILITY_CANARY_BIN) sh scripts/run_agent_observability_canary.sh

eval-agent-runtime: ## Evaluate Agent concurrency, backpressure and tool-resume contracts
	mkdir -p artifacts/agent-eval
	GOCACHE=$(GOCACHE) go test -count=1 -json ./internal/agent > artifacts/agent-eval/runtime.jsonl

build: build-go ## Build locally available services

build-go: ## Build Go API and worker
	mkdir -p bin
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/api ./cmd/api
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/worker ./cmd/worker
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/agent-worker ./cmd/agent-worker

run-api: ## Run API locally
	set -a; . ./$(ENV_FILE); set +a; go run ./cmd/api

run-worker: ## Run background worker locally
	set -a; . ./$(ENV_FILE); set +a; go run ./cmd/worker

run-agent-worker: ## Run the database-first LangGraph Agent worker locally
	set -a; . ./$(ENV_FILE); set +a; go run ./cmd/agent-worker

migrate: ## Apply pending legacy MySQL migrations
	set -a; . ./$(ENV_FILE); set +a; go run ./cmd/migrate

postgres-up: ## Start the PostgreSQL migration target
	docker compose --env-file $(ENV_FILE) -f deploy/compose/compose.yml up -d postgres

postgres-migrate: ## Build and apply PostgreSQL target migrations
	docker compose --profile postgres --env-file $(ENV_FILE) -f deploy/compose/compose.yml up --build --abort-on-container-exit migrate-postgres

postgres-copy-core: ## Copy core MySQL data into PostgreSQL and validate primary keys
	set -a; . ./$(ENV_FILE); set +a; CORE_DATA_MIGRATION_MODE=copy go run ./cmd/migrate-core-data

postgres-validate-core: ## Compare core MySQL and PostgreSQL primary-key sets
	set -a; . ./$(ENV_FILE); set +a; CORE_DATA_MIGRATION_MODE=validate go run ./cmd/migrate-core-data

postgres-copy-life: ## Copy ledger, plan and reminder data into PostgreSQL
	set -a; . ./$(ENV_FILE); set +a; DATA_MIGRATION_SCOPE=life CORE_DATA_MIGRATION_MODE=copy go run ./cmd/migrate-core-data

postgres-validate-life: ## Compare life-domain MySQL and PostgreSQL primary-key sets
	set -a; . ./$(ENV_FILE); set +a; DATA_MIGRATION_SCOPE=life CORE_DATA_MIGRATION_MODE=validate go run ./cmd/migrate-core-data

postgres-copy-tools: ## Copy document and skill runtime data into PostgreSQL
	set -a; . ./$(ENV_FILE); set +a; DATA_MIGRATION_SCOPE=tools CORE_DATA_MIGRATION_MODE=copy go run ./cmd/migrate-core-data

postgres-validate-tools: ## Validate document and skill runtime data in PostgreSQL
	set -a; . ./$(ENV_FILE); set +a; DATA_MIGRATION_SCOPE=tools CORE_DATA_MIGRATION_MODE=validate go run ./cmd/migrate-core-data

postgres-copy-governance: ## Copy team, email, billing, safety and operations data into PostgreSQL
	DATA_MIGRATION_SCOPE=governance CORE_DATA_MIGRATION_MODE=copy docker compose --profile postgres-data --env-file $(ENV_FILE) -f deploy/compose/compose.yml run --rm --build migrate-postgres-data

postgres-validate-governance: ## Validate governance and operations data in PostgreSQL
	DATA_MIGRATION_SCOPE=governance CORE_DATA_MIGRATION_MODE=validate docker compose --profile postgres-data --env-file $(ENV_FILE) -f deploy/compose/compose.yml run --rm --build migrate-postgres-data

postgres-test-stores: ## Run PostgreSQL Store integration tests inside Docker
	docker compose --profile postgres --profile postgres-test --env-file $(ENV_FILE) -f deploy/compose/compose.yml run --rm --build postgres-store-test -test.v

postgres-test-agent-retry: ## Verify persisted Agent retry recovery and terminal metrics
	docker compose --profile postgres --profile postgres-test --env-file $(ENV_FILE) -f deploy/compose/compose.yml run --rm --build postgres-store-test -test.run '^(TestCoreStores|TestAgentRetryConcurrentPersistence)$$' -test.count $(AGENT_RETRY_TEST_COUNT) -test.v

agent-checkpoint-setup: ## Create or upgrade LangGraph checkpoint tables
	docker compose --profile agent --env-file $(ENV_FILE) -f deploy/compose/compose.yml run --rm --build agent-checkpoint-setup setup

agent-checkpoint-smoke: ## Verify LangGraph interrupt/resume against PostgreSQL
	docker compose --profile agent --env-file $(ENV_FILE) -f deploy/compose/compose.yml run --rm --build agent-checkpoint-setup smoke

agent-worker-up: ## Start API, Outbox Relay and the dedicated Agent worker
	docker compose --profile app --profile agent --profile agent-runtime --env-file $(ENV_FILE) -f deploy/compose/compose.yml up -d --build api worker agent-worker

agent-worker-logs: ## Follow the dedicated Agent worker
	docker compose --profile app --profile agent --profile agent-runtime --env-file $(ENV_FILE) -f deploy/compose/compose.yml logs -f agent-worker

agent-direct-canary: ## Run disposable no-tool Agent requests and require the direct path
	API_BASE_URL=$(API_BASE_URL) AGENT_DIRECT_CANARY_BASELINE=$(AGENT_DIRECT_CANARY_BASELINE) AGENT_DIRECT_CANARY_SUITE=$(AGENT_DIRECT_CANARY_SUITE) AGENT_DIRECT_CANARY_JSON_REPORT=$(AGENT_DIRECT_CANARY_JSON_REPORT) AGENT_DIRECT_CANARY_JUNIT_REPORT=$(AGENT_DIRECT_CANARY_JUNIT_REPORT) PYTHON=$(PYTHON) sh scripts/run_agent_direct_canary.sh

infra-up: ## Start local infrastructure
	docker compose --env-file .env -f deploy/compose/compose.yml up -d

infra-up-kafka-scale: ## Start local infrastructure plus optional Kafka scale transport
	KAFKA_ENABLED=true KAFKA_BROKERS=kafka:29092 docker compose --profile kafka-scale --env-file $(ENV_FILE) -f deploy/compose/compose.yml up -d

infra-down: ## Stop local infrastructure
	docker compose --env-file .env -f deploy/compose/compose.yml down

infra-config: ## Validate the Compose model
	docker compose --env-file .env.example -f deploy/compose/compose.yml config --quiet

docker-up: ## Build and start the complete Docker application stack
	docker compose --profile app --profile agent --profile agent-runtime --profile observability --env-file $(ENV_FILE) -f deploy/compose/compose.yml up -d --build

docker-up-kafka-scale: ## Start the application with Kafka horizontal-scale dispatch
	KAFKA_ENABLED=true KAFKA_BROKERS=kafka:29092 docker compose --profile app --profile agent --profile agent-runtime --profile kafka-scale --profile observability --env-file $(ENV_FILE) -f deploy/compose/compose.yml up -d --build

docker-down: ## Stop the complete Docker application stack
	docker compose --profile app --profile agent --profile agent-runtime --profile observability --env-file $(ENV_FILE) -f deploy/compose/compose.yml down

docker-logs: ## Follow API, Worker, Agent Worker and Web container logs
	docker compose --profile app --profile agent --profile agent-runtime --profile observability --env-file $(ENV_FILE) -f deploy/compose/compose.yml logs -f api worker agent-worker web loki alloy

docker-ps: ## Show complete Docker application status
	docker compose --profile app --profile agent --profile agent-runtime --profile observability --env-file $(ENV_FILE) -f deploy/compose/compose.yml ps

log-stack-up: ## Start Loki and Alloy structured-log collection
	docker compose --profile observability --env-file $(ENV_FILE) -f deploy/compose/compose.yml up -d loki alloy

log-stack-logs: ## Follow Loki and Alloy logs
	docker compose --profile observability --env-file $(ENV_FILE) -f deploy/compose/compose.yml logs -f loki alloy

release-check: ## Run backend release-readiness static gates
	GOCACHE=$(GOCACHE) go test ./...
	PYTHONPATH=workers/python/src $(PYTHON) -m unittest discover -s workers/python/tests -v
	$(MAKE) eval-agent-replay
	$(MAKE) eval-agent-runtime
	$(MAKE) eval-agent-performance
	$(MAKE) eval-agent-retry
	$(MAKE) eval-observability-release
	$(MAKE) validate-observability
	ruby -ryaml -e 'YAML.load_file("api/openapi/openapi.yaml"); puts "openapi_yaml=valid"'
	docker compose --env-file .env.example -f deploy/compose/compose.yml config --quiet
	sh -n scripts/collect_release_evidence.sh
	sh -n scripts/validate_release_evidence.sh
	sh -n scripts/test_release_evidence_validator.sh
	sh -n scripts/run_agent_direct_canary.sh
	sh -n scripts/run_agent_observability_canary.sh
	sh -n scripts/run_observability_drill.sh
	sh -n scripts/run_api_health_canary.sh
	sh scripts/test_release_evidence_validator.sh
	git diff --check

release-evidence: ## Collect release evidence from a running API
	scripts/collect_release_evidence.sh

validate-release-evidence: ## Validate RELEASE_EVIDENCE_DIR or pass evidence dir via env
	scripts/validate_release_evidence.sh "$$RELEASE_EVIDENCE_DIR"

test-release-evidence-validator: ## Run semantic tests for release evidence validation
	sh scripts/test_release_evidence_validator.sh

check: fmt test build-go infra-config ## Run the M0 local quality gate
