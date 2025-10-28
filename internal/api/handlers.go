package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	_ "image/png"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"localcloud/internal/db"
	"localcloud/internal/storage"

	"github.com/disintegration/imaging"
	"github.com/gorilla/mux"
	"github.com/rwcarlsen/goexif/exif"
)

var (
	DataDir        string
	thumbnailQueue chan string
	wg             sync.WaitGroup
)

// ---------------------- helpers ----------------------

func absClean(root, rel string) (string, error) {
	rel = strings.TrimPrefix(rel, "/")
	abs := filepath.Join(root, rel)
	realRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	realPath, err := filepath.Abs(abs)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(realPath, realRoot) {
		return "", fmt.Errorf("path outside of data dir")
	}
	return realPath, nil
}

func relAPIPath(abs string) string {
	rel, _ := filepath.Rel(DataDir, abs)
	return "/" + filepath.ToSlash(rel)
}

func thumbPathFor(abs string) string {
	rel, _ := filepath.Rel(DataDir, abs)
	rel = filepath.ToSlash(rel)
	thumbDir := filepath.Join(DataDir, ".thumbs", filepath.Dir(rel))
	os.MkdirAll(thumbDir, 0755)
	base := filepath.Base(rel)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	return filepath.Join(thumbDir, name+".jpg")
}

// ---------------------- thumbnail generation ----------------------

func generateImageThumbnail(abs, dst string, maxDim int) error {
	img, err := imaging.Open(abs)
	if err != nil {
		return err
	}
	thumb := imaging.Thumbnail(img, maxDim, maxDim, imaging.Lanczos)
	return imaging.Save(thumb, dst, imaging.JPEGQuality(82))
}

func generateVideoThumbnailFFmpeg(abs, dst string, maxDim int) error {
	// Use ffmpeg to extract a frame (requires ffmpeg installed)
	// -ss 2 -> seek 2s
	// output to stdout image and decode
	cmd := exec.Command("ffmpeg", "-ss", "2", "-i", abs, "-vframes", "1", "-vf", fmt.Sprintf("scale='min(%d,iw)':'min(%d,ih)'", maxDim, maxDim), "-f", "image2", "pipe:1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		// fallback: return empty image
		img := imaging.New(maxDim, maxDim, color.Black)
		return imaging.Save(img, dst, imaging.JPEGQuality(70))
	}
	img, _, err := image.Decode(&out)
	if err != nil {
		return err
	}
	thumb := imaging.Thumbnail(img, maxDim, maxDim, imaging.Lanczos)
	return imaging.Save(thumb, dst, imaging.JPEGQuality(82))
}

func generateThumbnail(abs, dst string, maxDim int) error {
	// return early if exists
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(abs))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".bmp", ".tiff", ".gif":
		return generateImageThumbnail(abs, dst, maxDim)
	default:
		// treat as video-ish or unknown: try ffmpeg
		if _, err := exec.LookPath("ffmpeg"); err == nil {
			return generateVideoThumbnailFFmpeg(abs, dst, maxDim)
		}
		// fallback blank image
		img := imaging.New(maxDim, maxDim, color.Black)
		return imaging.Save(img, dst, imaging.JPEGQuality(70))
	}
}

// ---------------------- thumbnail worker ----------------------

func StartThumbnailWorker(concurrency int) {
	if thumbnailQueue != nil {
		return
	}
	thumbnailQueue = make(chan string, 1024)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range thumbnailQueue {
				dst := thumbPathFor(p)
				if err := generateThumbnail(p, dst, 480); err != nil {
					log.Println("thumb generate err:", err)
				}
			}
		}()
	}
}

func EnqueueThumbnail(abs string) {
	if thumbnailQueue == nil {
		return
	}
	select {
	case thumbnailQueue <- abs:
	default:
		// queue full - drop (best-effort)
	}
}

// ---------------------- API Handlers ----------------------

// UploadHandler accepts multipart form file under key "file"
func UploadHandler(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<30) // 2GB
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "could not parse multipart form: "+err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file field required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	savedPath, err := storage.SaveFile(DataDir, header.Filename, file)
	if err != nil {
		http.Error(w, "failed to save file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	res, err := db.DB.Exec("INSERT OR IGNORE INTO files(filename, filepath) VALUES(?, ?)", header.Filename, savedPath)
	if err != nil {
		http.Error(w, "db insert error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	lastID, err := res.LastInsertId()
	if err != nil || lastID == 0 {
		row := db.DB.QueryRow("SELECT id FROM files WHERE filename = ?", header.Filename)
		_ = row.Scan(&lastID)
	}

	// enqueue thumbnail generation
	EnqueueThumbnail(savedPath)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":       lastID,
		"filename": header.Filename,
		"path":     savedPath,
	})
}

// ListHandler lists files from DB (metadata)
func ListHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := db.DB.Query("SELECT id, filename, filepath, uploaded_at FROM files ORDER BY uploaded_at DESC")
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	results := []map[string]interface{}{}
	for rows.Next() {
		var id int64
		var name, pathStr, uploaded string
		if err := rows.Scan(&id, &name, &pathStr, &uploaded); err != nil {
			continue
		}
		results = append(results, map[string]interface{}{
			"id":         id,
			"filename":   name,
			"filepath":   pathStr,
			"uploadedAt": uploaded,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"files": results})
}

// DeleteHandler deletes file from disk and metadata
func DeleteHandler(w http.ResponseWriter, r *http.Request) {
	vars := pathVarsFromRequest(r)
	filename := vars["filename"]
	if filename == "" {
		http.Error(w, "filename required", http.StatusBadRequest)
		return
	}
	if err := storage.DeleteFile(DataDir, filename); err != nil {
		http.Error(w, "delete failed: "+err.Error(), http.StatusNotFound)
		return
	}
	if _, err := db.DB.Exec("DELETE FROM files WHERE filename = ?", filename); err != nil {
		http.Error(w, "db delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("deleted"))
}

func pathVarsFromRequest(r *http.Request) map[string]string {
	// fallback simple parser for mux vars (we use mux in routes)
	vars := map[string]string{}
	// try mux
	if v := muxVars(r); v != nil {
		return v
	}
	return vars
}
func muxVars(r *http.Request) map[string]string {
	// import here to avoid package cycle at top — but we used mux in main; easiest is:
	// Actually use github.com/gorilla/mux directly:
	return mux.Vars(r)
}

// HealthHandler
func HealthHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok"))
}

// TreeHandler lists files/dirs under given path
type TreeItem struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"` // file | dir
	Size     int64  `json:"size"`
	Mime     string `json:"mime"`
	Modified string `json:"modified"`
}

func TreeHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("path")
	if q == "" {
		q = "/"
	}
	abs, err := absClean(DataDir, q)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		http.Error(w, "read dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	items := []TreeItem{}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		relPath, _ := filepath.Rel(DataDir, filepath.Join(abs, e.Name()))
		apiPath := "/" + filepath.ToSlash(relPath)
		item := TreeItem{
			Name:     e.Name(),
			Path:     apiPath,
			Modified: info.ModTime().Format(time.RFC3339),
		}
		if e.IsDir() {
			item.Type = "dir"
		} else {
			item.Type = "file"
			item.Size = info.Size()
			mt := mime.TypeByExtension(strings.ToLower(filepath.Ext(e.Name())))
			if mt == "" {
				mt = "application/octet-stream"
			}
			item.Mime = mt
		}
		items = append(items, item)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"items": items})
}

// FileHandler streams file with Range support
func FileHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("path")
	if q == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	abs, err := absClean(DataDir, q)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		http.Error(w, "open error: "+err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		http.Error(w, "stat error", http.StatusInternalServerError)
		return
	}
	size := fi.Size()
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(fi.Name())))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Accept-Ranges", "bytes")

	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
		return
	}
	start, end, err := parseRange(rangeHeader, size)
	if err != nil {
		http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		http.Error(w, "seek error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
	w.WriteHeader(http.StatusPartialContent)

	buf := make([]byte, 32*1024)
	left := end - start + 1
	for left > 0 {
		toRead := int64(len(buf))
		if left < toRead {
			toRead = left
		}
		n, err := f.Read(buf[:toRead])
		if n > 0 {
			_, _ = w.Write(buf[:n])
			left -= int64(n)
		}
		if err != nil {
			break
		}
	}
}

func parseRange(rangeHeader string, size int64) (int64, int64, error) {
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return 0, 0, fmt.Errorf("unsupported range")
	}
	parts := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid range")
	}
	var start, end int64
	var err error
	if parts[0] == "" {
		sl, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return 0, 0, err
		}
		if sl > size {
			sl = size
		}
		start = size - sl
		end = size - 1
		return start, end, nil
	}
	start, err = strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	if parts[1] == "" {
		end = size - 1
	} else {
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return 0, 0, err
		}
	}
	if start > end || end >= size {
		return 0, 0, fmt.Errorf("invalid range bounds")
	}
	return start, end, nil
}

// ---------------- Thumbnail endpoint ----------------

// ThumbnailHandler: GET /api/thumbnail?path=/some.jpg&w=320
func ThumbnailHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("path")
	if q == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	wStr := r.URL.Query().Get("w")
	width := 320
	if wStr != "" {
		if v, err := strconv.Atoi(wStr); err == nil && v > 0 && v <= 2000 {
			width = v
		}
	}
	abs, err := absClean(DataDir, q)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	dst := thumbPathFor(abs)
	// ensure generation (best-effort)
	if err := generateThumbnail(abs, dst, width); err != nil {
		// log, but continue to serve placeholder if exists
		log.Println("thumb gen err:", err)
	}
	// if dst exists serve, else 404
	if _, err := os.Stat(dst); err != nil {
		http.Error(w, "no thumbnail", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, dst)
}

// ---------------- Metadata endpoint ----------------

// MetadataHandler: GET /api/metadata?path=/some.jpg
func MetadataHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("path")
	if q == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	abs, err := absClean(DataDir, q)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	fi, err := os.Stat(abs)
	if err != nil {
		http.Error(w, "stat error", http.StatusNotFound)
		return
	}
	meta := map[string]interface{}{
		"name":     fi.Name(),
		"size":     fi.Size(),
		"modified": fi.ModTime().Format(time.RFC3339),
		"path":     q,
	}
	ext := strings.ToLower(filepath.Ext(abs))
	if ext == ".jpg" || ext == ".jpeg" {
		if f, err := os.Open(abs); err == nil {
			if x, err := exif.Decode(f); err == nil {
				if dt, err := x.DateTime(); err == nil {
					meta["exif_datetime"] = dt.Format(time.RFC3339)
				}
				if m, err := x.Get(exif.Model); err == nil {
					if s, err := m.StringVal(); err == nil {
						meta["camera_model"] = s
					}
				}
			}
			f.Close()
		}
	} else if strings.HasPrefix(mime.TypeByExtension(ext), "video/") {
		// ffprobe for duration
		if _, err := exec.LookPath("ffprobe"); err == nil {
			cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "format=duration", "-of", "default=nk=1:nw=1", abs)
			out, _ := cmd.Output()
			if len(out) > 0 {
				if dur, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil {
					meta["duration_seconds"] = dur
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(meta)
}

// ---------------- Grid endpoint ----------------

// GridHandler: GET /api/grid?path=/&offset=0&limit=50
func GridHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("path")
	if q == "" {
		q = "/"
	}
	abs, err := absClean(DataDir, q)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 60
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		http.Error(w, "read dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	items := []map[string]interface{}{}
	total := len(entries)
	for i := offset; i < total && len(items) < limit; i++ {
		e := entries[i]
		info, _ := e.Info()
		relPath, _ := filepath.Rel(DataDir, filepath.Join(abs, e.Name()))
		apiPath := "/" + filepath.ToSlash(relPath)
		item := map[string]interface{}{
			"name":     e.Name(),
			"path":     apiPath,
			"modified": info.ModTime().Format(time.RFC3339),
			"size":     info.Size(),
		}
		if e.IsDir() {
			item["type"] = "dir"
		} else {
			item["type"] = "file"
			mt := mime.TypeByExtension(strings.ToLower(filepath.Ext(e.Name())))
			if mt == "" {
				mt = "application/octet-stream"
			}
			item["mime"] = mt
			item["thumb"] = "/api/thumbnail?path=" + url.QueryEscape(apiPath) + "&w=360"
		}
		items = append(items, item)
	}
	resp := map[string]interface{}{
		"items":  items,
		"offset": offset,
		"limit":  limit,
		"total":  total,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
