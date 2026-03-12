module github.com/shreeharshshinde/orion

go 1.22

require (
	// PostgreSQL driver — pgx is faster than database/sql because it uses
	// PostgreSQL's native binary protocol instead of converting everything to strings
	github.com/jackc/pgx/v5 v5.5.4

	// Redis client with full support for Streams, consumer groups, pipelines
	github.com/redis/go-redis/v9 v9.5.1

	// Kubernetes Go client — for launching K8s Jobs (Phase 5)
	k8s.io/client-go v0.29.2
	k8s.io/api v0.29.2
	k8s.io/apimachinery v0.29.2

	// gRPC — for streaming APIs (Phase 7)
	google.golang.org/grpc v1.62.1
	google.golang.org/protobuf v1.33.0

	// OpenTelemetry — distributed tracing SDK
	go.opentelemetry.io/otel v1.24.0
	go.opentelemetry.io/otel/sdk v1.24.0
	go.opentelemetry.io/otel/trace v1.24.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.24.0

	// Prometheus — metrics collection
	github.com/prometheus/client_golang v1.19.0

	// UUID generation for job IDs
	github.com/google/uuid v1.6.0
)