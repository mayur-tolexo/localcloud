package middleware

import (
	"net/http"
	"os"
)

func BasicAuth(next http.Handler) http.Handler {
	user := os.Getenv("APP_USER")
	pass := os.Getenv("APP_PASS")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
