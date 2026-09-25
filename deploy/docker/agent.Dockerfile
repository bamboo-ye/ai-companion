ARG BASE_REGISTRY=docker.io/library
FROM ${BASE_REGISTRY}/golang:1.26.8-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/agent-worker ./cmd/agent-worker \
    && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/healthcheck ./cmd/healthcheck

FROM ${BASE_REGISTRY}/python:3.13-slim
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    LANGGRAPH_STRICT_MSGPACK=true \
    PYTHON_EXECUTABLE=/usr/local/bin/python \
    PYTHON_WORKER_PATH=/app/workers/python/src
WORKDIR /app
COPY workers/python/agent-requirements.txt ./workers/python/agent-requirements.txt
RUN pip install --no-cache-dir -r workers/python/agent-requirements.txt
COPY workers/python/pyproject.toml ./workers/python/pyproject.toml
COPY workers/python/src ./workers/python/src
RUN pip install --no-cache-dir --no-deps ./workers/python \
    && useradd --create-home --uid 10002 agent \
    && mkdir -p /app/.scheduler /app/.shadow \
    && chown -R agent:agent /app/.scheduler /app/.shadow
COPY --from=build /out/agent-worker /agent-worker
COPY --from=build /out/healthcheck /healthcheck
RUN groupadd --gid 10003 modelquota \
    && usermod -a -G modelquota agent \
    && mkdir -p /app/.model-slots \
    && chown root:modelquota /app/.model-slots \
    && chmod 2770 /app/.model-slots
USER agent
ENTRYPOINT ["/agent-worker"]
