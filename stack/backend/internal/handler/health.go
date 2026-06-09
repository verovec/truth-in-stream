package handler

import (
	"net/http"

	"github.com/verovec/truth-in-stream/backend/internal/httpx"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// healthHandler returns 200 when dependencies are healthy, else 503.
func healthHandler(health *service.HealthChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := health.Check(r.Context()); err != nil {
			httpx.Error(w, http.StatusServiceUnavailable, "unhealthy")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
