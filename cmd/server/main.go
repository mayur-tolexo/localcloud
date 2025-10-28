package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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

	// Create full DB path inside DATA dir
	dbPath := filepath.Join(dataDir, "metadata.db")

	// Ensure the parent directory exists
	if err := ensureDir(filepath.Dir(dbPath)); err != nil {
		log.Fatalf("failed to create DB directory: %v", err)
	}

	// Create DB file if missing
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		file, err := os.Create(dbPath)
		if err != nil {
			log.Fatalf("failed to create metadata.db: %v", err)
		}
		file.Close()
		log.Printf("Created new database at %s", dbPath)
	}

	// Ensure writable permissions
	if err := os.Chmod(dbPath, 0664); err != nil {
		log.Printf("warning: unable to set db file permissions: %v", err)
	}

	// Initialize the database
	db.InitDB(dbPath)

	// synchronous indexing at startup and enqueue thumbnails
	go func() {
		processed, err := db.IndexDataDirSync(dataDir)
		if err != nil {
			log.Printf("background indexing error: %v", err)
			return
		}
		log.Printf("background indexed %d files. Enqueuing thumbs...", len(processed))
		for _, apiPath := range processed {
			api.EnqueueThumbnail(filepath.Join(dataDir, strings.TrimPrefix(apiPath, "/")))
		}
	}()

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
	r.Use(middleware.RecoverJSON)

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
