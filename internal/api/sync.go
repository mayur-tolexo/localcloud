package api

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"localcloud/internal/db"
	"localcloud/internal/storage"
)

// InitSyncDB ensures the media table exists. Call once after db.InitDB()
func InitSyncDB() error {
	_, err := db.DB.Exec(`
	CREATE TABLE IF NOT EXISTS media (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		filename TEXT NOT NULL,
		filepath TEXT NOT NULL,
		sha256 TEXT,
		device_id TEXT,
		uploaded_at DATETIME DEFAULT (datetime('now')),
		backed_up INTEGER DEFAULT 0,
		backup_path TEXT,
		backup_at DATETIME,
		retry_count INTEGER DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_media_sha256 ON media(sha256);
	CREATE INDEX IF NOT EXISTS idx_media_device ON media(device_id);
	`)
	return err
}

// SyncUploadHandler handles device uploads (multipart form-data, key "file")
// Optional form fields: device_id
func SyncUploadHandler(w http.ResponseWriter, r *http.Request) {
	// limit to reasonable size, e.g. 3GB
	r.Body = http.MaxBytesReader(w, r.Body, 3<<30)
	if err := r.ParseMultipartForm(256 << 20); err != nil {
		http.Error(w, "parse multipart: "+err.Error(), http.StatusBadRequest)
		return
	}
	f, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer f.Close()

	deviceID := r.FormValue("device_id")
	if deviceID == "" {
		deviceID = "unknown"
	}

	// create device folder under DataDir/devices/<deviceID>/
	deviceDir := filepath.Join(DataDir, "devices", deviceID)
	if err := os.MkdirAll(deviceDir, 0755); err != nil {
		http.Error(w, "mkdir failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// write to temp file while computing SHA256
	tmpName := fmt.Sprintf(".upload_%d_%s", time.Now().UnixNano(), header.Filename)
	tmpPath := filepath.Join(deviceDir, tmpName)
	out, err := os.Create(tmpPath)
	if err != nil {
		http.Error(w, "create tmp: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h := sha256.New()
	mw := io.MultiWriter(out, h)
	if _, err := io.Copy(mw, f); err != nil {
		out.Close()
		os.Remove(tmpPath)
		http.Error(w, "save error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out.Close()
	sum := hex.EncodeToString(h.Sum(nil))

	// check duplicate by SHA256
	var existingPath string
	err = db.DB.QueryRow("SELECT filepath FROM media WHERE sha256 = ? LIMIT 1", sum).Scan(&existingPath)
	if err == nil && existingPath != "" {
		// duplicate found -> remove tmp and return skipped
		_ = os.Remove(tmpPath)
		resp := map[string]interface{}{"status": "ok", "skipped": true, "path": existingPath}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	// choose final path (avoid overwrite by appending suffix)
	finalName := header.Filename
	finalPath := filepath.Join(deviceDir, finalName)
	for i := 1; ; i++ {
		if _, err := os.Stat(finalPath); os.IsNotExist(err) {
			break
		}
		ext := filepath.Ext(finalName)
		nameOnly := finalName[:len(finalName)-len(ext)]
		finalPath = filepath.Join(deviceDir, fmt.Sprintf("%s_%d%s", nameOnly, i, ext))
	}

	// move temp to final
	if err := os.Rename(tmpPath, finalPath); err != nil {
		// fallback to copy if rename fails
		if err2 := storage.CopyFile(tmpPath, finalPath); err2 != nil {
			os.Remove(tmpPath)
			http.Error(w, "move error: "+err.Error()+" / "+err2.Error(), http.StatusInternalServerError)
			return
		}
		_ = os.Remove(tmpPath)
	}

	// insert into media table
	res, err := db.DB.Exec("INSERT INTO media(filename, filepath, sha256, device_id) VALUES(?, ?, ?, ?)",
		filepath.Base(finalPath), finalPath, sum, deviceID)
	if err != nil {
		// log but continue
		fmt.Println("db insert error:", err)
	}
	lastID, _ := res.LastInsertId()

	// enqueue backup job (background worker will copy to backup dir)
	EnqueueBackup(finalPath, lastID)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "ok",
		"skipped": false,
		"path":    relAPIPath(finalPath),
		"id":      lastID,
	})
}

// SyncStatusHandler returns recent media for device (or global if device_id not supplied)
func SyncStatusHandler(w http.ResponseWriter, r *http.Request) {
	device := r.URL.Query().Get("device_id")
	q := "SELECT id, filename, filepath, sha256, backed_up, backup_path, backup_at, uploaded_at FROM media"
	var rows *sql.Rows
	var err error
	if device != "" {
		rows, err = db.DB.Query(q+" WHERE device_id = ? ORDER BY uploaded_at DESC LIMIT 200", device)
	} else {
		rows, err = db.DB.Query(q + " ORDER BY uploaded_at DESC LIMIT 200")
	}
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []map[string]interface{}{}
	for rows.Next() {
		var (
			id         int64
			name       string
			pathStr    string
			sha        string
			backedUp   int
			backupPath sql.NullString
			backupAt   sql.NullString
			uploadedAt string
		)
		_ = rows.Scan(&id, &name, &pathStr, &sha, &backedUp, &backupPath, &backupAt, &uploadedAt)
		item := map[string]interface{}{
			"id":         id,
			"filename":   name,
			"path":       relAPIPath(pathStr),
			"sha256":     sha,
			"backed_up":  backedUp == 1,
			"backupPath": backupPath.String,
			"backupAt":   backupAt.String,
			"uploadedAt": uploadedAt,
		}
		out = append(out, item)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": out})
}
