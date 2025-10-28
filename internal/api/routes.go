package api

import (
	"localcloud/internal/config"

	"github.com/gorilla/mux"
)

func RegisterRoutes(r *mux.Router, dataDir string) {
	DataDir = dataDir
	config.LoadConfig()

	r.HandleFunc("/api/upload", UploadHandler).Methods("POST")
	r.HandleFunc("/api/files", ListHandler).Methods("GET")
	r.HandleFunc("/api/delete/{filename}", DeleteHandler).Methods("DELETE")
	r.HandleFunc("/api/health", HealthHandler).Methods("GET")

	r.HandleFunc("/api/tree", TreeHandler).Methods("GET")
	r.HandleFunc("/api/file", FileHandler).Methods("GET")

	// thumbnails, metadata, grid
	r.HandleFunc("/api/thumbnail", ThumbnailHandler).Methods("GET")
	r.HandleFunc("/api/metadata", MetadataHandler).Methods("GET")
	r.HandleFunc("/api/grid", GridHandler).Methods("GET")
}
