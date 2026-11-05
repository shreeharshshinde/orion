# Phase 13 — HTTP Request Body Size Limit

> **Status:** Planned  
> **Priority:** High — low effort, prevents memory exhaustion  
> **Estimated effort:** 30–45 minutes  
> **Depends on:** Nothing (implement alongside Authentication, both touch `cmd/api/main.go`)

---

## Table of Contents

1. [Problem statement](#1-problem-statement)
2. [Why this matters for Orion specifically](#2-why-this-matters-for-orion-specifically)
3. [How `http.MaxBytesReader` works](#3-how-httpmaxbytesreader-works)
4. [Files to create or modify](#4-files-to-create-or-modify)
5. [Implementation plan](#5-implementation-plan)
6. [Configuration](#6-configuration)
7. [Error response](#7-error-response)
8. [Tests](#8-tests)
9. [How to verify](#9-how-to-verify)
10. [What this does not protect against](#10-what-this-does-not-protect-against)

---

## 1. Problem statement

The Orion API server currently accepts request bodies of unlimited size. A
single `POST /jobs` or `POST /pipelines` request carrying a multi-gigabyte
body will be buffered entirely into memory by `json.NewDecoder(r.Body).Decode`
before any validation runs.

This exposes two concrete risks:

1. **Accidental**: A training script that accidentally encodes a full dataset
   into the job payload instead of a path to it. Depending on the dataset
   size, this can exhaust the API server's memory and kill the process — taking
   down all other in-flight requests and the gRPC streaming connections with it.

2. **Malicious**: An attacker (or a misconfigured client on an internal network)
   that sends a sustained stream of large bodies to deliberately OOM the
   process. The attack requires no credentials once auth is disabled in dev,
   and is trivially possible even with auth if a valid token is obtained.

The fix is two lines and has zero impact on legitimate request traffic since
job payloads should never exceed a few kilobytes.

---

## 2. Why this matters for Orion specifically

Orion uses `JSONB` payload columns in PostgreSQL (`payload` and `dag_spec`).
These are schema-free by design (ADR-007), which means there is no structural
check that rejects large payloads before they reach the decoder. The handler
calls `json.NewDecoder(r.Body).Decode(&req)` and then passes the decoded
struct directly to the store — the body is fully consumed before any
validation.

Additionally, `POST /pipelines` accepts an entire DAG spec inline. A pipeline
with thousands of nodes would produce a large but structurally valid JSON body.
The size limit catches this before the decoder allocates memory for it.

---

## 3. How `http.MaxBytesReader` works

`http.MaxBytesReader(w, r.Body, limit)` wraps `r.Body` with a reader that
returns an error after `limit` bytes have been read. It also sets a flag on
the response writer that causes `http.Server` to close the underlying TCP
connection after the response is written (to drain and discard the oversized
body rather than leaving it buffered).

The error message from the underlying reader is `"http: request body too
large"`. When `json.NewDecoder` hits this, it returns a
`*json.SyntaxError`-style error that the handler surfaces as a 400 Bad
Request.

No handler code needs to change — the error propagates naturally through the
existing `json.Decode` → `400 Bad Request` path already in all handlers.

---

## 4. Files to create or modify

| File | Action | What changes |
|---|---|---|
| `internal/api/handler/middleware.go` | **Modify** | Add `MaxBodySizeMiddleware(limit int64) func(http.Handler) http.Handler` |
| `internal/config/config.go` | **Modify** | Add `MaxRequestBodyBytes int64` to the `Service` config struct |
| `cmd/api/main.go` | **Modify** | Add `MaxBodySizeMiddleware` to the HTTP middleware chain |

No new files. No changes to any handler. No changes to the gRPC server (gRPC
has its own `MaxRecvMsgSize` option, documented below).

---

## 5. Implementation plan

### 5.1 `internal/api/handler/middleware.go` — add `MaxBodySizeMiddleware`

Add after the existing `MetricsMiddleware` and `TracingMiddleware` functions:

```go
// MaxBodySizeMiddleware rejects HTTP request bodies that exceed limit bytes.
//
// It replaces r.Body with an http.MaxBytesReader, which causes json.Decode
// and other body readers to return an error after limit bytes are consumed.
// The http.Server then closes the connection to prevent the oversized body
// from remaining buffered.
//
// A limit of 0 disables the restriction (all body sizes accepted). This
// allows local development without configuration.
//
// Recommended production value: 10 * 1024 * 1024 (10 MiB). Job payloads
// should be at most a few kilobytes; the 10 MiB headroom covers large DAG
// specs and future fields without being permissive.
func MaxBodySizeMiddleware(limit int64) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        if limit <= 0 {
            return next // restriction disabled
        }
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            r.Body = http.MaxBytesReader(w, r.Body, limit)
            next.ServeHTTP(w, r)
        })
    }
}
```

### 5.2 `internal/config/config.go` — add the field

In the `Service` struct:

```go
type Service struct {
    Environment         string
    LogLevel            string
    HTTPPort            int
    GRPCPort            int
    APIKey              string
    MaxRequestBodyBytes int64 // 0 = unlimited (development default)
}
```

In `Load()`:

```go
MaxRequestBodyBytes: int64(getEnvInt("ORION_MAX_REQUEST_BODY_BYTES", 10*1024*1024)),
```

Where `getEnvInt` is a helper that parses the env var and returns the default
if absent or unparseable:

```go
func getEnvInt(key string, defaultVal int) int {
    if v := os.Getenv(key); v != "" {
        if n, err := strconv.Atoi(v); err == nil {
            return n
        }
    }
    return defaultVal
}
```

### 5.3 `cmd/api/main.go` — wire into the middleware chain

The full middleware stack in order (outermost to innermost):

```go
instrumentedHandler := handler.TracingMiddleware(
    handler.AuthMiddleware(cfg.Service.APIKey,
        handler.MaxBodySizeMiddleware(cfg.Service.MaxRequestBodyBytes)(
            handler.MetricsMiddleware(metrics, mux),
        ),
    ),
)
```

Order matters:
- `TracingMiddleware` is outermost so the trace span covers total request time
  including auth.
- `AuthMiddleware` is before `MaxBodySizeMiddleware` so oversized requests from
  unauthenticated callers get 401 before the body is touched at all.
- `MaxBodySizeMiddleware` wraps the body before `MetricsMiddleware` and the
  handlers so the limit is enforced regardless of which handler reads the body.

### 5.4 gRPC: `MaxRecvMsgSize`

gRPC has a separate limit for incoming message size. Add it to the gRPC server:

```go
const maxGRPCMsgSize = 10 * 1024 * 1024 // 10 MiB

grpcSrv := grpc.NewServer(
    grpc.StatsHandler(otelgrpc.NewServerHandler()),
    grpc.MaxRecvMsgSize(maxGRPCMsgSize),
    grpc.ChainUnaryInterceptor(
        handler.AuthUnaryInterceptor(cfg.Service.APIKey),
    ),
    grpc.ChainStreamInterceptor(
        handler.AuthStreamInterceptor(cfg.Service.APIKey),
    ),
)
```

This is a constant, not config-driven, because gRPC payload sizes are
determined by the proto schema and are not expected to vary by environment.

---

## 6. Configuration

### Environment variable

```bash
# Default: 10 MiB (10485760 bytes)
# Set to 0 to disable the limit entirely (not recommended for production)
ORION_MAX_REQUEST_BODY_BYTES=10485760
```

### `.env.example` addition

```bash
# Maximum HTTP request body size in bytes.
# Requests larger than this are rejected with 400 Bad Request.
# Default: 10485760 (10 MiB). Set to 0 to disable.
ORION_MAX_REQUEST_BODY_BYTES=10485760
```

### Choosing the right limit

| Scenario | Recommended limit |
|---|---|
| Standard job submission (`POST /jobs`) | 64 KiB would be sufficient |
| Pipeline with large DAG spec (`POST /pipelines`) | 1–2 MiB is generous |
| Conservative production default | 10 MiB — safe headroom for all cases |
| Completely disable (local dev only) | 0 |

The 10 MiB default is intentionally generous. It stops runaway clients while
being invisible to all legitimate traffic.

---

## 7. Error response

When a body exceeds the limit, `http.MaxBytesReader` causes the `json.Decode`
call inside the handler to fail. The existing handler error path returns:

```http
HTTP/1.1 400 Bad Request
Content-Type: application/json

{"error": "invalid request body: http: request body too large"}
```

No additional handler code is needed to produce this response — the existing
`json.Decode` error handling already formats and returns 400 for any decode
failure.

---

## 8. Tests

Add test cases to the existing handler tests (`internal/api/handler/job_test.go`
and `internal/api/handler/pipeline_test.go`) rather than creating a new file:

```go
// In TestSubmitJob or a new TestSubmitJob_Oversized:
//
// 1. Create a test server with MaxBodySizeMiddleware(1024) (1 KiB limit)
// 2. POST a body of exactly 1025 bytes of valid JSON
// 3. Assert HTTP 400 and error message contains "too large"
//
// 4. POST a body of exactly 1024 bytes
// 5. Assert the request processes normally (not rejected by size limit)
//
// 6. Set limit to 0, POST a 10 MiB body
// 7. Assert it is NOT rejected by size (limit disabled)
```

The middleware itself is simple enough that a dedicated `middleware_test.go`
is not necessary — the behavior is fully observable through the handler tests.

---

## 9. How to verify

```bash
# Start with default 10 MiB limit
make run-api

# Generate a body larger than 10 MiB (using Python for convenience)
python3 -c "
import json, sys
payload = {'name': 'oversize-test', 'type': 'inline', 'payload': {'data': 'x' * (11 * 1024 * 1024)}}
sys.stdout.write(json.dumps(payload))
" > /tmp/big_payload.json

curl -s -w "\nHTTP status: %{http_code}\n" \
  -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ORION_API_KEY" \
  -d @/tmp/big_payload.json
# Expected: HTTP status: 400

# Normal payload still works
curl -s -w "\nHTTP status: %{http_code}\n" \
  -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ORION_API_KEY" \
  -d '{"name":"size-ok","type":"inline","payload":{"handler_name":"noop"}}'
# Expected: HTTP status: 201

# Unit tests
go test ./internal/api/handler/... -v
```

---

## 10. What this does not protect against

**Slow-read / Slowloris attacks**

`http.MaxBytesReader` limits total body size but does not enforce a minimum
read rate. An attacker can send 1 byte every 30 seconds and keep a connection
open indefinitely. The correct mitigation is `http.Server.ReadTimeout` and
`http.Server.ReadHeaderTimeout`, which are already set in `cmd/api/main.go`:

```go
srv := &http.Server{
    ReadTimeout:  10 * time.Second,
    WriteTimeout: 30 * time.Second,
    IdleTimeout:  60 * time.Second,
}
```

These two controls together (body size limit + read timeout) address the full
surface of HTTP abuse.

**Query parameter abuse**

URL query strings (`GET /jobs?status=...`) are not body-limited. They are
naturally bounded by what the HTTP server accepts as a URL, which Go's
`net/http` limits to 8192 bytes for the request line. No additional work is
needed.

**gRPC streaming**

`grpc.MaxRecvMsgSize` limits a single message. For server-streaming RPCs
(`WatchJob`, `WatchPipeline`), the client only sends one small request
message, so the limit already covers it. There is no client-streaming in
Orion's current proto surface.
