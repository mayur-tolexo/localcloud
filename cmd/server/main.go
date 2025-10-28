package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"localcloud/internal/api"
	"localcloud/internal/config"
	"localcloud/internal/db"
	"localcloud/internal/middleware"

	"github.com/gorilla/mux"
)

func ensureDir(p string) error {
	return os.MkdirAll(p, 0755)
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

	// initialize media table for sync/backup
	if err := api.InitSyncDB(); err != nil {
		log.Fatalf("InitSyncDB failed: %v", err)
	}

	// start workers (thumbnail worker may already be started)
	api.StartThumbnailWorker(3) // if not already started elsewhere

	// start backup worker - store backups under DATA_DIR/backups (or change path)
	backupDir := filepath.Join(dataDir, "backups")
	api.StartBackupWorker(3, backupDir)

	// Router
	r := mux.NewRouter()

	// Simple CORS for local testing (register BEFORE wrapping with auth)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			if req.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			next.ServeHTTP(w, req)
		})
	})

	// Register API routes (and static UI) on router
	api.RegisterRoutes(r, dataDir)

	// Static UI (if present)
	webDir := filepath.Join(".", "web")
	if _, err := os.Stat(webDir); err == nil {
		r.PathPrefix("/ui/").Handler(http.StripPrefix("/ui/", http.FileServer(http.Dir(webDir))))
		// root redirect
		r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui/", http.StatusSeeOther)
		})
	}

	// Start thumbnail worker (concurrency 3)
	api.StartThumbnailWorker(3)

	// Protect all routes with Basic Auth — wrap the fully configured router
	protected := middleware.BasicAuth(r)

	// Bind & serve
	bind := ":" + config.BindPort
	log.Printf("LocalCloud API listening on %s (DATA_DIR=%s)\n", bind, dataDir)
	log.Fatal(http.ListenAndServe(bind, protected))
}
