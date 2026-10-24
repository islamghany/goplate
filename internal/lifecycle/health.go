package lifecycle

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

const (
	checkTimeout = 2 * time.Second
	cacheFor     = time.Second
)

// health serves liveness and readiness probes.
//
//   - /livez answers "is the process alive?" and NEVER checks dependencies:
//     a database blip must not make Kubernetes restart every pod at once.
//   - /readyz answers "should I receive traffic?": it runs every Checker in
//     parallel (each with a timeout) and fails during shutdown. Results are
//     cached briefly so aggressive probing cannot overload dependencies.
type health struct {
	m *Manager

	mu      sync.Mutex
	at      time.Time
	results map[string]string // check name -> "ok" or error text
	ok      bool
}

func newHealth(m *Manager) *health { return &health{m: m} }

// LiveHandler serves the liveness probe.
func (m *Manager) LiveHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

// ReadyHandler serves the readiness probe. With verbose set, per-check
// results are included: expose that only on the internal admin port, since
// error texts can reveal infrastructure details.
func (m *Manager) ReadyHandler(verbose bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.Ready() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not ready"})
			return
		}
		ok, results := m.health.run(r.Context())
		body := map[string]any{"status": "ok"}
		status := http.StatusOK
		if !ok {
			body["status"] = "unavailable"
			status = http.StatusServiceUnavailable
		}
		if verbose {
			body["checks"] = results
		}
		writeJSON(w, status, body)
	})
}

func (h *health) run(ctx context.Context) (bool, map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Since(h.at) < cacheFor && h.results != nil {
		return h.ok, h.results
	}

	results := make(map[string]string, len(h.m.checkers))
	var mu sync.Mutex
	var wg sync.WaitGroup
	allOK := true
	for _, c := range h.m.checkers {
		wg.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, checkTimeout)
			defer cancel()
			res := "ok"
			if err := c.Check(cctx); err != nil {
				res = err.Error()
			}
			mu.Lock()
			defer mu.Unlock()
			results[c.Name()] = res
			if res != "ok" {
				allOK = false
			}
		})
	}
	wg.Wait()

	h.at, h.results, h.ok = time.Now(), results, allOK
	return allOK, results
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
