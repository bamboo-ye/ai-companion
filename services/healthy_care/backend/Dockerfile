FROM golang:1.26-alpine@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/care ./cmd/server

FROM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8
RUN apk add --no-cache ca-certificates tzdata && addgroup -g 10001 care && adduser -D -u 10001 -G care care && mkdir -p /data/files && chown -R care:care /data
COPY --from=build /out/care /usr/local/bin/care
USER care
EXPOSE 8080
ENTRYPOINT ["care"]
