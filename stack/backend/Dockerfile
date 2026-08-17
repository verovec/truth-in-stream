# syntax=docker/dockerfile:1

# yt-dlp is pinned by version and verified against the checksums published in the
# release's SHA2-256SUMS, so a tampered or moved asset fails the build. The asset
# is selected per TARGETARCH (yt-dlp_linux on amd64, yt-dlp_linux_aarch64 on
# arm64, e.g. Apple Silicon local dev) with one pinned checksum each. Bump the
# version and both checksums together (yt-dlp ships frequently). ffmpeg is
# required for yt-dlp to remux the downloaded streams into a single mp4.
ARG YTDLP_VERSION=2026.06.09
ARG YTDLP_SHA256_AMD64=bf8aac79b72287a6d2043074415132558b43743a8f9461a22b0141e90f16ce66
ARG YTDLP_SHA256_ARM64=cabd246445bdfde0eda0dfe68bbe90354be83f3fdbbf077df11a2ea55f41cdbd

# streamlink resolves official YouTube live simulcasts for the tvcapture worker
# (it pipes the stream into ffmpeg). Debian's apt package lags well behind
# upstream, so it is pinned by version and installed from PyPI. HLS parliamentary
# sources need only ffmpeg (already installed), not streamlink. Bump deliberately
# and re-verify YouTube resolution still works (streamlink#6922 tracks YouTube's
# n-challenge churn). streamlink has no PO-token mechanism, so no token sidecar is
# needed for the pure-streamlink pipeline.
ARG STREAMLINK_VERSION=8.4.0

# Fetches and verifies the yt-dlp binary once; dev and prod copy it from here so
# the asset selection and checksum verification can never diverge between the
# two stages. TARGETARCH is only populated by BuildKit, so a legacy build falls
# back to the host architecture reported by dpkg.
FROM debian:bookworm-slim AS ytdlp
ARG TARGETARCH
ARG YTDLP_VERSION
ARG YTDLP_SHA256_AMD64
ARG YTDLP_SHA256_ARM64
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl; \
    arch="${TARGETARCH:-$(dpkg --print-architecture)}"; \
    case "${arch}" in \
        amd64) ytdlp_asset=yt-dlp_linux; ytdlp_sha256="${YTDLP_SHA256_AMD64}" ;; \
        arm64) ytdlp_asset=yt-dlp_linux_aarch64; ytdlp_sha256="${YTDLP_SHA256_ARM64}" ;; \
        *) echo "unsupported architecture: ${arch}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /usr/local/bin/yt-dlp "https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_VERSION}/${ytdlp_asset}"; \
    echo "${ytdlp_sha256}  /usr/local/bin/yt-dlp" | sha256sum -c -; \
    chmod 0755 /usr/local/bin/yt-dlp

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
ARG STREAMLINK_VERSION
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates ffmpeg python3 python3-pip; \
    pip install --no-cache-dir --break-system-packages "streamlink==${STREAMLINK_VERSION}"; \
    rm -rf /var/lib/apt/lists/*
COPY --from=ytdlp /usr/local/bin/yt-dlp /usr/local/bin/yt-dlp
EXPOSE 8080
CMD ["go", "run", "./cmd/server"]

# Builder: static binaries. The one image carries the HTTP server and the
# ingestion entry points; each non-server workload runs as its own ECS task or
# service selecting its binary as the entrypoint: /embedworker and /crawlworker
# are the dump and crawl worker fleets (see stack/terraform/modules/worker), and
# /wikicrawl is the on-demand category-crawl producer (modules/scheduled-task).
FROM base AS builder
RUN go build -o /out/server ./cmd/server
RUN go build -o /out/embedworker ./cmd/embedworker
RUN go build -o /out/crawlworker ./cmd/crawlworker
RUN go build -o /out/wikicrawl ./cmd/wikicrawl
RUN go build -o /out/tvcapture ./cmd/tvcapture

# Prod: a slim Debian base (not distroless/static) because the pinned yt-dlp
# binary and ffmpeg both need a glibc userland. Runs as a non-root system user.
FROM debian:bookworm-slim AS prod
ARG STREAMLINK_VERSION
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates ffmpeg python3 python3-pip; \
    pip install --no-cache-dir --break-system-packages "streamlink==${STREAMLINK_VERSION}"; \
    rm -rf /var/lib/apt/lists/*; \
    useradd --system --uid 65532 --user-group --no-create-home nonroot
COPY --from=ytdlp /usr/local/bin/yt-dlp /usr/local/bin/yt-dlp
# python3-pip is intentionally NOT purged: streamlink was pip-installed and
# imports its dependencies (e.g. setuptools/pkg_resources) at runtime; autoremoving
# pip would strip those and break `streamlink` at exec time, silently disabling
# YouTube capture in prod.
COPY --from=builder /out/server /server
COPY --from=builder /out/embedworker /embedworker
COPY --from=builder /out/crawlworker /crawlworker
COPY --from=builder /out/wikicrawl /wikicrawl
COPY --from=builder /out/tvcapture /tvcapture
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/server"]
