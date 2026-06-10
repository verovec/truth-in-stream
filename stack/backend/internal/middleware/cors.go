package middleware

import "net/http"

// CORS allows browser requests from allowedOrigin. In production the frontend
// and API share an origin, so CORS is unnecessary and this middleware is left
// out of the chain; in local dev the browser calls the backend cross-origin
// (the page is served from a different port), so the configured origin must be
// reflected and the preflight OPTIONS request answered before it reaches the
// route mux, which only registers GET and POST.
func CORS(allowedOrigin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", allowedOrigin)
			h.Set("Vary", "Origin")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Content-Type")
				h.Set("Access-Control-Max-Age", "300")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
