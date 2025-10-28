package api

import (
	"github.com/gorilla/mux"
)

// RegisterRoutes registers API routes. Pass in mux router and the DataDir path.
func RegisterRoutes(r *mux.Router, dataDir string) {
	// set global DataDir used by handlers
	DataDir = dataDir

	// file management
	r.HandleFunc("/api/upload", UploadHandler).Methods("POST")
	r.HandleFunc("/api/files", ListHandler).Methods("GET")
	r.HandleFunc("/api/delete/{filename}", DeleteHandler).Methods("DELETE")
	r.HandleFunc("/api/health", HealthHandler).Methods("GET")

	// filesystem browsing & file serving
	r.HandleFunc("/api/tree", TreeHandler).Methods("GET")
	r.HandleFunc("/api/file", FileHandler).Methods("GET")

	// thumbnails, metadata, grid view
	r.HandleFunc("/api/thumbnail", ThumbnailHandler).Methods("GET")
	r.HandleFunc("/api/metadata", MetadataHandler).Methods("GET")
	r.HandleFunc("/api/grid", GridHandler).Methods("GET")

	// sync & backup
	r.HandleFunc("/api/sync/upload", SyncUploadHandler).Methods("POST")
	r.HandleFunc("/api/sync/status", SyncStatusHandler).Methods("GET")

	// search
	r.HandleFunc("/api/search", SearchHandler).Methods("GET")
}
