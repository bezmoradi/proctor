package health

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/bezmoradi/proctor/examples/sample/internal/core/shared"
)

const readyTimeout = 2 * time.Second

type Check struct {
	Name  string
	Probe func(context.Context) error
}

func Live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func Ready(logger shared.Logger, checks ...Check) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()

		failed := make([]string, len(checks))
		var wg sync.WaitGroup
		for i, check := range checks {
			wg.Go(func() {
				if err := check.Probe(ctx); err != nil {
					logger.Error("readiness check failed", "check", check.Name, "error", err)
					failed[i] = check.Name
				}
			})
		}
		wg.Wait()

		failed = slices.DeleteFunc(failed, func(name string) bool { return name == "" })
		if len(failed) > 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "failed": failed})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
	}
}

func writeJSON(w http.ResponseWriter, code int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
