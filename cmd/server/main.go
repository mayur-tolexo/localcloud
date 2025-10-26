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

	// Initialize DB (metadata.db inside dataDir)
	dbPath := filepath.Join(dataDir, "metadata.db")
	db.InitDB(dbPath)

	// Router
	r := mux.NewRouter()
	api.RegisterRoutes(r, dataDir)

	addr := ":" + config.BindPort
	log.Printf("LocalCloud API starting on %s (DATA_DIR=%s)\n", addr, dataDir)
	log.Fatal(http.ListenAndServe(addr, r))
}
