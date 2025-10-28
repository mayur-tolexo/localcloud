package api

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// DownloadFileHandler serves a file as a download with original filename.
// GET /api/download?path=/some/file.jpg
func DownloadFileHandler(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if fi.IsDir() {
		http.Error(w, "path is a directory, use /api/download-zip", http.StatusBadRequest)
		return
	}

	// open file
	f, err := os.Open(abs)
	if err != nil {
		http.Error(w, "open error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	// set headers for download
	name := filepath.Base(abs)
	w.Header().Set("Content-Type", "application/octet-stream")
	// Use RFC5987 style filename*? But simple quoted filename ok for common cases
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", escapeQuotes(name)))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
	// stream file (do not use ServeContent since we want forced download)
	if _, err := io.Copy(w, f); err != nil {
		// if client disconnects, copying may fail - log and return
		log.Printf("download copy error: %v", err)
	}
}

// DownloadZipHandler streams a zip archive of the directory at `path` (recursive).
// GET /api/download-zip?path=/some/dir
func DownloadZipHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("path")
	if q == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}
	absRoot, err := absClean(DataDir, q)
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	// if path is file, zip single file with parent name fallback
	info, err := os.Stat(absRoot)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// determine zip filename
	var zipName string
	if info.IsDir() {
		zipName = filepath.Base(absRoot)
		if zipName == "." || zipName == string(os.PathSeparator) || zipName == "" {
			zipName = "localcloud"
		}
	} else {
		zipName = strings.TrimSuffix(filepath.Base(absRoot), filepath.Ext(absRoot))
	}
	zipName = zipName + ".zip"

	// set headers (streaming)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", escapeQuotes(zipName)))
	// do not set Content-Length

	// stream with io.Pipe and write zip in goroutine
	pr, pw := io.Pipe()
	zipWriter := zip.NewWriter(pw)

	go func() {
		// ensure writer cleanup on any error
		defer func() {
			_ = zipWriter.Close()
			_ = pw.Close()
		}()

		// If the requested path is a file, add single entry
		if !info.IsDir() {
			if err := addFileToZip(zipWriter, absRoot, filepath.Base(absRoot)); err != nil {
				log.Printf("zip add file error: %v", err)
				_ = pw.CloseWithError(err)
				return
			}
			return
		}

		// Walk directory recursively and add files.
		// Use filepath.WalkDir to stream files.
		err := filepath.Walk(absRoot, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				// skip unreadable file/dir but continue
				log.Printf("walk error %s: %v", path, err)
				return nil
			}
			// skip directories (we only add files; directories implied by file paths)
			if fi.IsDir() {
				return nil
			}
			// relative path inside ZIP
			rel, err := filepath.Rel(absRoot, path)
			if err != nil {
				return nil
			}
			// use forward slashes for zip
			rel = filepath.ToSlash(rel)
			// optionally skip hidden files (dotfiles) — keep consistent with your ignore rules
			if shouldIgnoreFile(fi.Name()) {
				return nil
			}
			// add file
			if err := addFileToZip(zipWriter, path, rel); err != nil {
				log.Printf("zip add file error: %v", err)
				// return err to abort zip generation
				return err
			}
			return nil
		})
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
	}()

	// copy the pipe reader to response writer
	_, err = io.Copy(w, pr)
	if err != nil {
		// client disconnected or other copy error - log
		log.Printf("zip stream copy error: %v", err)
	}
}

// addFileToZip writes a file at absPath into zipWriter with entry name zipPath
func addFileToZip(zipWriter *zip.Writer, absPath, zipPath string) error {
	// Open file
	f, err := os.Open(absPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// create header
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(fi)
	if err != nil {
		return err
	}
	// ensure correct name (relative path inside zip)
	header.Name = zipPath
	// use deflate compression for files (default)
	header.Method = zip.Deflate

	w, err := zipWriter.CreateHeader(header)
	if err != nil {
		return err
	}
	// copy file contents into zip entry
	_, err = io.Copy(w, f)
	return err
}

// escapeQuotes escapes " characters in filenames for Content-Disposition header
func escapeQuotes(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}
