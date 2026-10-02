package httpserver

import (
	"context"
	"encoding/json"
	"example.com/gogo-services/apps/catalog"
	"example.com/gogo-services/internal/database"
	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/security"
	"net/http"
	"time"
)

// Handler exposes only public demonstration counts, never records or writes.
// A real private API must add authentication and authorization explicitly.
func Handler(connection *database.Connection, settings conf.Values) (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /health/ready/", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := connection.Backend.Ping(ctx); err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /products/count/", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), settings.Duration("GOGO_DB_QUERY_TIMEOUT"))
		defer cancel()
		count, err := catalog.Count(ctx, connection.Store)
		if err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]int64{"count": count})
	})
	headers, err := security.Headers(security.HeadersConfig{AllowedHosts: settings.List("GOGO_ALLOWED_HOSTS")})
	if err != nil {
		return nil, err
	}
	return headers(mux), nil
}
