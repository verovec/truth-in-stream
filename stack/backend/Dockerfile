# syntax=docker/dockerfile:1

# yt-dlp is pinned by version and verified against the checksum published in the
# release's SHA2-256SUMS, so a tampered or moved asset fails the build. Bump both
# together (yt-dlp ships frequently). ffmpeg is required for yt-dlp to remux the
# downloaded streams into a single mp4.
ARG YTDLP_VERSION=2026.06.09
ARG YTDLP_SHA256=bf8aac79b72287a6d2043074415132558b43743a8f9461a22b0141e90f16ce66

# Shared base. Pure-Go build (no cgo) now that the store is Postgres + pgvector.
FROM golang:1.26 AS base
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0

# Dev: run with `go run` (compose bind-mounts source over /app). yt-dlp and
# ffmpeg are installed so a YouTube ingest works against the local stack.
FROM base AS dev
ARG YTDLP_VERSION
ARG YTDLP_SHA256
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates ffmpeg; \
    curl -fsSL -o /usr/local/bin/yt-dlp "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/yt-dlp_linux"; \
    echo "${YTDLP_SHA256}  /usr/local/bin/yt-dlp" | sha256sum -c -; \
    chmod 0755 /usr/local/bin/yt-dlp; \
    rm -rf /var/lib/apt/lists/*
EXPOSE 8080
CMD ["go", "run", "./cmd/server"]

# Builder: static binary.
FROM base AS builder
RUN go build -o /out/server ./cmd/server

# Prod: a slim Debian base (not distroless/static) because the pinned yt-dlp
# binary and ffmpeg both need a glibc userland. Runs as a non-root system user.
FROM debian:bookworm-slim AS prod
ARG YTDLP_VERSION
ARG YTDLP_SHA256
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates ffmpeg curl; \
    curl -fsSL -o /usr/local/bin/yt-dlp "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/yt-dlp_linux"; \
    echo "${YTDLP_SHA256}  /usr/local/bin/yt-dlp" | sha256sum -c -; \
    chmod 0755 /usr/local/bin/yt-dlp; \
    apt-get purge -y curl; \
    apt-get autoremove -y --purge; \
    rm -rf /var/lib/apt/lists/*; \
    useradd --system --uid 65532 --user-group --no-create-home nonroot
COPY --from=builder /out/server /server
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/server"]
