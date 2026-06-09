# syntax=docker/dockerfile:1

# Shared base with the C toolchain and native Ladybug lib (flags confirmed by spike).
FROM golang:1.26 AS base
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc libstdc++-14-dev curl ca-certificates && rm -rf /var/lib/apt/lists/*
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go generate ./...
ENV CGO_ENABLED=1
ENV CGO_CFLAGS="-I/app/lib-ladybug"
ENV CGO_LDFLAGS="-L/app/lib-ladybug -llbug -Wl,-rpath,/app/lib-ladybug"

# Dev: run with `go run` (compose bind-mounts source over /app).
FROM base AS dev
EXPOSE 8080
CMD ["go", "run", "-tags", "system_ladybug", "./cmd/server"]

# Builder: shared-lib link. Reuses base so the native lib and flags are identical.
# The binary's baked rpath is /app/lib-ladybug.
FROM base AS builder
RUN go build -tags system_ladybug -o /out/server ./cmd/server

# Prod: distroless/cc (ships libstdc++/libgcc + glibc loader for cgo), non-root.
# Recreate /app/lib-ladybug so the binary's baked rpath resolves liblbug.so.0 at load.
FROM gcr.io/distroless/cc-debian12:nonroot AS prod
COPY --from=builder /app/lib-ladybug /app/lib-ladybug
COPY --from=builder /out/server /server
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
