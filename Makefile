SHELL := /bin/sh
PYTHON := $(if $(wildcard workers/python/.venv/bin/python),workers/python/.venv/bin/python,python3)
GOCACHE ?= /tmp/ai-companion-go-cache

.DEFAULT_GOAL := help

.PHONY: help fmt test test-go test-python test-web eval-m2 eval-m4 build build-go run-api run-worker migrate infra-up infra-down infra-config release-check release-evidence validate-release-evidence test-release-evidence-validator check

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

build: build-go ## Build locally available services

build-go: ## Build Go API and worker
	mkdir -p bin
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/api ./cmd/api
	GOCACHE=$(GOCACHE) go build -trimpath -o bin/worker ./cmd/worker

run-api: ## Run API locally
	go run ./cmd/api

run-worker: ## Run background worker locally
	go run ./cmd/worker

migrate: ## Apply pending MySQL migrations
	go run ./cmd/migrate

infra-up: ## Start local infrastructure
	docker compose --env-file .env -f deploy/compose/compose.yml up -d

infra-down: ## Stop local infrastructure
	docker compose --env-file .env -f deploy/compose/compose.yml down

infra-config: ## Validate the Compose model
	docker compose --env-file .env.example -f deploy/compose/compose.yml config --quiet

release-check: ## Run backend release-readiness static gates
	GOCACHE=$(GOCACHE) go test ./...
	PYTHONPATH=workers/python/src $(PYTHON) -m unittest discover -s workers/python/tests -v
	ruby -ryaml -e 'YAML.load_file("api/openapi/openapi.yaml"); puts "openapi_yaml=valid"'
	docker compose --env-file .env.example -f deploy/compose/compose.yml config --quiet
	sh -n scripts/collect_release_evidence.sh
	sh -n scripts/validate_release_evidence.sh
	sh -n scripts/test_release_evidence_validator.sh
	sh scripts/test_release_evidence_validator.sh
	git diff --check

release-evidence: ## Collect release evidence from a running API
	scripts/collect_release_evidence.sh

validate-release-evidence: ## Validate RELEASE_EVIDENCE_DIR or pass evidence dir via env
	scripts/validate_release_evidence.sh "$$RELEASE_EVIDENCE_DIR"

test-release-evidence-validator: ## Run semantic tests for release evidence validation
	sh scripts/test_release_evidence_validator.sh

check: fmt test build-go infra-config ## Run the M0 local quality gate
