// Package health serves liveness and readiness. Readiness reports dependency
// state by name only; it never includes secrets, DSNs or error details.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Check probes one dependency. Required checks gate readiness; optional
// checks are reported but never make the service unready.
type Check struct {
	Name     string
	Required bool
	Probe    func(ctx context.Context) error
}

// Handler serves /healthz and /readyz.
type Handler struct {
	ProductName string
	Checks      []Check
	Timeout     time.Duration
}

type readiness struct {
	Status      string            `json:"status"`
	ProductName string            `json:"product_name"`
	Checks      map[string]string `json:"checks"`
	Optional    map[string]string `json:"optional,omitempty"`
}

// Liveness reports that the process is serving. It has no dependencies.
func (h Handler) Liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

// Readiness runs every check concurrently and reports their state.
func (h Handler) Readiness(w http.ResponseWriter, r *http.Request) {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	type result struct {
		check Check
		err   error
	}
	results := make([]result, len(h.Checks))
	var wg sync.WaitGroup
	for i, c := range h.Checks {
		wg.Add(1)
		go func(i int, c Check) {
			defer wg.Done()
			results[i] = result{check: c, err: c.Probe(ctx)}
		}(i, c)
	}
	wg.Wait()

	body := readiness{Status: "ready", ProductName: h.ProductName, Checks: map[string]string{}}
	sort.Slice(results, func(i, j int) bool { return results[i].check.Name < results[j].check.Name })
	for _, res := range results {
		state := "ok"
		if res.err != nil {
			state = "unavailable"
		}
		if res.check.Required {
			body.Checks[res.check.Name] = state
			if res.err != nil {
				body.Status = "unready"
			}
			continue
		}
		if body.Optional == nil {
			body.Optional = map[string]string{}
		}
		body.Optional[res.check.Name] = state
	}
	code := http.StatusOK
	if body.Status != "ready" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, body)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
