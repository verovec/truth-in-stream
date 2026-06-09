# syntax=docker/dockerfile:1

# Shared base. Pure-Go build (no cgo) now that the store is Postgres + pgvector.
FROM golang:1.26 AS base
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0

# Dev: run with `go run` (compose bind-mounts source over /app).
FROM base AS dev
EXPOSE 8080
CMD ["go", "run", "./cmd/server"]

# Builder: static binary.
FROM base AS builder
RUN go build -o /out/server ./cmd/server

# Prod: distroless/static (no libc needed for a CGO_ENABLED=0 binary), non-root.
FROM gcr.io/distroless/static-debian12:nonroot AS prod
COPY --from=builder /out/server /server
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
