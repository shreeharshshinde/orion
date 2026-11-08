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

// exemptPaths are never blocked by AuthMiddleware, regardless of token presence.
// These paths must remain reachable by Kubernetes probes and Prometheus scrapers
// without credentials.
var exemptPaths = map[string]bool{
	"/healthz": true,
	"/readyz":  true,
	"/metrics": true,
}

// AuthMiddleware returns an HTTP middleware that validates every incoming request
// carries a valid Bearer token matching apiKey.
//
// Security properties:
//   - Token comparison uses crypto/subtle.ConstantTimeCompare to prevent
//     timing attacks that could reveal the secret length or content.
//   - OPTIONS requests are exempt so CORS preflight passes without credentials.
//   - /healthz, /readyz, and /metrics are exempt so Kubernetes probes and
//     Prometheus scraping work without configuration.
//
// Zero-config local dev:
//
//	If apiKey is empty, the middleware is a no-op — all requests pass through.
//	This keeps `make run-api` working without any environment setup.
//
// Middleware chain position (outermost → innermost):
//
//	TracingMiddleware → AuthMiddleware → MaxBodySizeMiddleware → MetricsMiddleware → mux
//
// The span in TracingMiddleware covers the auth check so failed auth attempts
// are visible in Jaeger with their 401 status codes.
func AuthMiddleware(apiKey string, next http.Handler) http.Handler {
	if apiKey == "" {
		// Auth disabled — pass all requests through unchanged.
		return next
	}

	// Pre-compute the full expected header value once at startup.
	// This avoids string allocation on every request.
	expected := []byte("Bearer " + apiKey)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Exempt Kubernetes liveness/readiness probes and Prometheus scraper.
		if exemptPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		// Exempt CORS preflight requests — browsers send OPTIONS without
		// credentials before the real request. Without this exemption the
		// dashboard would see 401 on every page load in a cross-origin setup.
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		// Constant-time comparison prevents timing attacks that could leak
		// the secret token's length or content byte-by-byte.
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, expected) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("WWW-Authenticate", `Bearer realm="orion"`)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}

		next.ServeHTTP(w, r)
	})
}

// AuthUnaryInterceptor returns a gRPC server interceptor that validates Bearer
// tokens on every unary RPC (SubmitJob, GetJob, etc.).
//
// Token format follows the same convention as the HTTP layer:
//
//	metadata key: "authorization"
//	value:        "Bearer <token>"
//
// Returns codes.Unauthenticated when the token is absent or invalid.
// When apiKey is empty, auth is disabled and all calls pass through.
func AuthUnaryInterceptor(apiKey string) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
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

// AuthStreamInterceptor returns a gRPC server interceptor that validates Bearer
// tokens on every streaming RPC (WatchJob, WatchPipeline).
//
// Streaming RPCs validate the token once at stream open time. After the stream
// is established, no re-authentication is performed per message.
//
// Returns codes.Unauthenticated when the token is absent or invalid.
// When apiKey is empty, auth is disabled and all streams pass through.
func AuthStreamInterceptor(apiKey string) grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		_ *grpc.StreamServerInfo,
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

// validateGRPCToken extracts the "authorization" metadata key from the incoming
// gRPC context and checks it against the expected "Bearer <apiKey>" value.
//
// gRPC metadata keys are always lower-cased; "authorization" is the canonical
// form matching the HTTP/2 specification and gRPC client convention.
//
// Uses constant-time comparison to prevent timing attacks, matching the HTTP
// auth middleware behaviour.
func validateGRPCToken(ctx context.Context, apiKey string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing metadata")
	}

	values := md.Get("authorization")
	if len(values) == 0 {
		return status.Error(codes.Unauthenticated, "missing authorization header")
	}

	// Strip "Bearer " prefix before comparing so the comparison is against
	// the raw token value. This matches the HTTP layer's pre-computed expected
	// value which includes the prefix.
	raw := values[0]
	if !strings.HasPrefix(raw, "Bearer ") {
		return status.Error(codes.Unauthenticated, "authorization header must use Bearer scheme")
	}

	token := strings.TrimPrefix(raw, "Bearer ")
	if subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) != 1 {
		return status.Error(codes.Unauthenticated, "invalid token")
	}

	return nil
}
