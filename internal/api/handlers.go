package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"localcloud/internal/config"
	"localcloud/internal/db"
	"localcloud/internal/storage"

	"github.com/gorilla/mux"
)

type UploadResponse struct {
	ID       int64  `json:"id"`
	Filename string `json:"filename"`
	Path     string `json:"path"`
}

type FileRecord struct {
	ID       int64  `json:"id"`
	Filename string `json:"filename"`
	Path     string `json:"path"`
	Uploaded string `json:"uploaded_at"`
}

var DataDir string

// helper: notify AI service to index file (async)
func notifyAIIndex(filePath string, fileID int64) {
	go func() {
		payload := map[string]interface{}{
			"file_path": filePath,
			"file_id":   fileID,
		}
		b, _ := json.Marshal(payload)
		client := &http.Client{Timeout: 25 * time.Second}
		aiURL := config.AIService + "/index"
		req, err := http.NewRequest("POST", aiURL, bytes.NewReader(b))
		if err != nil {
			log.Println("AI notify request create err:", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			log.Println("AI notify request err:", err)
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
}

// UploadHandler accepts multipart form file under key "file"
func UploadHandler(w http.ResponseWriter, r *http.Request) {
	// limit to 500 MB per request overall (adjustable)
	r.Body = http.MaxBytesReader(w, r.Body, 500<<20)
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		http.Error(w, "could not parse multipart form: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file field required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Save file to disk
	savedPath, err := storage.SaveFile(DataDir, header.Filename, file)
	if err != nil {
		http.Error(w, "failed to save file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// insert metadata to sqlite
	res, err := db.DB.Exec("INSERT OR IGNORE INTO files(filename, filepath) VALUES(?, ?)", header.Filename, savedPath)
	if err != nil {
		http.Error(w, "db insert error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	lastID, err := res.LastInsertId()
	if err != nil || lastID == 0 {
		// If insert ignored due to unique, fetch existing id
		row := db.DB.QueryRow("SELECT id FROM files WHERE filename = ?", header.Filename)
		_ = row.Scan(&lastID)
	}

	// notify AI microservice asynchronously
	notifyAIIndex(savedPath, lastID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(UploadResponse{ID: lastID, Filename: header.Filename, Path: savedPath})
}

// ListHandler lists files from DB (paginated optional)
func ListHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := db.DB.Query("SELECT id, filename, filepath, uploaded_at FROM files ORDER BY uploaded_at DESC")
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	results := []FileRecord{}
	for rows.Next() {
		var f FileRecord
		var uploadedAt string
		if err := rows.Scan(&f.ID, &f.Filename, &f.Path, &uploadedAt); err != nil {
			continue
		}
		f.Uploaded = uploadedAt
		results = append(results, f)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"files": results})
}

// DeleteHandler deletes file from disk and metadata
func DeleteHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	filename := vars["filename"]
	if filename == "" {
		http.Error(w, "filename required", http.StatusBadRequest)
		return
	}

	// ensure base
	filename = filepath.Base(filename)

	if err := storage.DeleteFile(DataDir, filename); err != nil {
		http.Error(w, "delete failed: "+err.Error(), http.StatusNotFound)
		return
	}

	_, err := db.DB.Exec("DELETE FROM files WHERE filename = ?", filename)
	if err != nil {
		http.Error(w, "db delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("deleted"))
}

// HealthHandler
func HealthHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok"))
}

/*
SearchHandler:
1) Accepts query param q
2) Calls AI service to convert text -> embedding (POST /embed-text)
3) Calls Qdrant /collections/images/points/search with {"vector":[], "limit": 20}
4) Maps results' payload (we expect payload.filename or use point id to lookup sqlite)
5) Returns list of {id, filename, dlna_url}
*/
func SearchHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limitStr := r.URL.Query().Get("limit")
	limit := 12
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			limit = v
		}
	}
	if q == "" {
		http.Error(w, "q parameter required", http.StatusBadRequest)
		return
	}

	vec, err := requestTextEmbedding(q)
	if err != nil {
		http.Error(w, "embedding error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	ids, filenames, err := qdrantSearch(vec, limit)
	if err != nil {
		http.Error(w, "qdrant search error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Map filenames to Gerbera DLNA URLs (Gerbera serves at http://<PI_IP>:49152/media/...)
	dlnaBase := fmt.Sprintf("http://%s:49152/media/", config.GerberaIP)
	results := []map[string]interface{}{}
	for i := range filenames {
		results = append(results, map[string]interface{}{
			"id":       ids[i],
			"filename": filenames[i],
			"dlna_url": dlnaBase + filenames[i],
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"results": results})
}

/* ----- Helpers ----- */

// requestTextEmbedding calls AI_SERVICE_URL/embed-text with {"text": "..."}
// expects {"embedding":[...float...]}
func requestTextEmbedding(text string) ([]float64, error) {
	reqBody := map[string]string{"text": text}
	b, _ := json.Marshal(reqBody)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Post(config.AIService+"/embed-text", "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var parsed struct {
		Embedding []float64 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	return parsed.Embedding, nil
}

// qdrantSearch calls Qdrant search API and returns lists of ids and filenames
func qdrantSearch(vector []float64, limit int) ([]int64, []string, error) {
	type searchReq struct {
		Vector []float64 `json:"vector"`
		Limit  int       `json:"limit"`
	}

	reqBody := searchReq{Vector: vector, Limit: limit}
	b, _ := json.Marshal(reqBody)

	client := &http.Client{Timeout: 20 * time.Second}
	url := fmt.Sprintf("%s/collections/%s/points/search", config.QdrantURL, "images") // collection = images
	resp, err := client.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	// parse response - Qdrant returns "result": [{"id":..., "payload": {...}}]
	var parsed struct {
		Result []struct {
			ID      interface{}            `json:"id"`
			Payload map[string]interface{} `json:"payload"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, nil, err
	}

	var ids []int64
	var filenames []string
	for _, r := range parsed.Result {
		// handle id as number or string
		switch v := r.ID.(type) {
		case float64:
			ids = append(ids, int64(v))
		case string:
			// try parse numeric string
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				ids = append(ids, n)
			} else {
				ids = append(ids, 0)
			}
		default:
			ids = append(ids, 0)
		}
		// payload filename
		name := ""
		if pfn, ok := r.Payload["filename"]; ok {
			if s, ok := pfn.(string); ok {
				name = s
			}
		}
		filenames = append(filenames, name)
	}
	return ids, filenames, nil
}
