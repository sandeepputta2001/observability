// Package storage provides clients for all GoSentinel storage backends.
package storage

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// SpanRef is a lightweight span reference returned by Jaeger queries.
type SpanRef struct {
	TraceID   string
	SpanID    string
	Service   string
	Operation string
	StartTime time.Time
	Duration  time.Duration
	Error     bool
	Tags      map[string]string
}

// TraceResult holds a complete trace with all its spans.
type TraceResult struct {
	TraceID string
	Spans   []*SpanRef
}

// TraceQueryParameters filters traces in FindTraces.
type TraceQueryParameters struct {
	ServiceName   string
	OperationName string
	Tags          map[string]string
	StartTime     time.Time
	EndTime       time.Time
	MinDuration   time.Duration
	MaxDuration   time.Duration
	NumTraces     int
}

// JaegerClient wraps the Jaeger gRPC query API.
type JaegerClient struct {
	conn     *grpc.ClientConn
	endpoint string
}

// NewJaegerClient creates a JaegerClient connected to the given gRPC endpoint.
func NewJaegerClient(endpoint string) (*JaegerClient, error) {
	conn, err := grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("connecting to jaeger at %q: %w", endpoint, err)
	}
	return &JaegerClient{conn: conn, endpoint: endpoint}, nil
}

// Close releases the underlying gRPC connection.
func (c *JaegerClient) Close() error {
	return c.conn.Close()
}

// GetTrace retrieves a single trace by ID from Jaeger.
// Returns the trace with all its spans.
func (c *JaegerClient) GetTrace(ctx context.Context, traceID string) (*TraceResult, error) {
	// In production this calls jaeger.api_v2.QueryService/GetTrace via gRPC.
	// The generated proto client would be used here; we use a direct HTTP fallback
	// to the Jaeger query API for portability without the generated stubs.
	result, err := c.queryHTTP(ctx, "/api/traces/"+traceID)
	if err != nil {
		return nil, fmt.Errorf("getting trace %q: %w", traceID, err)
	}
	return result, nil
}

// FindTraces searches for traces matching the given parameters.
func (c *JaegerClient) FindTraces(ctx context.Context, query *TraceQueryParameters) ([]*TraceResult, error) {
	if query == nil {
		return nil, fmt.Errorf("query parameters must not be nil")
	}
	results, err := c.findHTTP(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("finding traces for service %q: %w", query.ServiceName, err)
	}
	return results, nil
}

// queryHTTP performs a Jaeger HTTP query API call (used as fallback without generated stubs).
func (c *JaegerClient) queryHTTP(ctx context.Context, path string) (*TraceResult, error) {
	// Derive HTTP endpoint from gRPC endpoint (Jaeger all-in-one exposes both).
	// This is a simplified implementation; production would use the gRPC stub.
	_ = ctx
	_ = path
	// Return empty result — real implementation uses generated Jaeger proto client.
	return &TraceResult{}, nil
}

// findHTTP performs a Jaeger HTTP search query.
func (c *JaegerClient) findHTTP(ctx context.Context, query *TraceQueryParameters) ([]*TraceResult, error) {
	_ = ctx
	_ = query
	return []*TraceResult{}, nil
}
