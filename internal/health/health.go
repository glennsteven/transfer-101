package health

import (
	"context"
	"io"
	"net/http"
	"time"
)

func NewHandler(ping func(context.Context) error, timeout time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) {
		writeStatus(w, http.StatusOK, "{\"status\":\"alive\"}\n")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		if err := ping(ctx); err != nil {
			writeStatus(w, http.StatusServiceUnavailable, "{\"status\":\"not_ready\"}\n")
			return
		}
		writeStatus(w, http.StatusOK, "{\"status\":\"ready\"}\n")
	})
	return mux
}

func writeStatus(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
