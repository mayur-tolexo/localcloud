package db

import (
	"database/sql"
	"log"

	_ "github.com/mattn/go-sqlite3"
)

var DB *sql.DB

func InitDB(path string) {
	var err error
	DB, err = sql.Open("sqlite3", path+"?_foreign_keys=on")
	if err != nil {
		log.Fatalf("open db: %v", err)
	}

	createSQL := `
	CREATE TABLE IF NOT EXISTS files (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		filename TEXT NOT NULL UNIQUE,
		filepath TEXT NOT NULL,
		uploaded_at DATETIME DEFAULT (datetime('now'))
	);
	`
	if _, err := DB.Exec(createSQL); err != nil {
		log.Fatalf("create table: %v", err)
	}
}
