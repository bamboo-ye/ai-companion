# syntax=docker/dockerfile:1.7
FROM golang:1.26.4-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG SERVICE=api
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags="-s -w -X github.com/windcry1/ai-companion/internal/buildinfo.Version=${VERSION} -X github.com/windcry1/ai-companion/internal/buildinfo.Commit=${COMMIT} -X github.com/windcry1/ai-companion/internal/buildinfo.Date=${BUILD_DATE}" \
    -o /out/service ./cmd/${SERVICE}

FROM python:3.13.5-slim
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PYTHON_EXECUTABLE=/usr/local/bin/python \
    PYTHON_WORKER_PATH=/app/workers/python/src
WORKDIR /app
COPY workers/python/pyproject.toml ./workers/python/pyproject.toml
COPY workers/python/src ./workers/python/src
RUN pip install --no-cache-dir ./workers/python \
    && useradd --create-home --uid 10001 app \
    && mkdir -p /app/.data/files /app/.data/skill-files \
    && chown -R app:app /app/.data
COPY --from=build /out/service /service
USER app
ENTRYPOINT ["/service"]
