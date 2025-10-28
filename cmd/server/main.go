package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"localcloud/internal/api"
	"localcloud/internal/config"
	"localcloud/internal/db"

	"github.com/gorilla/mux"
)

func ensureDir(path string) error {
	return os.MkdirAll(path, 0755)
}

func main() {
	// Load config
	config.LoadConfig()
	dataDir := config.DataDir

	// Ensure data dir exists
	if err := ensureDir(dataDir); err != nil {
		log.Fatalf("failed to create data dir: %v", err)
	}

	// Init DB
	dbPath := filepath.Join(dataDir, "metadata.db")
	db.InitDB(dbPath)

	// Router
	r := mux.NewRouter()

	// Simple CORS for local testing
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			if req.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			next.ServeHTTP(w, req)
		})
	})

	// Register API
	api.RegisterRoutes(r, dataDir)

	// Start thumbnail worker (concurrency 3)
	api.StartThumbnailWorker(3)

	// Static UI (if present)
	webDir := filepath.Join(".", "web")
	if _, err := os.Stat(webDir); err == nil {
		r.PathPrefix("/ui/").Handler(http.StripPrefix("/ui/", http.FileServer(http.Dir(webDir))))
		// root redirect
		r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui/", http.StatusSeeOther)
		})
	}

	bind := ":" + config.BindPort
	log.Printf("LocalCloud API listening on %s (DATA_DIR=%s)\n", bind, dataDir)
	log.Fatal(http.ListenAndServe(bind, r))
}
