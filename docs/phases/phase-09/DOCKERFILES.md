# Dockerfiles — Phase 9

## What This Document Covers

The three multi-stage Dockerfiles added in Phase 9: why multi-stage, what each stage does, what ends up in the final image, and how to build and verify them.

---

## Design: Multi-Stage Builds

All three Dockerfiles follow the same two-stage pattern:

```
Stage 1: builder (golang:1.22-alpine)
  - Has the full Go toolchain (~400MB)
  - Downloads dependencies
  - Compiles a static binary

Stage 2: final (scratch)
  - Empty filesystem
  - Copies only: binary + CA certs + timezone data
  - Result: ~12–15MB image
```

The builder stage is discarded after compilation. Nothing from it — no Go toolchain, no Alpine packages, no source code — ends up in the final image.

---

## Why `scratch`

`scratch` is Docker's empty base image. It contains literally nothing: no shell, no libc, no package manager, no OS utilities.

This is possible because the Go binary is compiled with `CGO_ENABLED=0`, which produces a fully static binary that links nothing at runtime. The binary is self-contained.

Benefits:
- **Size**: ~12MB vs ~400MB (builder) or ~20MB (alpine)
- **Security**: no shell means `kubectl exec bash` returns an error — attackers who compromise the container have no tools to work with
- **Attack surface**: no OS packages means no OS CVEs

The two things that must be copied from the builder:
1. **CA certificates** (`/etc/ssl/certs/ca-certificates.crt`) — needed for TLS connections to PostgreSQL, Redis, and the OTel collector. Without them, TLS handshakes fail with "certificate signed by unknown authority".
2. **Timezone data** (`/usr/share/zoneinfo`) — needed for `time.LoadLocation` and correct `slog` timestamps. Without it, all times are UTC regardless of `TZ` env var.

---

## `Dockerfile.api`

```dockerfile
# Stage 1: Build
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache ca-certificates git tzdata

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download          # ← cached layer; only re-runs when go.mod changes

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s -X main.Version=${VERSION}" \
    -o /orion-api ./cmd/api/

# Stage 2: Final
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /orion-api /orion-api

USER 10001:10001

EXPOSE 8080 9091

ENTRYPOINT ["/orion-api"]
```

**Ports exposed:**
- `8080` — HTTP API (`/jobs`, `/pipelines`, `/queues`, `/healthz`, `/readyz`)
- `9091` — Prometheus metrics (`/metrics`)
- `9090` — gRPC (not in EXPOSE but the binary listens on it; EXPOSE is documentation only)

---

## `Dockerfile.scheduler`

Identical structure to the API Dockerfile. Differences:

- Builds `./cmd/scheduler/` → `/orion-scheduler`
- Exposes only `9092` (metrics port)
- No HTTP port — the scheduler has no API surface

```dockerfile
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s -X main.Version=${VERSION}" \
    -o /orion-scheduler ./cmd/scheduler/
```

---

## `Dockerfile.worker`

Identical structure. Differences:

- Builds `./cmd/worker/` → `/orion-worker`
- Exposes only `9093` (metrics port)
- Worker uses in-cluster Kubernetes config (`ORION_K8S_IN_CLUSTER=true`) — no kubeconfig file needed

```dockerfile
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s -X main.Version=${VERSION}" \
    -o /orion-worker ./cmd/worker/
```

---

## Build Flags Explained

```
CGO_ENABLED=0    — disable cgo; produce a fully static binary (no libc dependency)
GOOS=linux       — target Linux (required when building on macOS)
GOARCH=amd64     — target x86-64 (change to arm64 for Apple Silicon nodes)
-w               — strip DWARF debug info (~15% size reduction)
-s               — strip symbol table (~25% size reduction)
-X main.Version  — embed the git version string into the binary
```

The `-w -s` flags together reduce binary size by ~40% with no runtime impact. The binary cannot be debugged with `dlv` after stripping, but production binaries should not be debugged interactively — use traces and logs instead.

---

## Layer Caching Strategy

```dockerfile
COPY go.mod go.sum ./
RUN go mod download    # ← this layer is cached

COPY . .               # ← source changes invalidate from here
RUN go build ...
```

`go mod download` runs in its own layer before source is copied. When only source files change (the common case), Docker reuses the cached dependency layer. A full `go mod download` only re-runs when `go.mod` or `go.sum` changes.

Without this ordering, every source change would re-download all dependencies (~30–60 seconds). With it, rebuilds take ~10–15 seconds.

---

## Non-Root User

```dockerfile
USER 10001:10001
```

UID 10001 is used because `scratch` has no `/etc/passwd` — there is no named user. Kubernetes `runAsNonRoot: true` validates that the numeric UID is not 0. Using a numeric UID directly satisfies this check without needing a passwd file.

The Helm chart's pod security context mirrors this:
```yaml
securityContext:
  runAsNonRoot: true
  runAsUser: 10001
  runAsGroup: 10001
```

---

## Build Commands

```bash
# Set version from git
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")

# Build all three images
make docker-build
# Equivalent to:
docker build -f deploy/docker/Dockerfile.api \
  --build-arg VERSION=$VERSION \
  -t orion-api:$VERSION .

docker build -f deploy/docker/Dockerfile.scheduler \
  --build-arg VERSION=$VERSION \
  -t orion-scheduler:$VERSION .

docker build -f deploy/docker/Dockerfile.worker \
  --build-arg VERSION=$VERSION \
  -t orion-worker:$VERSION .
```

**Important:** All three builds run from the **repository root** (`.`), not from `deploy/docker/`. The `COPY . .` instruction needs access to the entire Go module. Running `docker build` from inside `deploy/docker/` would fail because `go.mod` is not in that directory.

---

## Verifying Image Size

```bash
docker images | grep orion
# REPOSITORY         TAG     IMAGE ID       SIZE
# orion-api          dev     abc123...      13.2MB
# orion-scheduler    dev     def456...      11.8MB
# orion-worker       dev     ghi789...      14.1MB
```

If sizes are significantly larger (>50MB), check:
1. `CGO_ENABLED=0` is set — cgo pulls in libc
2. Final stage is `FROM scratch` — not accidentally left as `FROM alpine`
3. `-ldflags="-w -s"` is present — debug symbols inflate size

---

## Inspecting the Final Image

Since `scratch` has no shell, standard inspection tools don't work inside the container. Use `docker inspect` and layer analysis instead:

```bash
# List layers and their sizes
docker history orion-api:dev
# IMAGE          CREATED BY                                      SIZE
# <missing>      ENTRYPOINT ["/orion-api"]                       0B
# <missing>      USER 10001:10001                                0B
# <missing>      EXPOSE 8080 9091                                0B
# <missing>      COPY /orion-api /orion-api                      12.1MB
# <missing>      COPY /usr/share/zoneinfo /usr/share/zoneinfo    1.1MB
# <missing>      COPY /etc/ssl/certs/ca-certificates.crt ...     214kB

# Verify the binary is statically linked
docker run --rm --entrypoint="" orion-api:dev /orion-api --version
# orion-api version dev
```

---

## Push to Registry

```bash
REGISTRY=ghcr.io/shreeharshshinde/orion

make docker-push
# Equivalent to:
docker tag orion-api:$VERSION $REGISTRY/orion-api:$VERSION
docker push $REGISTRY/orion-api:$VERSION
# (same for scheduler and worker)
```

For GitHub Container Registry, authenticate first:
```bash
echo $GITHUB_TOKEN | docker login ghcr.io -u shreeharshshinde --password-stdin
```

---

## Note on `preStop` and `scratch`

The worker Deployment's `preStop` hook is defined as:
```yaml
lifecycle:
  preStop:
    exec:
      command: ["/bin/sh", "-c", "sleep 5"]
```

`/bin/sh` does not exist in a `scratch` image. Kubernetes will log a warning but the pod will still terminate gracefully — the `terminationGracePeriodSeconds: 60` ensures the kubelet waits 60 seconds before sending SIGKILL regardless.

If you need a functional `preStop` hook (e.g., to drain a connection pool), switch the worker's final stage to `gcr.io/distroless/static:nonroot` instead of `scratch`. This adds ~2MB but includes a minimal shell-less runtime that supports exec probes.
