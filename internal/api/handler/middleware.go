package handler

import (
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/shreeharshshinde/orion/internal/observability"
)

// ─────────────────────────────────────────────────────────────────────────────
// MetricsMiddleware
// ─────────────────────────────────────────────────────────────────────────────

// MetricsMiddleware wraps an http.Handler and records Prometheus metrics for
// every request. Apply once to the top-level mux in cmd/api/main.go:
//
//	srv := &http.Server{Handler: handler.MetricsMiddleware(metrics, mux)}
//
// What it records:
//   - orion_http_requests_total{method, path, status_code} counter
//   - orion_http_request_duration_seconds{method, path} histogram
//
// Route label strategy (cardinality control):
//
//	r.Pattern is used as the path label, not r.URL.Path.
//	Go 1.22 sets r.Pattern to the matched route template ("GET /jobs/{id}").
//	Using r.URL.Path would give one label value per unique UUID — cardinality explosion.
//	r.Pattern gives one label value per route — exactly 8 values for Orion's 8 routes.
func MetricsMiddleware(m *observability.Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Wrap ResponseWriter to capture the status code written by the handler.
		// http.ResponseWriter.WriteHeader is called by the handler — we intercept it.
		rw := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(rw, r)

		elapsed := time.Since(start).Seconds()

		// Normalize route: use the pattern set by Go 1.22 mux.
		// Falls back to URL path if pattern is not set (health probes registered
		// with HandleFunc by exact path rather than pattern).
		route := r.Pattern
		if route == "" {
			route = r.URL.Path
		}

		statusStr := strconv.Itoa(rw.statusCode)

		m.HTTPRequestsTotal.WithLabelValues(r.Method, route, statusStr).Inc()
		m.HTTPRequestDuration.WithLabelValues(r.Method, route).Observe(elapsed)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// TracingMiddleware
// ─────────────────────────────────────────────────────────────────────────────

// TracingMiddleware wraps an http.Handler and creates an OpenTelemetry span
// for every HTTP request. The span is named "http.METHOD /route/pattern".
//
// Trace context propagation:
//
//	If the incoming request contains a W3C "traceparent" header (sent by a
//	client SDK or API gateway that already created a trace), this span becomes
//	a child of that trace. This links the full end-to-end trace across services.
//
//	If no traceparent header is present, this is the root span — a new trace
//	is created for this request.
//
// Apply alongside MetricsMiddleware:
//
//	srv := &http.Server{
//	    Handler: handler.TracingMiddleware(
//	        handler.MetricsMiddleware(metrics, mux),
//	    ),
//	}
func TracingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract W3C traceparent from incoming request headers.
		// otel.GetTextMapPropagator() reads the global propagator set in SetupTracing.
		prop := observability.Tracer("orion.api")

		route := r.Pattern
		if route == "" {
			route = r.URL.Path
		}

		spanName := r.Method + " " + route

		ctx, span := prop.Start(r.Context(), spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.method", r.Method),
				attribute.String("http.route", route),
				attribute.String("http.url", r.URL.String()),
			),
		)
		defer span.End()

		rw := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r.WithContext(ctx))

		span.SetAttributes(attribute.Int("http.status_code", rw.statusCode))

		if rw.statusCode >= 500 {
			span.SetStatus(codes.Error, "server error")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// statusRecorder
// ─────────────────────────────────────────────────────────────────────────────

// statusRecorder wraps http.ResponseWriter to capture the HTTP status code
// written by the downstream handler. Both middlewares use this.
//
// The default status is 200 OK — if the handler never calls WriteHeader
// (which is valid and means 200), we still record the correct value.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}
