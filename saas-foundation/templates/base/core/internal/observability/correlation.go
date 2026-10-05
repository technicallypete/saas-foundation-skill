package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	traceIDKey
)

var (
	requestIDRe   = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)
	traceparentRe = regexp.MustCompile(`^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$`)
)

// ParseTraceparent extracts the trace ID from a W3C traceparent header. It
// rejects malformed values, version ff and all-zero identifiers.
func ParseTraceparent(h string) (traceID string, ok bool) {
	m := traceparentRe.FindStringSubmatch(strings.TrimSpace(h))
	if m == nil || m[1] == "ff" {
		return "", false
	}
	if m[2] == strings.Repeat("0", 32) || m[3] == strings.Repeat("0", 16) {
		return "", false
	}
	return m[2], true
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// RequestID returns the request ID stored in ctx.
func RequestID(ctx context.Context) string { s, _ := ctx.Value(requestIDKey).(string); return s }

// TraceID returns the trace ID stored in ctx.
func TraceID(ctx context.Context) string { s, _ := ctx.Value(traceIDKey).(string); return s }

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Middleware assigns a request ID (accepting a well-formed inbound one),
// continues an inbound W3C trace or starts a new one, and logs the request
// without headers, bodies or query strings.
func Middleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := r.Header.Get("X-Request-Id")
		if !requestIDRe.MatchString(reqID) {
			reqID = randomHex(16)
		}
		traceID, ok := ParseTraceparent(r.Header.Get("traceparent"))
		if !ok {
			traceID = randomHex(16)
		}
		ctx := context.WithValue(context.WithValue(r.Context(), requestIDKey, reqID), traceIDKey, traceID)
		w.Header().Set("X-Request-Id", reqID)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(ctx))
		logger.LogAttrs(ctx, slog.LevelInfo, "request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.String("request_id", reqID),
			slog.String("trace_id", traceID),
		)
	})
}
