// Package middleware provides HTTP middleware for JWT auth, rate limiting, and OTel tracing.
package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
)

// contextKey is an unexported type for context keys in this package.
type contextKey string

const claimsKey contextKey = "jwt_claims"

// Claims represents JWT claims extracted from a request.
type Claims struct {
	Subject string
	jwt.RegisteredClaims
}

// JWTAuth returns middleware that validates Bearer JWT tokens using the provided secret.
// Requests without a valid token receive 401 Unauthorized.
func JWTAuth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := r.Header.Get("Authorization")
			if len(tokenStr) > 7 && tokenStr[:7] == "Bearer " {
				tokenStr = tokenStr[7:]
			} else {
				http.Error(w, "missing or malformed Authorization header", http.StatusUnauthorized)
				return
			}

			claims := &Claims{}
			token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
				return []byte(secret), nil
			})
			if err != nil || !token.Valid {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), claimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClaimsFromContext extracts JWT claims from the request context.
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(claimsKey).(*Claims)
	return c, ok
}

// clientBucket holds rate limit state for a single client.
type clientBucket struct {
	tokens   float64
	lastSeen time.Time
	mu       sync.Mutex
}

// RateLimiter returns middleware that limits requests per second per client IP.
// Clients exceeding the limit receive 429 Too Many Requests.
func RateLimiter(rps int) func(http.Handler) http.Handler {
	clients := sync.Map{}
	rate := float64(rps)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := r.RemoteAddr

			val, _ := clients.LoadOrStore(ip, &clientBucket{tokens: rate, lastSeen: time.Now()})
			bucket := val.(*clientBucket)

			bucket.mu.Lock()
			now := time.Now()
			elapsed := now.Sub(bucket.lastSeen).Seconds()
			bucket.tokens = min(rate, bucket.tokens+elapsed*rate)
			bucket.lastSeen = now

			if bucket.tokens < 1 {
				bucket.mu.Unlock()
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			bucket.tokens--
			bucket.mu.Unlock()

			next.ServeHTTP(w, r)
		})
	}
}

// RequestLogger returns middleware that logs each request with trace context using slog.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		span := trace.SpanFromContext(r.Context())
		sc := span.SpanContext()

		wrapped := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(wrapped, r)

		slog.InfoContext(r.Context(), "http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", wrapped.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"trace_id", sc.TraceID().String(),
			"span_id", sc.SpanID().String(),
		)
	})
}

// OTelTracing wraps the handler with OpenTelemetry HTTP instrumentation.
func OTelTracing(operation string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, operation)
	}
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
