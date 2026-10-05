package health

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func ok(context.Context) error   { return nil }
func fail(context.Context) error { return errors.New("dial tcp 10.0.0.1:5432: secret-dsn-detail") }

func readiness200(t *testing.T, h Handler) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if strings.Contains(rec.Body.String(), "secret-dsn-detail") {
		t.Fatal("readiness leaks error detail")
	}
	return rec.Code, body
}

func TestReadyWhenAllRequiredChecksPass(t *testing.T) {
	code, body := readiness200(t, Handler{ProductName: "Sample", Checks: []Check{
		{Name: "postgres", Required: true, Probe: ok},
		{Name: "redis", Required: true, Probe: ok},
	}})
	if code != http.StatusOK || body["status"] != "ready" || body["product_name"] != "Sample" {
		t.Fatalf("unexpected: %d %v", code, body)
	}
}

func TestUnreadyNamesFailingDependency(t *testing.T) {
	code, body := readiness200(t, Handler{Checks: []Check{
		{Name: "postgres", Required: true, Probe: ok},
		{Name: "redis", Required: true, Probe: fail},
	}})
	if code != http.StatusServiceUnavailable || body["status"] != "unready" {
		t.Fatalf("unexpected: %d %v", code, body)
	}
	checks := body["checks"].(map[string]any)
	if checks["redis"] != "unavailable" || checks["postgres"] != "ok" {
		t.Fatalf("unexpected checks: %v", checks)
	}
}

func TestOptionalCheckNeverGatesReadiness(t *testing.T) {
	code, body := readiness200(t, Handler{Checks: []Check{
		{Name: "postgres", Required: true, Probe: ok},
		{Name: "intelligence", Required: false, Probe: fail},
	}})
	if code != http.StatusOK {
		t.Fatalf("optional failure must not gate readiness: %d", code)
	}
	if body["optional"].(map[string]any)["intelligence"] != "unavailable" {
		t.Fatalf("optional state not reported: %v", body)
	}
}

func TestLivenessHasNoDependencies(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler{Checks: []Check{{Name: "postgres", Required: true, Probe: fail}}}.Liveness(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("liveness = %d", rec.Code)
	}
}

func fakeRedis(t *testing.T, password string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				authed := password == ""
				for {
					header, err := r.ReadString('\n')
					if err != nil {
						return
					}
					n := 0
					for _, ch := range strings.TrimSpace(header[1:]) {
						n = n*10 + int(ch-'0')
					}
					args := make([]string, n)
					for i := range args {
						_, _ = r.ReadString('\n')
						v, _ := r.ReadString('\n')
						args[i] = strings.TrimRight(v, "\r\n")
					}
					switch {
					case args[0] == "AUTH" && len(args) == 2 && args[1] == password:
						authed = true
						c.Write([]byte("+OK\r\n"))
					case args[0] == "AUTH":
						c.Write([]byte("-WRONGPASS\r\n"))
					case args[0] == "PING" && authed:
						c.Write([]byte("+PONG\r\n"))
					default:
						c.Write([]byte("-NOAUTH Authentication required.\r\n"))
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func TestRedisPingWithAuth(t *testing.T) {
	addr := fakeRedis(t, "pw")
	if err := Redis(addr, "pw").Probe(context.Background()); err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if err := Redis(addr, "wrong").Probe(context.Background()); err == nil {
		t.Fatal("expected authentication failure")
	}
	if err := Redis(addr, "").Probe(context.Background()); err == nil {
		t.Fatal("expected NOAUTH failure without a password")
	}
}

func TestIntelligenceCheckIsOptional(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := Intelligence(srv.URL)
	if c.Required {
		t.Fatal("intelligence check must be optional")
	}
	if err := c.Probe(context.Background()); err != nil {
		t.Fatalf("expected healthy: %v", err)
	}
}
