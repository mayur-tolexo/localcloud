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
	"strings"
	"time"

	"localcloud/internal/db"
	"localcloud/internal/storage"

	"github.com/rwcarlsen/goexif/exif"
)

// InitSyncDB ensures the media table exists and migrates missing columns/indexes.
// Call once after db.InitDB()
func InitSyncDB() error {
	// Ensure base table exists (this will not modify existing columns)
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
		backup_at DATETIME
	);
	`)
	if err != nil {
		return err
	}

	// Query existing columns
	rows, err := db.DB.Query(`PRAGMA table_info(media);`)
	if err != nil {
		return err
	}
	defer rows.Close()

	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name string
		var ctype string
		var notnull int
		var dfltValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return err
		}
		existing[name] = true
	}

	// Columns we want to ensure exist and their definitions
	toAdd := map[string]string{
		"retry_count":   "INTEGER DEFAULT 0",
		"exif_datetime": "TEXT",
		"camera_model":  "TEXT",
	}

	for col, def := range toAdd {
		if !existing[col] {
			alter := fmt.Sprintf("ALTER TABLE media ADD COLUMN %s %s;", col, def)
			if _, err := db.DB.Exec(alter); err != nil {
				// Log error and continue attempting other columns
				fmt.Println("migration: failed to add column", col, ":", err)
			} else {
				fmt.Println("migration: added column", col)
			}
		}
	}

	// Ensure useful indexes exist
	if _, err := db.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_media_sha256 ON media(sha256);`); err != nil {
		return err
	}
	if _, err := db.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_media_device ON media(device_id);`); err != nil {
		return err
	}
	if _, err := db.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_media_filename ON media(filename);`); err != nil {
		return err
	}
	if _, err := db.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_media_exif_dt ON media(exif_datetime);`); err != nil {
		return err
	}

	return nil
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

	// extract EXIF for JPEGs
	var exifDate, cameraModel string
	ext := strings.ToLower(filepath.Ext(finalPath))
	if ext == ".jpg" || ext == ".jpeg" {
		if f2, err := os.Open(finalPath); err == nil {
			if x, err := exif.Decode(f2); err == nil {
				if dt, err := x.DateTime(); err == nil {
					exifDate = dt.Format(time.RFC3339)
				}
				if m, err := x.Get(exif.Model); err == nil {
					if s, err := m.StringVal(); err == nil {
						cameraModel = s
					}
				}
			}
			_ = f2.Close()
		}
	}

	// insert into media table including exif fields
	res, err := db.DB.Exec(`
		INSERT INTO media(filename, filepath, sha256, device_id, exif_datetime, camera_model) 
		VALUES(?, ?, ?, ?, ?, ?)`,
		filepath.Base(finalPath), finalPath, sum, deviceID, exifDate, cameraModel)
	if err != nil {
		// log but continue
		fmt.Println("db insert error:", err)
	}
	lastID, _ := res.LastInsertId()

	// enqueue backup job (background worker will copy to backup dir)
	EnqueueBackup(finalPath, lastID)

	// enqueue thumbnail generation if thumbnail worker is running
	EnqueueThumbnail(finalPath)

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
	q := "SELECT id, filename, filepath, sha256, backed_up, backup_path, backup_at, uploaded_at, exif_datetime, camera_model FROM media"
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
			sha        sql.NullString
			backedUp   int
			backupPath sql.NullString
			backupAt   sql.NullString
			uploadedAt string
			exifDT     sql.NullString
			camera     sql.NullString
		)
		_ = rows.Scan(&id, &name, &pathStr, &sha, &backedUp, &backupPath, &backupAt, &uploadedAt, &exifDT, &camera)
		item := map[string]interface{}{
			"id":         id,
			"filename":   name,
			"path":       relAPIPath(pathStr),
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
		out = append(out, item)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": out})
}
