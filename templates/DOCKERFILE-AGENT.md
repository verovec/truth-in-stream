# DOCKERFILE-AGENT: {{ORG_NAME}}

```
CREATED: {{DATE}}
LAST_UPDATED: {{DATE}}
VERSION: 2.0.0
AGENT_TYPE: platform
CATEGORY: platform
SCOPE: Dockerfile patterns, container image builds, and security hardening for {{ORG_NAME}}
PARENT: {{PARENT_PATH}}
```

## Linear Card Policy

Before creating or updating any Linear card, you MUST read the roadmap agent first. The roadmap owns all card rules (structure, formatting, tone, defaults, MCP usage, confidentiality). Defer to: `{{ROADMAP_PATH}}` > "Linear Card Rules".

## CRITICAL WARNING

```
THIS DOCUMENT MUST BE UPDATED WHEN:
- A new Dockerfile is added or an existing one is restructured
- Base image version or pinned digest changes (Go, Node, distroless, etc.)
- Dependency installation strategy changes (go mod, npm/pnpm, etc.)
- Build stage structure changes (new stages, removed stages)
- User/group or non-root configuration changes
- Entrypoint or CMD changes
- Security hardening rules change (distroless, non-root, scanning, etc.)
- CI/CD build-push-scan steps change
- New container registry or tagging strategy adopted
```

---

## 1. Reference Patterns

This workspace ships two production images. Both are multi-stage, run as a non-root
user, and pin their base images by immutable `@sha256` digest. New Dockerfiles MUST
follow the matching pattern.

### 1.1 Go service (distroless final stage)

```dockerfile
# syntax=docker/dockerfile:1
FROM golang:<ver>@sha256:<digest> AS build
WORKDIR /src
# go.su[m] is an optional-file glob: copied if present, ignored until deps exist.
COPY go.mod go.su[m] ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot@sha256:<digest>
COPY --from=build /out/server /server
EXPOSE <port>
USER nonroot:nonroot
ENTRYPOINT ["/server"]
```

- `CGO_ENABLED=0` produces a static binary, so `distroless/static` (no libc, no shell,
  no package manager) is the correct, smallest final stage.
- `-trimpath -ldflags="-s -w"` strips paths and debug symbols for a smaller,
  reproducible binary.
- Cache mounts make module downloads and the build cache survive across builds.
- No `HEALTHCHECK`: distroless has no shell and the only executable is the binary, so
  liveness/readiness is delegated to the orchestrator (ECS/K8s probes).

### 1.2 Node / Next.js service (standalone output)

```dockerfile
# syntax=docker/dockerfile:1
FROM node:<ver>-alpine@sha256:<digest> AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci

FROM node:<ver>-alpine@sha256:<digest> AS build
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build

FROM node:<ver>-alpine@sha256:<digest> AS runner
WORKDIR /app
ENV NODE_ENV=production
RUN addgroup -g 1001 -S nodejs && adduser -S nextjs -u 1001
COPY --from=build /app/public ./public
COPY --from=build --chown=nextjs:nodejs /app/.next/standalone ./
COPY --from=build --chown=nextjs:nodejs /app/.next/static ./.next/static
USER nextjs
EXPOSE 3000
CMD ["node", "server.js"]
```

- Requires `output: "standalone"` in `next.config.ts`; the runner needs no
  `node_modules`, only the standalone bundle plus `public` and `.next/static`.
- The npm cache mount keeps `npm ci` fast across builds.
- A `HEALTHCHECK` using the runtime's `fetch` against the listen port is optional and
  only useful when the platform honours Docker health checks.

---

## 2. Security Hardening Checklist

Every Dockerfile MUST satisfy all of the following:

| Rule | How |
|------|-----|
| Non-root user | distroless `:nonroot` + `USER nonroot:nonroot`, or a dedicated `adduser` before `USER` |
| Minimal final image | distroless for Go; standalone bundle on alpine for Node. No build tools or package managers in the final stage |
| Digest-pinned base images | `FROM <image>@sha256:<digest>`, never a floating tag in production images |
| No secrets in image | Never COPY `.env`, credentials, or key files; inject at runtime via env vars or a secrets manager. Enforce with `.dockerignore` |
| Reproducible dependencies | `go mod download` against `go.mod`/`go.sum`; `npm ci` against `package-lock.json` |
| BuildKit cache mounts | `--mount=type=cache` for module/build caches instead of fat layers |
| Vulnerability scan in CI | Trivy (or equivalent) gates the push on HIGH/CRITICAL (see Section 5) |
| `.dockerignore` present | Exclude VCS, tests, secrets, `node_modules`, build artifacts from the context |

---

## 3. Dependency Installation Patterns

### 3.1 Go

```dockerfile
COPY go.mod go.su[m] ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
```

- The `go` directive in `go.mod` MUST match (be <=) the toolchain in the build image.
- `go.su[m]` is the optional-file glob idiom so the COPY succeeds before any
  dependency (and therefore any `go.sum`) exists.

### 3.2 Node.js (npm / pnpm)

```dockerfile
COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
```

- `npm ci` installs the full lockfile (including devDependencies needed to build).
- The standalone build prunes what reaches the runner, so the final image carries
  only runtime dependencies.

---

## 4. Local Development

Local dev runs through `docker-compose.yml`, not these Dockerfiles: source is
bind-mounted and dev servers run with hot reload. Keep the compose base images
digest-pinned to the same digests as the production stages (and the devcontainer) so
local, CI, and production agree. Use `healthcheck` + `depends_on: condition:
service_healthy` to order service startup.

---

## 5. CI/CD Build Integration

### 5.1 GitHub Actions build-scan-push pattern

```yaml
- uses: actions/checkout@<sha> # <ver>
- uses: docker/setup-buildx-action@<sha> # <ver>
- uses: docker/login-action@<sha> # <ver>
- id: meta
  uses: docker/metadata-action@<sha> # <ver>
# Build locally first so the image can be scanned before publishing.
- name: Build image (no push)
  uses: docker/build-push-action@<sha> # <ver>
  with:
    context: <context>
    load: true
    tags: <name>:scan
    cache-from: type=gha,scope=<name>
    cache-to: type=gha,scope=<name>,mode=max
- name: Scan image
  uses: aquasecurity/trivy-action@<sha> # <ver>
  with:
    image-ref: <name>:scan
    exit-code: "1"
    ignore-unfixed: true
    severity: HIGH,CRITICAL
- name: Build and push image
  uses: docker/build-push-action@<sha> # <ver>
  with:
    context: <context>
    push: true
    tags: ${{ steps.meta.outputs.tags }}
    labels: ${{ steps.meta.outputs.labels }}
    cache-from: type=gha,scope=<name>
    cache-to: type=gha,scope=<name>,mode=max
    provenance: true
    sbom: true
```

- Pin every action to a commit SHA, with the human-readable version in a trailing
  comment. Never a bare tag.
- The scan step gates the push; the shared GHA cache makes the second build a fast
  re-tag.
- Emit provenance + SBOM attestations for supply-chain traceability.
- Resolve SHAs and image digests from a registry/API at change time, never from
  memory; let Renovate/Dependabot keep them current.

### 5.2 Tagging convention

| Variant | Tags |
|---------|------|
| Any service | `type=sha` (commit SHA) and `latest` on the default branch |

---

## 6. Dockerfile Inventory

{{DOCKERFILE_INVENTORY}}

For each Dockerfile, document:
- Path relative to repo root
- Runtime (Go service, Next.js service, etc.)
- Base image, version, and pinned digest
- Build context path
- CI workflow that builds it
- Deployed to (ECS service, App Runner, K8s deployment, etc.)

---

## Cross-References

```yaml
parent: {{PARENT_PATH}}
platform_agent: {{PLATFORM_AGENT_PATH}}
application_agent: {{APPLICATION_AGENT_PATH}}
roadmap: {{ROADMAP_PATH}}
```

## Workflow

When working on Dockerfile changes:

1. Read this agent for the canonical pattern and security checklist
2. Read the **platform agent** for deployment topology and CI/CD pipeline context
3. Read the **application agent** if the change affects dependency installation or entrypoints
4. Apply the matching pattern from Section 1, verify against the checklist in Section 2
5. Resolve any new base-image digest or action SHA from a registry/API, not from memory
6. Update the Dockerfile inventory in Section 6 if a new Dockerfile was added

## Document Maintenance

```
CREATED: {{DATE}}
LAST_UPDATED: {{DATE}}
DOCUMENT_OWNER: {{ORG_NAME}} Team
AUTHORS: [TO BE FILLED]

UPDATE_TRIGGERS:
- New Dockerfile added or existing one restructured
- Base image version or pinned digest changes
- Dependency installation strategy changes
- Build stage structure changes
- Security hardening rules change
- CI/CD build-scan-push steps change
- Container registry or tagging strategy changes
```

END_OF_DOCUMENT
