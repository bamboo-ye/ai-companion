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
    go test -c -trimpath -o /out/postgresstore.test ./internal/persistence/postgresstore

FROM ${BASE_REGISTRY}/alpine:3.21
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 app
COPY --from=build /out/postgresstore.test /postgresstore.test
USER app
ENTRYPOINT ["/postgresstore.test"]
