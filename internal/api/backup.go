package api

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"localcloud/internal/db"
	"localcloud/internal/storage"
)

type backupJob struct {
	absPath string
	mediaID int64
}

var backupQueue chan backupJob

// StartBackupWorker starts N worker goroutines that copy files to backupDir.
// Call once at startup: e.g. StartBackupWorker(3, filepath.Join(config.DataDir,"backups"))
func StartBackupWorker(concurrency int, backupDir string) {
	if backupQueue != nil {
		return
	}
	backupQueue = make(chan backupJob, 4096)
	for i := 0; i < concurrency; i++ {
		go func() {
			for job := range backupQueue {
				processBackup(job, backupDir)
			}
		}()
	}
}

// EnqueueBackup enqueues a file for backup (best-effort)
func EnqueueBackup(absPath string, mediaID int64) {
	if backupQueue == nil {
		// If called before StartBackupWorker, best-effort start default worker to backup under DataDir/backups
		go StartBackupWorker(2, filepath.Join(DataDir, "backups"))
	}
	select {
	case backupQueue <- backupJob{absPath: absPath, mediaID: mediaID}:
	default:
		// queue full -> drop (or consider persistent queue)
	}
}

func processBackup(job backupJob, backupDir string) {
	abs := job.absPath
	// verify that source exists
	if _, err := os.Stat(abs); err != nil {
		fmt.Println("backup: source missing:", abs)
		return
	}
	rel, _ := filepath.Rel(DataDir, abs)
	dest := filepath.Join(backupDir, rel)

	// ensure destination dir
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		fmt.Println("backup: mkdir err:", err)
		return
	}

	// if already exists, skip
	if _, err := os.Stat(dest); err == nil {
		// update DB
		now := time.Now().Format(time.RFC3339)
		_, _ = db.DB.Exec("UPDATE media SET backed_up = 1, backup_path = ?, backup_at = ? WHERE id = ?", dest, now, job.mediaID)
		return
	}

	// copy file atomically (simple copy then update)
	if err := storage.CopyFile(abs, dest); err != nil {
		fmt.Println("backup: copy err:", err)
		// we could increment retry count here
		return
	}

	// success: update DB
	now := time.Now().Format(time.RFC3339)
	_, err := db.DB.Exec("UPDATE media SET backed_up = 1, backup_path = ?, backup_at = ? WHERE id = ?", dest, now, job.mediaID)
	if err != nil {
		fmt.Println("backup: db update err:", err)
	}
}
