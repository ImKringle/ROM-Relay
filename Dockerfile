# syntax=docker/dockerfile:1

# ---- build stage ----
# Cross-compiles a static, CGO-free binary. modernc.org/sqlite is pure Go, so
# CGO_ENABLED=0 works and the result runs on distroless/static. BuildKit provides
# TARGETOS/TARGETARCH, so `docker buildx build --platform linux/amd64,linux/arm64`
# produces a multi-arch image (amd64 for most mini PCs, arm64 for ARM boards).
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
# go.mod replaces go-qrcode with a vendored copy under third_party, so that path
# must exist before `go mod download` can resolve the module graph.
COPY go.mod go.sum ./
COPY third_party/ ./third_party/
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/romrelay ./cmd/romrelay

# ---- runtime stage ----
# distroless/static: no shell, no package manager, small attack surface. Runs as
# root so it can write to mounted data volumes without host-side chown gymnastics
# (self-hosted, single tenant). The container is Linux and runs on a Linux host
# natively and on Windows through Docker Desktop's WSL2 backend.
FROM gcr.io/distroless/static-debian12:latest
WORKDIR /app
COPY --from=build /out/romrelay /app/romrelay
# Baked-in container defaults (data under /app/data, Postgres via DATABASE_URL).
# Override by mounting your own /app/config.ini and passing secrets via .env.
COPY config.ini /app/config.ini
COPY config.example.ini /app/config.example.ini
COPY .env.example /app/.env.example
EXPOSE 8080
VOLUME ["/app/data"]
# No shell in the image, so the health check runs the binary's own probe mode.
HEALTHCHECK --interval=30s --timeout=5s --start-period=40s --retries=3 \
    CMD ["/app/romrelay", "-healthcheck"]
ENTRYPOINT ["/app/romrelay", "-config-dir", "/app"]
