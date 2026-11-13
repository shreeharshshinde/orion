package logsink

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

// S3Sink uploads execution logs to an S3-compatible object store.
//
// Compatible with:
//   - AWS S3     — set region, leave endpoint empty.
//   - MinIO      — set endpoint to http://localhost:9000.
//   - Cloudflare R2 — set endpoint to https://<account>.r2.cloudflarestorage.com.
//
// URI format returned by Flush:
//
//	s3://<bucket>/<prefix>/<jobID>/<execID>.log
//
// S3Sink uses PutObject, not multipart upload, because execution logs are
// typically small (< 100 MB) and are uploaded once at the end of execution.
// For very large logs, consider a streaming approach in future iterations.
//
// The sink is goroutine-safe — the underlying s3.Client is concurrency-safe.
type S3Sink struct {
	client *s3.Client
	bucket string
	prefix string // optional key prefix, e.g. "orion/logs"
}

// S3Config holds all parameters needed to construct an S3Sink.
type S3Config struct {
	// Bucket is the S3 bucket name. Required.
	Bucket string

	// Prefix is an optional key prefix. Logs are stored at:
	// <Prefix>/<jobID>/<execID>.log (or <jobID>/<execID>.log if empty).
	Prefix string

	// Region is the AWS region, e.g. "us-east-1". Required for AWS S3.
	Region string

	// Endpoint overrides the default AWS endpoint.
	// Set to your MinIO or R2 endpoint URL for self-hosted storage.
	// Leave empty for AWS S3.
	Endpoint string

	// AccessKeyID and SecretAccessKey are the S3 credentials.
	// Leave both empty to use the default credential chain (instance role,
	// environment variables AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY, etc.).
	AccessKeyID     string
	SecretAccessKey string
}

// NewS3Sink creates an S3Sink using the provided configuration.
// It performs no I/O at construction time — connectivity is validated
// lazily on the first Flush call.
func NewS3Sink(ctx context.Context, cfg S3Config) (*S3Sink, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("logsink: S3Sink bucket must not be empty")
	}
	if cfg.Region == "" {
		return nil, fmt.Errorf("logsink: S3Sink region must not be empty")
	}

	// Build AWS SDK options.
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}

	// Static credentials override the default credential chain.
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				cfg.AccessKeyID,
				cfg.SecretAccessKey,
				"", // session token (empty for long-term credentials)
			),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("logsink: loading AWS config: %w", err)
	}

	// S3 client options (custom endpoint for MinIO/R2).
	s3Opts := []func(*s3.Options){}
	if cfg.Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			// Path-style is required by MinIO and many S3-compatible stores.
			o.UsePathStyle = true
		})
	}

	client := s3.NewFromConfig(awsCfg, s3Opts...)

	return &S3Sink{
		client: client,
		bucket: cfg.Bucket,
		prefix: cfg.Prefix,
	}, nil
}

// Writer returns a fresh *bytes.Buffer. The worker pool passes the same
// buffer's contents to Flush after execution completes.
func (s *S3Sink) Writer() io.Writer { return &bytes.Buffer{} }

// Flush uploads data to S3 as a single PutObject call.
//
// Returns "" (and no error) when data is empty — no object is uploaded for
// executions that produced no log output.
//
// The object key is:  [<prefix>/]<jobID>/<execID>.log
func (s *S3Sink) Flush(ctx context.Context, jobID, execID uuid.UUID, data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}

	key := s.objectKey(jobID, execID)

	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("text/plain; charset=utf-8"),
	})
	if err != nil {
		return "", fmt.Errorf("logsink: S3 PutObject to s3://%s/%s: %w", s.bucket, key, err)
	}

	return fmt.Sprintf("s3://%s/%s", s.bucket, key), nil
}

// objectKey constructs the S3 object key for a given job/exec pair.
// If a prefix was configured, it is prepended with a slash separator.
func (s *S3Sink) objectKey(jobID, execID uuid.UUID) string {
	name := fmt.Sprintf("%s/%s.log", jobID.String(), execID.String())
	if s.prefix != "" {
		return s.prefix + "/" + name
	}
	return name
}
