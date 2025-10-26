package api

import (
	"net/http"

	"localcloud/internal/config"

	"github.com/gorilla/mux"
)

func RegisterRoutes(r *mux.Router, dataDir string) {
	DataDir = dataDir
	config.LoadConfig()

	r.HandleFunc("/upload", UploadHandler).Methods("POST")
	r.HandleFunc("/files", ListHandler).Methods("GET")
	r.HandleFunc("/delete/{filename}", DeleteHandler).Methods("DELETE")
	r.HandleFunc("/health", HealthHandler).Methods("GET")
	r.HandleFunc("/search", SearchHandler).Methods("GET")

	// static info
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("LocalCloud Go API running"))
	}).Methods("GET")
}
