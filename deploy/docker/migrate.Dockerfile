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
    go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate \
    && go build -trimpath -ldflags="-s -w" -o /out/migrate-core-data ./cmd/migrate-core-data

FROM ${BASE_REGISTRY}/alpine:3.21
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 app
WORKDIR /app
COPY migrations ./migrations
COPY --from=build /out/migrate /migrate
COPY --from=build /out/migrate-core-data /migrate-core-data
USER app
ENTRYPOINT ["/migrate"]
