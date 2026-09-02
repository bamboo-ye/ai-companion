ARG BASE_REGISTRY=docker.io/library
FROM ${BASE_REGISTRY}/golang:1.26.4-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG SERVICE=api
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags="-s -w -X github.com/windcry1/ai-companion/internal/buildinfo.Version=${VERSION} -X github.com/windcry1/ai-companion/internal/buildinfo.Commit=${COMMIT} -X github.com/windcry1/ai-companion/internal/buildinfo.Date=${BUILD_DATE}" \
    -o /out/service ./cmd/${SERVICE}

FROM ${BASE_REGISTRY}/python:3.13-slim
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PYTHON_EXECUTABLE=/usr/local/bin/python \
    PYTHON_WORKER_PATH=/app/workers/python/src \
    SPREADSHEET_EXECUTABLE=/usr/local/bin/python \
    SPREADSHEET_WORKER_PATH=/app/workers/python/src/ai_companion_worker/ledger_export.py
WORKDIR /app
COPY workers/python/pyproject.toml ./workers/python/pyproject.toml
COPY workers/python/src ./workers/python/src
RUN apt-get update \
    && apt-get install --no-install-recommends -y fonts-wqy-zenhei poppler-utils \
    && rm -rf /var/lib/apt/lists/* \
    && pip install --no-cache-dir ./workers/python \
    && useradd --create-home --uid 10001 app \
    && mkdir -p /app/.data/files /app/.data/skill-files /app/.data/ledger-exports \
    && chown -R app:app /app/.data
COPY --from=build /out/service /service
USER app
ENTRYPOINT ["/service"]
