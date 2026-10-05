package observability

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRedactsSensitiveKeysAndSecretValues(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, slog.LevelInfo, []string{"s3cr3t-value"})
	log.Info("connecting with s3cr3t-value",
		slog.String("db_password", "anything"),
		slog.String("detail", "dsn contains s3cr3t-value here"),
		slog.Group("nested", slog.String("api_key", "k"), slog.String("note", "ok")),
	)
	out := buf.String()
	if strings.Contains(out, "s3cr3t-value") || strings.Contains(out, "anything") || strings.Contains(out, `"k"`) {
		t.Fatalf("secret leaked: %s", out)
	}
	if !strings.Contains(out, "[REDACTED]") || !strings.Contains(out, `"note":"ok"`) {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestRedactsAttrsAddedWithWith(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, slog.LevelInfo, []string{"tok-123"}).With(slog.String("authorization", "Bearer tok-123"))
	log.Info("x")
	if strings.Contains(buf.String(), "tok-123") {
		t.Fatalf("secret leaked: %s", buf.String())
	}
}

func TestParseTraceparent(t *testing.T) {
	valid := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	if id, ok := ParseTraceparent(valid); !ok || id != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("valid traceparent rejected: %q %v", id, ok)
	}
	for _, bad := range []string{
		"",
		"garbage",
		"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01",
	} {
		if _, ok := ParseTraceparent(bad); ok {
			t.Errorf("accepted malformed traceparent %q", bad)
		}
	}
}

func TestMiddlewareCorrelates(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, slog.LevelInfo, nil)
	var seenTrace, seenReq string
	h := Middleware(log, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenTrace, seenReq = TraceID(r.Context()), RequestID(r.Context())
		w.WriteHeader(http.StatusTeapot)
	}))
	req := httptest.NewRequest(http.MethodGet, "/readyz?token=abc", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	req.Header.Set("X-Request-Id", "req-12345678")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if seenTrace != "4bf92f3577b34da6a3ce929d0e0e4736" || seenReq != "req-12345678" {
		t.Fatalf("correlation not propagated: %q %q", seenTrace, seenReq)
	}
	if rec.Header().Get("X-Request-Id") != "req-12345678" {
		t.Fatal("request ID not echoed")
	}
	if strings.Contains(buf.String(), "token=abc") {
		t.Fatalf("query string logged: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"status":418`) {
		t.Fatalf("status not logged: %s", buf.String())
	}
}

func TestMiddlewareReplacesMalformedRequestID(t *testing.T) {
	log := NewLogger(&bytes.Buffer{}, slog.LevelInfo, nil)
	h := Middleware(log, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "bad id\nwith newline")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Request-Id"); got == "" || strings.Contains(got, " ") {
		t.Fatalf("malformed request ID not replaced: %q", got)
	}
}
