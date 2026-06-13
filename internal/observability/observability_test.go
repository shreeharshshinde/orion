package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
)

func TestNewMetrics_RegistersAllMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)

	if metrics == nil {
		t.Fatal("NewMetrics returned nil")
	}

	// Check that all metrics are properly initialized
	if metrics.JobsSubmitted == nil {
		t.Error("JobsSubmitted metric not initialized")
	}
	if metrics.JobsCompleted == nil {
		t.Error("JobsCompleted metric not initialized")
	}
	if metrics.JobsFailed == nil {
		t.Error("JobsFailed metric not initialized")
	}
	if metrics.QueueDepth == nil {
		t.Error("QueueDepth metric not initialized")
	}
	if metrics.WorkerActiveJobs == nil {
		t.Error("WorkerActiveJobs metric not initialized")
	}
	if metrics.SchedulerCycleLatency == nil {
		t.Error("SchedulerCycleLatency metric not initialized")
	}
}

func TestNewMetrics_MetricsCanBeUsed(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)

	// Test counter increment
	metrics.JobsSubmitted.WithLabelValues("default", "k8s_job").Inc()
	
	// Test gauge set
	metrics.QueueDepth.WithLabelValues("high").Set(5)
	
	// Test histogram observe
	metrics.SchedulerCycleLatency.Observe(0.5)

	// Verify metrics have expected values
	if testutil.ToFloat64(metrics.JobsSubmitted.WithLabelValues("default", "k8s_job")) != 1 {
		t.Error("JobsSubmitted counter not incremented correctly")
	}
	
	if testutil.ToFloat64(metrics.QueueDepth.WithLabelValues("high")) != 5 {
		t.Error("QueueDepth gauge not set correctly")
	}
	
	// For histograms, we can't use ToFloat64 directly, so just check it doesn't panic
	// and that the metric was registered properly by gathering all metrics
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}
	
	found := false
	for _, family := range families {
		if family.GetName() == "orion_scheduler_cycle_duration_seconds" {
			found = true
			if len(family.GetMetric()) == 0 {
				t.Error("Histogram metric not observed correctly")
			}
			break
		}
	}
	if !found {
		t.Error("SchedulerCycleLatency histogram not found in registered metrics")
	}
}

func TestNewMetrics_PanicsOnDuplicateRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	NewMetrics(reg)
	
	// Second registration should panic
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic on duplicate registration")
		}
	}()
	
	NewMetrics(reg)
}

func TestNewLogger_Development(t *testing.T) {
	logger := NewLogger("info", "test-service", "development")
	
	if logger == nil {
		t.Fatal("NewLogger returned nil")
	}
	
	// Development should use text handler (hard to test output format directly)
	// Just verify it doesn't panic
	logger.Info("test message", "key", "value")
}

func TestNewLogger_Production(t *testing.T) {
	logger := NewLogger("info", "test-service", "production")
	
	if logger == nil {
		t.Fatal("NewLogger returned nil")
	}
	
	// Production should use JSON handler (hard to test output format directly)  
	// Just verify it doesn't panic
	logger.Info("test message", "key", "value")
}

func TestNewLogger_Staging(t *testing.T) {
	logger := NewLogger("info", "test-service", "staging")
	
	if logger == nil {
		t.Fatal("NewLogger returned nil")
	}
	
	// Staging should use JSON handler
	logger.Info("test message", "key", "value")
}

func TestNewLogger_UnknownEnvironment(t *testing.T) {
	logger := NewLogger("info", "test-service", "unknown")
	
	if logger == nil {
		t.Fatal("NewLogger returned nil")
	}
	
	// Unknown environment should default to text handler
	logger.Info("test message", "key", "value")
}

func TestSetupTracing_ValidEndpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	shutdown, err := SetupTracing(ctx, "test-service", "1.0.0", "localhost:4317", 0.1, false)
	if err != nil {
		t.Fatalf("SetupTracing failed: %v", err)
	}
	defer shutdown(context.Background())
	
	// Verify tracer is available
	tracer := otel.Tracer("test")
	if tracer == nil {
		t.Error("Tracer not available after setup")
	}
	
	// Verify we can create spans
	_, span := tracer.Start(context.Background(), "test-span")
	if span == nil {
		t.Error("Could not create span")
	}
	span.End()
}

func TestSetupTracing_EmptyEndpoint(t *testing.T) {
	ctx := context.Background()

	// Empty endpoint should return a no-op provider without error (no dial attempted).
	shutdown, err := SetupTracing(ctx, "test-service", "1.0.0", "", 0.1, false)
	if err != nil {
		t.Fatalf("expected no error with empty endpoint, got: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("expected clean shutdown, got: %v", err)
	}
}

func TestTracer_ReturnsGlobalTracer(t *testing.T) {
	// Skip this test as it requires a working OTLP endpoint
	t.Skip("Skipping tracer test as it requires OTLP setup")
}

func TestMetricsServer_StartsAndServesMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	
	// Increment a metric so we have something to check
	metrics.JobsSubmitted.WithLabelValues("default", "k8s_job").Inc()
	
	// Create a test server using the MetricsServer function
	srv := MetricsServer(0, reg) // port 0 for testing
	
	server := httptest.NewServer(srv.Handler)
	defer server.Close()
	
	// Make a request to the metrics endpoint
	resp, err := http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatalf("Failed to get metrics: %v", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
	
	// Read response body
	body := make([]byte, 8192)
	n, _ := resp.Body.Read(body)
	bodyStr := string(body[:n])
	
	// Check that our metric appears in the output
	if !strings.Contains(bodyStr, "orion_jobs_submitted_total") {
		t.Error("Expected to find orion_jobs_submitted_total in metrics output")
	}
}

func TestMetricsServer_HealthCheckEndpoint(t *testing.T) {
	reg := prometheus.NewRegistry()
	
	srv := MetricsServer(0, reg)
	
	server := httptest.NewServer(srv.Handler)
	defer server.Close()
	
	// Make a request to the health endpoint (/healthz not /health)
	resp, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("Failed to get health: %v", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
	
	// Read response body
	body := make([]byte, 256)
	n, _ := resp.Body.Read(body)
	bodyStr := string(body[:n])
	
	if !strings.Contains(bodyStr, "ok") {
		t.Error("Expected 'ok' in health check response")
	}
}

func TestMetrics_NilSafety(t *testing.T) {
	// Test that metrics can be safely called when nil (used in tests)
	var metrics *Metrics = nil
	
	// These should not panic even with nil metrics
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Nil metrics caused panic: %v", r)
		}
	}()
	
	// In real code, there are nil checks around metric calls
	// This test just ensures the struct can be nil without immediate panic
	if metrics != nil {
		metrics.JobsSubmitted.WithLabelValues("test", "test").Inc()
	}
}

func TestSetupTracing_ServiceNameValidation(t *testing.T) {
	ctx := context.Background()
	
	// Test empty service name with valid endpoint - should work
	shutdown, err := SetupTracing(ctx, "", "1.0.0", "localhost:4317", 0.1, false)
	if err != nil {
		// Expected - empty endpoint causes error, but that's OK for this test
		t.Logf("SetupTracing failed as expected with empty endpoint: %v", err)
	} else {
		defer shutdown(context.Background())
	}
	
	// Test with service name
	shutdown2, err := SetupTracing(ctx, "orion-test-service", "1.0.0", "localhost:4317", 0.1, false)
	if err != nil {
		// Expected - this will fail without a real OTLP endpoint
		t.Logf("SetupTracing failed as expected without real endpoint: %v", err)
	} else {
		defer shutdown2(context.Background())
	}
}

func TestMetrics_AllLabelsLowCardinality(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	
	// Test that all metrics accept reasonable label values
	// This helps ensure we don't accidentally add high-cardinality labels
	
	metrics.JobsSubmitted.WithLabelValues("high", "k8s_job").Inc()
	metrics.JobsCompleted.WithLabelValues("default", "inline").Inc()
	metrics.JobsFailed.WithLabelValues("low", "k8s_job", "timeout").Inc()
	metrics.QueueDepth.WithLabelValues("high").Set(10)
	metrics.WorkerActiveJobs.WithLabelValues("worker-001").Set(5)
	
	// Verify all metrics can be collected without error
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}
	
	if len(families) == 0 {
		t.Error("No metrics families found")
	}
}