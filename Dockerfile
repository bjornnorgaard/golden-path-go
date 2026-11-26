# syntax=docker/dockerfile:1

ARG GO_VERSION=1.27.4

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# Fully static binary: no libc, no shell, nothing but the program.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/webapi ./cmd/webapi

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/webapi /webapi
USER 65532:65532
EXPOSE 8080 9090
# Probes are defined by the platform (/healthz, /readyz); this serves plain `docker run`.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s CMD ["/webapi", "healthcheck"]
ENTRYPOINT ["/webapi"]
