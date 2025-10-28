package db

import (
	"database/sql"
	"fmt"
	"log"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// DB is the exported database handle used elsewhere in the app.
var DB *sql.DB

// InitDB opens/creates the sqlite db at dbPath and ensures schema & indexes.
// It does NOT perform the recursive indexing — call IndexDataDirSync to do that.
func InitDB(dbPath string) {
	if dbPath == "" {
		log.Fatalf("InitDB: dbPath is empty")
	}

	// open DB with WAL for concurrency
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_foreign_keys=1", dbPath)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		log.Fatalf("InitDB: open db failed: %v", err)
	}

	// quick ping
	if err := db.Ping(); err != nil {
		db.Close()
		log.Fatalf("InitDB: ping failed: %v", err)
	}

	DB = db

	// create base table if not exists
	if err := createFilesTable(); err != nil {
		log.Fatalf("InitDB: createFilesTable failed: %v", err)
	}

	// ensure columns are present (migrations)
	if err := ensureColumns(); err != nil {
		log.Fatalf("InitDB: ensureColumns failed: %v", err)
	}

	// create helpful indexes
	if err := ensureIndexes(); err != nil {
		log.Printf("InitDB: ensureIndexes warning: %v", err)
	}
}

// IndexDataDirSync walks dataDir recursively and upserts files into the DB.
// Returns a slice of API-style paths ("/path/from/data/..") for all processed files.
// Skips hidden files/dirs (starting with .) and ".thumbs". Safe to call from main.
func IndexDataDirSync(dataDir string) ([]string, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("IndexDataDirSync: dataDir is empty")
	}
	absData, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(absData)
	if err != nil {
		return nil, fmt.Errorf("IndexDataDirSync: stat dataDir: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("IndexDataDirSync: dataDir is not a directory: %s", absData)
	}

	log.Printf("IndexDataDirSync: indexing recursively under %s", absData)

	// Prepare statements once (concurrency-safe with separate Tx usage)
	insertSQL := `INSERT OR IGNORE INTO files(filename, filepath, mime, uploaded_at) VALUES (?, ?, ?, ?);`
	updateSQL := `UPDATE files SET mime = ?, uploaded_at = ? WHERE filepath = ?;`

	insertStmt, err := DB.Prepare(insertSQL)
	if err != nil {
		return nil, fmt.Errorf("prepare insert: %w", err)
	}
	defer insertStmt.Close()

	updateStmt, err := DB.Prepare(updateSQL)
	if err != nil {
		return nil, fmt.Errorf("prepare update: %w", err)
	}
	defer updateStmt.Close()

	// We'll collect processed API paths here (thread-safe)
	var mu sync.Mutex
	var processed []string

	// WalkDir provides efficient recursive walk
	err = filepath.WalkDir(absData, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// log and continue
			log.Printf("walkdir error %s: %v", path, walkErr)
			return nil
		}
		// Skip directories we don't want to descend
		if d.IsDir() {
			base := d.Name()
			if strings.HasPrefix(base, ".") || base == ".thumbs" {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip files with any hidden component
		relRaw, err := filepath.Rel(absData, path)
		if err != nil {
			return nil
		}
		parts := strings.Split(relRaw, string(os.PathSeparator))
		for _, p := range parts {
			if strings.HasPrefix(p, ".") {
				return nil
			}
		}

		// Skip the DB file itself if located inside dataDir
		if relRaw == "metadata.db" || strings.HasSuffix(path, "metadata.db") {
			return nil
		}

		// At this point it's a normal file to index
		info, err := d.Info()
		if err != nil {
			return nil
		}

		apiPath := "/" + filepath.ToSlash(relRaw)
		name := info.Name()
		ext := strings.ToLower(filepath.Ext(name))
		mt := mime.TypeByExtension(ext)
		if mt == "" {
			mt = "application/octet-stream"
		}
		now := time.Now().UTC().Format(time.RFC3339)

		// Use a transaction for upsert per-file to reduce contention and ensure consistency
		tx, err := DB.Begin()
		if err != nil {
			log.Printf("index tx begin error: %v", err)
			return nil
		}
		// try insert or ignore
		if _, err := tx.Stmt(insertStmt).Exec(name, apiPath, mt, now); err != nil {
			log.Printf("index insert error for %s: %v", apiPath, err)
			_ = tx.Rollback()
			return nil
		}
		// update mime/uploaded_at in case the row existed without them
		if _, err := tx.Stmt(updateStmt).Exec(mt, now, apiPath); err != nil {
			log.Printf("index update error for %s: %v", apiPath, err)
			_ = tx.Rollback()
			return nil
		}
		if err := tx.Commit(); err != nil {
			log.Printf("index tx commit error for %s: %v", apiPath, err)
			return nil
		}

		// append to processed list
		mu.Lock()
		processed = append(processed, apiPath)
		mu.Unlock()

		return nil
	})
	if err != nil {
		return processed, fmt.Errorf("walkdir failed: %w", err)
	}

	log.Printf("IndexDataDirSync: processed %d files", len(processed))
	return processed, nil
}

// createFilesTable ensures minimal files table exists
func createFilesTable() error {
	stmt := `
CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    filename TEXT NOT NULL,
    filepath TEXT NOT NULL UNIQUE,
    mime TEXT,
    uploaded_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    exif_datetime TEXT,
    camera_model TEXT
);
`
	_, err := DB.Exec(stmt)
	return err
}

// ensureColumns adds missing columns (safe)
func ensureColumns() error {
	rows, err := DB.Query("PRAGMA table_info(files);")
	if err != nil {
		return err
	}
	defer rows.Close()

	exists := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		exists[name] = true
	}

	cols := map[string]string{
		"mime":          "TEXT",
		"uploaded_at":   "DATETIME DEFAULT CURRENT_TIMESTAMP",
		"exif_datetime": "TEXT",
		"camera_model":  "TEXT",
	}

	for col, def := range cols {
		if !exists[col] {
			alter := fmt.Sprintf("ALTER TABLE files ADD COLUMN %s %s;", col, def)
			if _, err := DB.Exec(alter); err != nil {
				return fmt.Errorf("adding column %s failed: %w", col, err)
			}
			log.Printf("DB migration: added column %s", col)
		}
	}
	return nil
}

// ensureIndexes creates simple indexes
func ensureIndexes() error {
	stmts := []string{
		"CREATE INDEX IF NOT EXISTS idx_files_filename ON files(filename);",
		"CREATE INDEX IF NOT EXISTS idx_files_uploaded_at ON files(uploaded_at);",
	}
	for _, s := range stmts {
		if _, err := DB.Exec(s); err != nil {
			return err
		}
	}
	return nil
}
