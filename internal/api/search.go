package api

import (
	"database/sql"
	"encoding/json"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"localcloud/internal/db"
)

// SearchHandler is a robust LIKE-based search that always returns JSON.
// GET /api/search?query=pan&limit=100&offset=0
func SearchHandler(w http.ResponseWriter, r *http.Request) {
	// ensure we always return JSON
	w.Header().Set("Content-Type", "application/json")

	q := strings.TrimSpace(r.URL.Query().Get("query"))
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	// if empty query -> return recent items (files ordered by uploaded_at desc)
	if q == "" {
		rows, err := db.DB.Query(`SELECT id, filename, filepath, mime, uploaded_at, exif_datetime, camera_model
			FROM files ORDER BY uploaded_at DESC LIMIT ? OFFSET ?`, limit, offset)
		if err != nil {
			log.Printf("SearchHandler recent db query error: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "internal db error"})
			return
		}
		defer rows.Close()
		items := scanMediaRows(rows)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": items, "offset": offset, "limit": limit})
		return
	}

	// Build LIKE pattern
	pat := "%" + q + "%"

	// Parameterized query searching filename, camera_model, filepath (case-insensitive)
	qry := `
	SELECT id, filename, filepath, mime, uploaded_at, exif_datetime, camera_model
	FROM files
	WHERE LOWER(filename) LIKE LOWER(?) OR LOWER(camera_model) LIKE LOWER(?) OR LOWER(filepath) LIKE LOWER(?)
	ORDER BY uploaded_at DESC
	LIMIT ? OFFSET ?;
	`

	rows, err := db.DB.Query(qry, pat, pat, pat, limit, offset)
	if err != nil {
		log.Printf("SearchHandler db query error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "internal db error"})
		return
	}
	defer rows.Close()

	items := scanMediaRows(rows)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": items, "offset": offset, "limit": limit})
}

// scanMediaRows converts sql.Rows -> []map[string]interface{} with fields expected by UI
func scanMediaRows(rows *sql.Rows) []map[string]interface{} {
	out := []map[string]interface{}{}
	for rows.Next() {
		var (
			id        int64
			filename  string
			filepathS string
			mimeS     sql.NullString
			uploaded  sql.NullString
			exifDT    sql.NullString
			camera    sql.NullString
		)
		if err := rows.Scan(&id, &filename, &filepathS, &mimeS, &uploaded, &exifDT, &camera); err != nil {
			// log and continue
			log.Printf("scanMediaRows: row scan error: %v", err)
			continue
		}
		itemPath := relAPIPath(filepathS)
		mt := mimeS.String
		if mt == "" {
			ext := strings.ToLower(filepath.Ext(filename))
			mt = mime.TypeByExtension(ext)
			if mt == "" {
				mt = "application/octet-stream"
			}
		}
		item := map[string]interface{}{
			"id":         id,
			"name":       filename,
			"path":       itemPath,
			"mime":       mt,
			"modified":   uploaded.String,
			"uploadedAt": uploaded.String,
			"thumb":      "/api/thumbnail?path=" + url.QueryEscape(itemPath) + "&w=360",
			"type":       "file",
			"exif": map[string]interface{}{
				"datetime":    exifDT.String,
				"cameraModel": camera.String,
			},
		}
		out = append(out, item)
	}
	return out
}
