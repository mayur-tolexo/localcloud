package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"localcloud/internal/db"
)

// InitSearchIndex creates the FTS table and triggers only if FTS5 is available.
// If FTS5 is not available, it returns nil but sets a flag so SearchHandler uses
// the LIKE-based fallback.
var hasFTS5 bool = false

func InitSearchIndex() error {
	// Check if FTS5 is enabled in this SQLite build
	var has int
	row := db.DB.QueryRow("SELECT 1 WHERE sqlite_compileoption_used('ENABLE_FTS5')")
	if err := row.Scan(&has); err == nil && has == 1 {
		hasFTS5 = true
	} else {
		// Some sqlite builds don't expose compile options; try a safer probe:
		_, err := db.DB.Exec("CREATE VIRTUAL TABLE IF NOT EXISTS __fts_test USING fts5(x);")
		if err == nil {
			// remove the test table and mark hasFTS5 true
			_, _ = db.DB.Exec("DROP TABLE IF EXISTS __fts_test;")
			hasFTS5 = true
		} else {
			hasFTS5 = false
		}
	}

	if !hasFTS5 {
		// create simple indexes to speed up LIKE queries (best-effort)
		_, _ = db.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_files_filename ON files(filename);`)
		_, _ = db.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_files_uploaded_at ON files(uploaded_at);`)
		_, _ = db.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_files_mime ON files(mime);`)
		return fmt.Errorf("FTS5 not available; using LIKE-based fallback")
	}

	// Create FTS5 virtual table and populate it
	if _, err := db.DB.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS media_fts USING fts5(
			filename, exif_datetime, camera_model, path, mime,
			content=''
		);
	`); err != nil {
		return fmt.Errorf("create fts table: %w", err)
	}

	// Populate FTS table (only missing entries)
	if _, err := db.DB.Exec(`
	INSERT INTO media_fts(rowid, filename, exif_datetime, camera_model, path, mime)
	SELECT id, filename, exif_datetime, camera_model, filepath, mime FROM files
	WHERE id NOT IN (SELECT rowid FROM media_fts);
	`); err != nil {
		// not fatal — still continue
	}

	// Create triggers to keep FTS in sync
	_, _ = db.DB.Exec(`
	CREATE TRIGGER IF NOT EXISTS files_ai AFTER INSERT ON files BEGIN
	  INSERT INTO media_fts(rowid, filename, exif_datetime, camera_model, path, mime)
	  VALUES (new.id, new.filename, new.exif_datetime, new.camera_model, new.filepath, new.mime);
	END;
	CREATE TRIGGER IF NOT EXISTS files_ad AFTER DELETE ON files BEGIN
	  DELETE FROM media_fts WHERE rowid = old.id;
	END;
	CREATE TRIGGER IF NOT EXISTS files_au AFTER UPDATE ON files BEGIN
	  INSERT INTO media_fts(media_fts, rowid, filename, exif_datetime, camera_model, path, mime)
	    VALUES('delete', old.id, old.filename, old.exif_datetime, old.camera_model, old.filepath, old.mime);
	  INSERT INTO media_fts(rowid, filename, exif_datetime, camera_model, path, mime)
	    VALUES (new.id, new.filename, new.exif_datetime, new.camera_model, new.filepath, new.mime);
	END;
	`)

	return nil
}

// Helper: split query into tokens and return tokens longer than 1 char
func tokenize(q string) []string {
	parts := strings.FieldsFunc(q, func(r rune) bool {
		return r == ' ' || r == ',' || r == ';' || r == ':' || r == '-' || r == '_' || r == '.'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) < 2 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// scanMediaRows converts SQL rows (expected columns) into API response items
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
		_ = rows.Scan(&id, &filename, &filepathS, &mimeS, &uploaded, &exifDT, &camera)
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
			"uploadedAt": uploaded.String,
			"exif": map[string]interface{}{
				"datetime":    exifDT.String,
				"cameraModel": camera.String,
			},
			"thumb": "/api/thumbnail?path=" + url.QueryEscape(itemPath) + "&w=360",
		}
		out = append(out, item)
	}
	return out
}

// SearchHandler: improved search that uses FTS when available, otherwise a tokenized LIKE search.
// GET /api/search?query=...&limit=50&offset=0&mime=image/jpeg&date_from=YYYY-MM-DD&date_to=YYYY-MM-DD
func SearchHandler(w http.ResponseWriter, r *http.Request) {
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
	mimeFilter := strings.TrimSpace(r.URL.Query().Get("mime"))
	dateFrom := strings.TrimSpace(r.URL.Query().Get("date_from"))
	dateTo := strings.TrimSpace(r.URL.Query().Get("date_to"))

	// Empty query -> recent items (with optional filters)
	if q == "" {
		args := []interface{}{}
		where := []string{}
		if mimeFilter != "" {
			where = append(where, "mime = ?")
			args = append(args, mimeFilter)
		}
		if dateFrom != "" {
			if t, err := time.Parse("2006-01-02", dateFrom); err == nil {
				where = append(where, "date(uploaded_at) >= date(?)")
				args = append(args, t.Format("2006-01-02"))
			}
		}
		if dateTo != "" {
			if t, err := time.Parse("2006-01-02", dateTo); err == nil {
				where = append(where, "date(uploaded_at) <= date(?)")
				args = append(args, t.Format("2006-01-02"))
			}
		}
		qry := "SELECT id, filename, filepath, mime, uploaded_at, exif_datetime, camera_model FROM files"
		if len(where) > 0 {
			qry += " WHERE " + strings.Join(where, " AND ")
		}
		qry += " ORDER BY uploaded_at DESC LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
		rows, err := db.DB.Query(qry, args...)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		items := scanMediaRows(rows)
		respondJSON(w, map[string]interface{}{"items": items, "offset": offset, "limit": limit})
		return
	}

	// If FTS is available, use it
	if hasFTS5 {
		match := buildFTSMatch(q)
		if match != "" {
			args := []interface{}{}
			where := []string{"media_fts MATCH ?"}
			args = append(args, match)
			if mimeFilter != "" {
				where = append(where, "files.mime = ?")
				args = append(args, mimeFilter)
			}
			if dateFrom != "" {
				if t, err := time.Parse("2006-01-02", dateFrom); err == nil {
					where = append(where, "date(files.uploaded_at) >= date(?)")
					args = append(args, t.Format("2006-01-02"))
				}
			}
			if dateTo != "" {
				if t, err := time.Parse("2006-01-02", dateTo); err == nil {
					where = append(where, "date(files.uploaded_at) <= date(?)")
					args = append(args, t.Format("2006-01-02"))
				}
			}
			qry := "SELECT files.id, files.filename, files.filepath, files.mime, files.uploaded_at, files.exif_datetime, files.camera_model " +
				"FROM media_fts JOIN files ON files.id = media_fts.rowid WHERE " + strings.Join(where, " AND ") +
				" ORDER BY files.uploaded_at DESC LIMIT ? OFFSET ?"
			args = append(args, limit, offset)
			rows, err := db.DB.Query(qry, args...)
			if err == nil {
				defer rows.Close()
				items := scanMediaRows(rows)
				respondJSON(w, map[string]interface{}{"items": items, "offset": offset, "limit": limit, "source": "fts"})
				return
			}
			// if FTS query fails fallthrough to LIKE
		}
	}

	// Fallback: build tokenized LIKE query with simple ranking
	toks := tokenize(q)
	if len(toks) == 0 {
		// fallback exact like
		like := "%" + q + "%"
		args := []interface{}{like, like, like}
		where := "WHERE (filename LIKE ? OR exif_datetime LIKE ? OR camera_model LIKE ?)"
		if mimeFilter != "" {
			where += " AND mime = ?"
			args = append(args, mimeFilter)
		}
		if dateFrom != "" {
			if t, err := time.Parse("2006-01-02", dateFrom); err == nil {
				where += " AND date(uploaded_at) >= date(?)"
				args = append(args, t.Format("2006-01-02"))
			}
		}
		if dateTo != "" {
			if t, err := time.Parse("2006-01-02", dateTo); err == nil {
				where += " AND date(uploaded_at) <= date(?)"
				args = append(args, t.Format("2006-01-02"))
			}
		}
		qry := "SELECT id, filename, filepath, mime, uploaded_at, exif_datetime, camera_model FROM files " + where + " ORDER BY uploaded_at DESC LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
		rows, err := db.DB.Query(qry, args...)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		items := scanMediaRows(rows)
		respondJSON(w, map[string]interface{}{"items": items, "offset": offset, "limit": limit, "source": "like"})
		return
	}

	// Build WHERE clauses for tokens. We also prepare a lightweight ranking using CASE:
	// prefix matches in filename get higher score, then contains matches.
	whereParts := []string{}
	args := []interface{}{}

	// token conditions (filename OR camera_model OR exif_datetime OR filepath)
	tokenConds := []string{}
	for _, t := range toks {
		tokenConds = append(tokenConds, "(filename LIKE ? OR camera_model LIKE ? OR exif_datetime LIKE ? OR filepath LIKE ?)")
		// add args: contains
		c := "%" + t + "%"
		args = append(args, c, c, c, c)
	}
	whereParts = append(whereParts, strings.Join(tokenConds, " AND ")) // AND between tokens

	// optional filters
	if mimeFilter != "" {
		whereParts = append(whereParts, "mime = ?")
		args = append(args, mimeFilter)
	}
	if dateFrom != "" {
		if t, err := time.Parse("2006-01-02", dateFrom); err == nil {
			whereParts = append(whereParts, "date(uploaded_at) >= date(?)")
			args = append(args, t.Format("2006-01-02"))
		}
	}
	if dateTo != "" {
		if t, err := time.Parse("2006-01-02", dateTo); err == nil {
			whereParts = append(whereParts, "date(uploaded_at) <= date(?)")
			args = append(args, t.Format("2006-01-02"))
		}
	}

	whereClause := "WHERE " + strings.Join(whereParts, " AND ")

	// Ranking CASE expression:
	// higher score if filename starts with the first token, then filename contains token, else 0
	// This is simple but improves relevance for common queries.
	scoreParts := []string{}
	first := toks[0]
	scoreParts = append(scoreParts, fmt.Sprintf("CASE WHEN lower(filename) LIKE lower('%s%%') THEN 200 ELSE 0 END", escapeSQLLike(first)))
	for _, t := range toks {
		scoreParts = append(scoreParts, fmt.Sprintf("CASE WHEN lower(filename) LIKE lower('%%%s%%') THEN 50 ELSE 0 END", escapeSQLLike(t)))
		scoreParts = append(scoreParts, fmt.Sprintf("CASE WHEN lower(camera_model) LIKE lower('%%%s%%') THEN 20 ELSE 0 END", escapeSQLLike(t)))
	}
	scoreExpr := "(" + strings.Join(scoreParts, " + ") + ") AS score"

	qry := "SELECT id, filename, filepath, mime, uploaded_at, exif_datetime, camera_model, " + scoreExpr +
		" FROM files " + whereClause + " ORDER BY score DESC, uploaded_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := db.DB.Query(qry, args...)
	if err != nil {
		http.Error(w, "db error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	// We scanned an extra column (score) so read accordingly
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
			score     sql.NullFloat64
		)
		_ = rows.Scan(&id, &filename, &filepathS, &mimeS, &uploaded, &exifDT, &camera, &score)
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
			"uploadedAt": uploaded.String,
			"score":      score.Float64,
			"exif": map[string]interface{}{
				"datetime":    exifDT.String,
				"cameraModel": camera.String,
			},
			"thumb": "/api/thumbnail?path=" + url.QueryEscape(itemPath) + "&w=360",
		}
		out = append(out, item)
	}

	respondJSON(w, map[string]interface{}{"items": out, "offset": offset, "limit": limit, "source": "like_ranked"})
}

// buildFTSMatch is used when FTS5 is available; it produces token* OR token2* queries
func buildFTSMatch(query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	parts := strings.Fields(query)
	toks := []string{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) < 2 {
			continue
		}
		p = strings.ReplaceAll(p, `"`, `""`)
		toks = append(toks, p+"*")
	}
	if len(toks) == 0 {
		return ""
	}
	return strings.Join(toks, " OR ")
}

// escapeSQLLike escapes '%' and '_' for safe embedding in LIKE literals used in score expression.
// This is simple: replace % and _ with escaped versions. (We only use this in code-generated literals.)
func escapeSQLLike(s string) string {
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	return s
}

// respondJSON helper
func respondJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
