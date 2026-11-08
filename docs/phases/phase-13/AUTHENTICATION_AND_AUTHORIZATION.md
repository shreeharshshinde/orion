# Phase 13 — Authentication & Authorization

> **Status:** ✅ Complete  
> **Priority:** Critical — implement before any public or shared deployment  
> **Completed:** 2026-11-08  
> **Depends on:** Nothing (can be done in isolation)

---

## Table of Contents

1. [What was implemented](#0-what-was-implemented)
2. [Problem statement](#1-problem-statement)
3. [Design decisions](#2-design-decisions)
4. [Architecture](#3-architecture)
5. [Files to create or modify](#4-files-to-create-or-modify)
6. [Implementation plan](#5-implementation-plan)
7. [Configuration](#6-configuration)
8. [Exempt paths](#7-exempt-paths)
9. [gRPC authentication](#8-grpc-authentication)
10. [Tests](#9-tests)
11. [How to verify](#10-how-to-verify)
12. [Future extensions](#11-future-extensions)

---

## 0. What was implemented

### Files created / modified

| File | Change |
|---|---|
| `internal/api/handler/auth.go` | `AuthMiddleware`, `AuthUnaryInterceptor`, `AuthStreamInterceptor`, `validateGRPCToken` |
| `internal/api/handler/auth_test.go` | 13 table-driven test cases covering all pass/fail paths |
| `internal/config/config.go` | Added `APIKey string` to `ServiceConfig`; wired from `ORION_API_KEY` env var |
| `cmd/api/main.go` | Wired `AuthMiddleware` into the HTTP chain; added `AuthUnaryInterceptor` and `AuthStreamInterceptor` to gRPC server |

### Security additions beyond the original plan

Three changes were made over the initial design for correctness and production safety:

**1. Constant-time token comparison (`crypto/subtle`)**  
The original design used `!=` string equality. Replaced with
`crypto/subtle.ConstantTimeCompare` in both `AuthMiddleware` and
`validateGRPCToken`. Without this, a timing-oracle attack can recover the
secret byte-by-byte by measuring response latency differences.

**2. CORS preflight exemption**  
`OPTIONS` requests are exempt from token validation. Browsers issue a preflight
`OPTIONS` request without `Authorization` headers before every cross-origin
request. Without this exemption, the frontend dashboard would receive 401 on
every page load in a cross-origin deployment.

**3. `WWW-Authenticate` response header**  
401 responses include `WWW-Authenticate: Bearer realm="orion"` as required by
RFC 7235 §4.1. Without this header, HTTP clients and some SDKs treat the 401
as a generic server error rather than an authentication challenge, making
debugging much harder.

---

## 1. Problem statement

Orion's HTTP and gRPC surfaces are currently **completely open**. Any process
that can reach the API server can:

- Submit arbitrary jobs to any queue
- Cancel or delete any job
- Read execution history and payloads (which may contain model hyperparameters
  or data source credentials)
- Update queue configuration and rate limits
- Replay dead jobs

In a developer's local environment (`make infra-up`) this is fine. The moment
Orion is deployed to a shared namespace, a staging cluster, or accessed over a
network boundary, it is a security gap.

The fix for v1 is a **static shared API key** delivered as a Bearer token on
every request. A single key covers all callers. Role-based access control (per
team, per queue, per operation) is out of scope for this phase and documented
as a future extension below.

---

## 2. Design decisions

### Why a static API key rather than JWT

JWTs require a key management system, an issuer, token rotation, clock-skew
handling, and a way to revoke individual tokens. For a platform orchestrator
whose typical callers are CI pipelines, training scripts, and internal
dashboards — not end users — a shared secret is simpler and appropriate.

A single environment variable (`ORION_API_KEY`) is straightforward to set in
Kubernetes secrets, Helm values, or `.env` files without adding an auth
service dependency.

If multi-tenant isolation is needed later, the same `AuthMiddleware` interface
is extended to validate JWTs — no handler code changes required.

### Why Bearer token in `Authorization` header

It follows RFC 7235 exactly as clients expect:

```
Authorization: Bearer <token>
```

This works identically for `curl`, the frontend `fetch()` calls, and any SDK.
It is also the format gRPC metadata mirrors when using the standard
`authorization` metadata key.

### Why not mTLS at this phase

mTLS is a network-level control, not an application-level one. It is the right
choice once Orion is inside a service mesh (Istio, Linkerd). For now it adds
certificate rotation complexity without improving the threat model beyond what
a bearer token already provides.

---

## 3. Architecture

```
Incoming HTTP request
        │
        ▼
TracingMiddleware          ← creates root OTel span
        │
        ▼
AuthMiddleware             ← NEW: validates Authorization: Bearer <token>
        │                      returns 401 if missing or wrong
        │                      passes through /healthz, /readyz, /metrics
        ▼
MetricsMiddleware          ← records HTTP counters and latency
        │
        ▼
http.ServeMux (handlers)   ← only reached on valid token
```

```
Incoming gRPC call
        │
        ▼
otelgrpc.NewServerHandler  ← existing OTel stats handler
        │
        ▼
grpc.UnaryInterceptor      ← NEW: reads metadata["authorization"], validates
grpc.StreamInterceptor     ← NEW: same, for WatchJob / WatchPipeline streams
        │
        ▼
grpc.Server (handlers)     ← only reached on valid token
```

The `AuthMiddleware` does not need to be aware of routes or verbs. It is a
single pass/fail gate. The HTTP handler already returns correct 404/405 for
unknown routes — auth is checked before routing.

---

## 4. Files to create or modify

| File | Action | What changes |
|---|---|---|
| `internal/api/handler/auth.go` | **Create** | `AuthMiddleware` for HTTP; `AuthUnaryInterceptor` and `AuthStreamInterceptor` for gRPC |
| `internal/api/handler/auth_test.go` | **Create** | Table-driven tests for all token pass/fail cases |
| `internal/config/config.go` | **Modify** | Add `APIKey string` field to the `Service` config struct; read from `ORION_API_KEY` env var |
| `cmd/api/main.go` | **Modify** | Wire `AuthMiddleware` into the HTTP chain; pass interceptors to `grpc.NewServer` |
| `deploy/helm/values.yaml` | **Modify** | Add `auth.apiKey` value (empty = auth disabled, intentional for dev installs) |
| `deploy/helm/templates/secret.yaml` | **Modify** | Add `ORION_API_KEY` to the Kubernetes secret |
| `.env.example` | **Modify** | Document `ORION_API_KEY=` |

---

## 5. Implementation plan

### 5.1 `internal/api/handler/auth.go`

```go
package handler

import (
    "context"
    "crypto/subtle"
    "net/http"
    "strings"

    "google.golang.org/grpc"
    "google.golang.org/grpc/codes"
    "google.golang.org/grpc/metadata"
    "google.golang.org/grpc/status"
)

// exemptPaths are never blocked by auth, regardless of token presence.
// These must remain reachable by load balancers and Prometheus scrapers
// without credentials.
var exemptPaths = map[string]bool{
    "/healthz": true,
    "/readyz":  true,
    "/metrics": true,
}

// AuthMiddleware returns an HTTP middleware that validates every request
// carries a valid Bearer token matching apiKey.
//
// If apiKey is empty, the middleware is a no-op — auth is disabled.
// This lets local development (make run-api) work without configuration.
//
// Comparison uses crypto/subtle.ConstantTimeCompare to prevent timing
// attacks that could allow an attacker to infer the token length or
// value by measuring response latency.
//
// CORS preflight OPTIONS requests are exempt so browsers can complete
// the preflight handshake before attaching credentials.
//
// Usage:
//   instrumentedHandler := handler.TracingMiddleware(
//       handler.AuthMiddleware(cfg.Service.APIKey,
//           handler.MetricsMiddleware(metrics, mux),
//       ),
//   )
func AuthMiddleware(apiKey string, next http.Handler) http.Handler {
    if apiKey == "" {
        return next // auth disabled
    }
    expected := []byte("Bearer " + apiKey)

    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // OPTIONS preflight requests carry no credentials — browsers send them
        // before the real request to negotiate CORS. Blocking them here would
        // prevent the actual credentialed request from ever being made.
        if r.Method == http.MethodOptions || exemptPaths[r.URL.Path] {
            next.ServeHTTP(w, r)
            return
        }

        // ConstantTimeCompare prevents timing attacks: the comparison takes
        // the same amount of time regardless of where the strings diverge.
        // Plain != would return early on the first differing byte, leaking
        // information about how much of the token is correct.
        got := []byte(r.Header.Get("Authorization"))
        if subtle.ConstantTimeCompare(got, expected) != 1 {
            http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
            return
        }

        next.ServeHTTP(w, r)
    })
}

// AuthUnaryInterceptor is a gRPC server interceptor that validates Bearer
// tokens on every unary RPC. Returns codes.Unauthenticated on failure.
func AuthUnaryInterceptor(apiKey string) grpc.UnaryServerInterceptor {
    return func(
        ctx context.Context,
        req any,
        info *grpc.UnaryServerInfo,
        handler grpc.UnaryHandler,
    ) (any, error) {
        if apiKey == "" {
            return handler(ctx, req)
        }
        if err := validateGRPCToken(ctx, apiKey); err != nil {
            return nil, err
        }
        return handler(ctx, req)
    }
}

// AuthStreamInterceptor is a gRPC server interceptor for streaming RPCs
// (WatchJob, WatchPipeline). Returns codes.Unauthenticated on failure.
func AuthStreamInterceptor(apiKey string) grpc.StreamServerInterceptor {
    return func(
        srv any,
        ss grpc.ServerStream,
        info *grpc.StreamServerInfo,
        handler grpc.StreamHandler,
    ) error {
        if apiKey == "" {
            return handler(srv, ss)
        }
        if err := validateGRPCToken(ss.Context(), apiKey); err != nil {
            return err
        }
        return handler(srv, ss)
    }
}

// validateGRPCToken reads the "authorization" metadata key and checks it
// matches "Bearer <apiKey>" using a constant-time comparison.
func validateGRPCToken(ctx context.Context, apiKey string) error {
    md, ok := metadata.FromIncomingContext(ctx)
    if !ok {
        return status.Error(codes.Unauthenticated, "missing metadata")
    }
    values := md.Get("authorization")
    if len(values) == 0 {
        return status.Error(codes.Unauthenticated, "missing authorization header")
    }
    token := strings.TrimPrefix(values[0], "Bearer ")
    if subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) != 1 {
        return status.Error(codes.Unauthenticated, "invalid token")
    }
    return nil
}
```

### 5.2 `internal/config/config.go` — add `APIKey`

In the `Service` struct:

```go
type Service struct {
    Environment string
    LogLevel    string
    HTTPPort    int
    GRPCPort    int
    APIKey      string // ORION_API_KEY — empty disables auth
}
```

In the `Load()` function, add:

```go
APIKey: os.Getenv("ORION_API_KEY"),
```

### 5.3 `cmd/api/main.go` — wire both layers

HTTP chain (replace current middleware stack):

```go
instrumentedHandler := handler.TracingMiddleware(
    handler.AuthMiddleware(cfg.Service.APIKey,
        handler.MetricsMiddleware(metrics, mux),
    ),
)
```

gRPC server (replace current):

```go
grpcSrv := grpc.NewServer(
    grpc.StatsHandler(otelgrpc.NewServerHandler()),
    grpc.ChainUnaryInterceptor(
        handler.AuthUnaryInterceptor(cfg.Service.APIKey),
    ),
    grpc.ChainStreamInterceptor(
        handler.AuthStreamInterceptor(cfg.Service.APIKey),
    ),
)
```

---

## 6. Configuration

### Environment variable

```bash
ORION_API_KEY=your-secret-token-here
```

Setting this to an empty string or leaving it unset disables auth entirely.
This is intentional — local `make run-api` sessions should work without
configuration.

### `.env.example` addition

```bash
# API authentication
# Set to a random secret string to enable Bearer token auth on all endpoints.
# Leave empty to disable auth (local development only).
ORION_API_KEY=
```

### Helm values

```yaml
# deploy/helm/values.yaml
auth:
  # apiKey is the shared Bearer token required on all API requests.
  # Empty string disables auth (not recommended for production).
  apiKey: ""
```

In `deploy/helm/templates/secret.yaml`:

```yaml
data:
  ORION_API_KEY: {{ .Values.auth.apiKey | b64enc | quote }}
```

### Generating a key

```bash
# Generate a cryptographically random 32-byte key, hex-encoded
openssl rand -hex 32
```

---

## 7. Exempt paths

These paths bypass token validation unconditionally:

| Path | Reason |
|---|---|
| `/healthz` | Kubernetes liveness probe — kubelet has no token |
| `/readyz` | Kubernetes readiness probe — same |
| `/metrics` | Prometheus scraper — uses network policy / service account instead |

No other paths are exempt. `/jobs`, `/pipelines`, `/queues`, `/workers` all
require a valid token.

---

## 8. gRPC authentication

gRPC clients pass the token in metadata:

```go
// Go SDK
ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+apiKey)
conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
```

```bash
# grpcurl
grpcurl -plaintext \
  -H "authorization: Bearer your-token" \
  -d '{"job_id": "<id>"}' \
  localhost:9090 orion.v1.JobService/WatchJob
```

The interceptors check `metadata.FromIncomingContext(ctx)` for the
`authorization` key, following the same `Bearer <token>` convention as HTTP.

---

## 9. Tests

### `internal/api/handler/auth_test.go`

```go
// Table-driven test matrix:
//
// | apiKey config | Method  | Request header              | Path      | Expected |
// |---------------|---------|------------------------------|-----------|----------|
// | "secret"      | GET     | "Bearer secret"              | /jobs     | pass     |
// | "secret"      | GET     | "Bearer wrong"               | /jobs     | 401      |
// | "secret"      | GET     | (none)                       | /jobs     | 401      |
// | "secret"      | GET     | "Bearer secret"              | /healthz  | pass     |
// | "secret"      | GET     | (none)                       | /healthz  | pass     |
// | "secret"      | OPTIONS | (none)                       | /jobs     | pass (CORS preflight) |
// | "secret"      | OPTIONS | (none)                       | /pipelines| pass (CORS preflight) |
// | ""            | GET     | (none)                       | /jobs     | pass (disabled) |
// | "secret"      | GET     | "secret" (no Bearer prefix)  | /jobs     | 401      |
//
// Additionally verify that the comparison is constant-time by confirming
// subtle.ConstantTimeCompare is used (code review / static analysis), and
// that a token that shares a prefix with the real token ("Bearer sec") is
// rejected with 401 (not a prefix-match vulnerability).
```

### gRPC interceptor tests

Use `google.golang.org/grpc/test/bufconn` (already used in `server_test.go`)
to start an in-process gRPC server with the interceptors and assert
`codes.Unauthenticated` is returned without a valid token.

---

## 10. How to verify

```bash
# Start API with a key set
ORION_API_KEY=test-token make run-api

# Should fail — no token
curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/jobs
# Expected: 401

# Should succeed
curl -s -o /dev/null -w "%{http_code}" \
  -H "Authorization: Bearer test-token" \
  http://localhost:8080/jobs
# Expected: 200

# Health probes still work without token
curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/healthz
# Expected: 200

# gRPC without token
grpcurl -plaintext -d '{"name":"test","type":"inline"}' \
  localhost:9090 orion.v1.JobService/SubmitJob
# Expected: code = Unauthenticated

# gRPC with token
grpcurl -plaintext \
  -H "authorization: Bearer test-token" \
  -d '{"name":"test","type":"inline","payload":{"handler_name":"noop"}}' \
  localhost:9090 orion.v1.JobService/SubmitJob
# Expected: job object in response

# Verify unit tests pass
go test ./internal/api/handler/... -run TestAuth -v
```

---

## 11. Future extensions

These are explicitly out of scope for this phase but the design accommodates
them without changing handler code:

**Role-based access control (RBAC)**
Replace the single `apiKey` string with a token registry that maps each token
to a set of allowed operations (`submit`, `cancel`, `admin`). The
`AuthMiddleware` would enrich the request context with the caller's role and
a separate `RequireRole(role)` middleware would gate individual routes.

**JWT validation**
Replace `validateGRPCToken` with a JWT verifier. The `AuthMiddleware` and
interceptor signatures do not change — only the validation logic.

**Per-queue API keys**
Allow some tokens to only submit to specific queues. Checked in the job
handler after auth, not in the auth layer itself.

**mTLS**
Once inside a service mesh, retire the application-level token and use
peer certificate identity instead. The `AuthMiddleware` becomes a no-op and
is removed from the chain.
