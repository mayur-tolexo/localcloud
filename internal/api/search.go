package api

import (
	"database/sql"
	"encoding/json"
	"localcloud/internal/db"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// SearchHandler - GET /api/search?query=...&limit=50&offset=0&regex=1
// - query: text or regex depending on regex param
// - regex=1 : server will perform regex filter (safe bounded scan)
// - offset/limit supported for pagination (limit capped)
func SearchHandler(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("query"))
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")
	regexFlag := r.URL.Query().Get("regex")

	limit := 100
	if limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 && v <= 1000 {
			limit = v
		}
	}
	offset := 0
	if offsetStr != "" {
		if v, err := strconv.Atoi(offsetStr); err == nil && v >= 0 {
			offset = v
		}
	}

	// If regex mode requested, do a bounded scan (safety: limit max results scanned)
	if regexFlag == "1" {
		handleRegexSearch(w, q, offset, limit)
		return
	}

	// Normal LIKE search (fast via sqlite indexes)
	if q == "" {
		rows, err := db.DB.Query("SELECT id, filename, filepath, sha256, backed_up, backup_path, backup_at, uploaded_at, exif_datetime, camera_model FROM media ORDER BY uploaded_at DESC LIMIT ? OFFSET ?", limit, offset)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := scanMediaRows(rows)
		respondJSON(w, map[string]interface{}{"items": out, "offset": offset, "limit": limit})
		return
	}

	like := "%" + q + "%"
	rows, err := db.DB.Query(`
		SELECT id, filename, filepath, sha256, backed_up, backup_path, backup_at, uploaded_at, exif_datetime, camera_model
		FROM media
		WHERE filename LIKE ? OR exif_datetime LIKE ? OR camera_model LIKE ?
		ORDER BY uploaded_at DESC
		LIMIT ? OFFSET ?`, like, like, like, limit, offset)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := scanMediaRows(rows)
	respondJSON(w, map[string]interface{}{"items": out, "offset": offset, "limit": limit})
}

// handleRegexSearch scans a bounded set of rows and filters them using provided regex
func handleRegexSearch(w http.ResponseWriter, pattern string, offset, limit int) {
	// Secure: empty pattern -> recent items only
	if pattern == "" {
		rows, err := db.DB.Query("SELECT id, filename, filepath, sha256, backed_up, backup_path, backup_at, uploaded_at, exif_datetime, camera_model FROM media ORDER BY uploaded_at DESC LIMIT ? OFFSET ?", limit, offset)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := scanMediaRows(rows)
		respondJSON(w, map[string]interface{}{"items": out, "offset": offset, "limit": limit})
		return
	}

	// Compile regex safely (length limit to avoid catastrophic regex)
	if len(pattern) > 200 {
		http.Error(w, "regex too long", http.StatusBadRequest)
		return
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		http.Error(w, "invalid regex: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Fetch a bounded number of recent rows to filter (safety cap)
	const scanCap = 5000
	rows, err := db.DB.Query("SELECT id, filename, filepath, sha256, backed_up, backup_path, backup_at, uploaded_at, exif_datetime, camera_model FROM media ORDER BY uploaded_at DESC LIMIT ?", scanCap)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	matches := []map[string]interface{}{}
	count := 0
	for rows.Next() {
		var (
			id         int64
			filename   string
			filepathS  string
			sha        sql.NullString
			backedUp   int
			backupPath sql.NullString
			backupAt   sql.NullString
			uploadedAt string
			exifDT     sql.NullString
			camera     sql.NullString
		)
		_ = rows.Scan(&id, &filename, &filepathS, &sha, &backedUp, &backupPath, &backupAt, &uploadedAt, &exifDT, &camera)

		// check against filename, exif datetime, camera model
		if re.MatchString(filename) || re.MatchString(exifDT.String) || re.MatchString(camera.String) {
			itemPath := relAPIPath(filepathS)
			item := map[string]interface{}{
				"id":         id,
				"name":       filename,
				"path":       itemPath,
				"sha256":     sha.String,
				"backed_up":  backedUp == 1,
				"backupPath": backupPath.String,
				"backupAt":   backupAt.String,
				"uploadedAt": uploadedAt,
				"exif": map[string]interface{}{
					"datetime":    exifDT.String,
					"cameraModel": camera.String,
				},
			}
			ext := strings.ToLower(filepath.Ext(filename))
			mt := mime.TypeByExtension(ext)
			if mt == "" {
				mt = "application/octet-stream"
			}
			item["mime"] = mt
			item["thumb"] = "/api/thumbnail?path=" + url.QueryEscape(itemPath) + "&w=360"
			matches = append(matches, item)
			count++
			// apply pagination offset/limit on results
			if count >= offset+limit {
				break
			}
		}
	}

	// apply offset/limit to matches slice
	start := 0
	if offset > 0 {
		if offset >= len(matches) {
			start = len(matches)
		} else {
			start = offset
		}
	}
	end := start + limit
	if end > len(matches) {
		end = len(matches)
	}
	respItems := matches[start:end]
	respondJSON(w, map[string]interface{}{"items": respItems, "offset": offset, "limit": limit, "scanned": scanCap})
}

// scanMediaRows converts SQL rows into the API response item list
func scanMediaRows(rows *sql.Rows) []map[string]interface{} {
	out := []map[string]interface{}{}
	for rows.Next() {
		var (
			id         int64
			filename   string
			filepathS  string
			sha        sql.NullString
			backedUp   int
			backupPath sql.NullString
			backupAt   sql.NullString
			uploadedAt string
			exifDT     sql.NullString
			camera     sql.NullString
		)
		// Note: scanning in same order as SELECT
		_ = rows.Scan(&id, &filename, &filepathS, &sha, &backedUp, &backupPath, &backupAt, &uploadedAt, &exifDT, &camera)

		itemPath := relAPIPath(filepathS)
		item := map[string]interface{}{
			"id":         id,
			"name":       filename,
			"path":       itemPath,
			"sha256":     sha.String,
			"backed_up":  backedUp == 1,
			"backupPath": backupPath.String,
			"backupAt":   backupAt.String,
			"uploadedAt": uploadedAt,
			"exif": map[string]interface{}{
				"datetime":    exifDT.String,
				"cameraModel": camera.String,
			},
		}
		ext := strings.ToLower(filepath.Ext(filename))
		mt := mime.TypeByExtension(ext)
		if mt == "" {
			mt = "application/octet-stream"
		}
		item["mime"] = mt
		item["thumb"] = "/api/thumbnail?path=" + url.QueryEscape(itemPath) + "&w=360"
		out = append(out, item)
	}
	return out
}

// respondJSON writes JSON response with appropriate header
func respondJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
