package handler

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/shreeharshshinde/orion/internal/observability"
)

func TestTracingMiddleware_ExtractsTraceparent(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	const incomingTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	const incomingSpanID = "00f067aa0ba902b7"
	traceparent := "00-" + incomingTraceID + "-" + incomingSpanID + "-01"

	var capturedSpan trace.Span
	innerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedSpan = trace.SpanFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := TracingMiddleware(innerHandler)

	req := httptest.NewRequest(http.MethodGet, "/jobs/123", nil)
	req.Header.Set("traceparent", traceparent)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if capturedSpan == nil || !capturedSpan.SpanContext().IsValid() {
		t.Fatal("expected valid span in request context")
	}

	if got := capturedSpan.SpanContext().TraceID().String(); got != incomingTraceID {
		t.Errorf("expected TraceID %q, got %q", incomingTraceID, got)
	}
}

func TestTracingMiddleware_CreatesRootSpanWhenNoTraceparent(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	var capturedSpan trace.Span
	innerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedSpan = trace.SpanFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := TracingMiddleware(innerHandler)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if capturedSpan == nil || !capturedSpan.SpanContext().IsValid() {
		t.Fatal("expected valid span in request context")
	}

	if capturedSpan.SpanContext().TraceID().String() == "" {
		t.Error("expected non-empty TraceID")
	}
}

func TestMetricsMiddleware_RecordsMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)

	innerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	handler := MetricsMiddleware(metrics, innerHandler)

	req := httptest.NewRequest(http.MethodPost, "/jobs", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	count := testutil.ToFloat64(metrics.HTTPRequestsTotal.WithLabelValues("POST", "/jobs", "201"))
	if count != 1 {
		t.Errorf("expected HTTPRequestsTotal=1, got %v", count)
	}
}

func TestRequestLogger_EnrichesWithTrace(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)

	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-span")
	defer span.End()

	baseLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)

	enriched := RequestLogger(req, baseLogger)
	if enriched == baseLogger {
		t.Error("expected enriched logger to differ from base logger when span is present")
	}
}
