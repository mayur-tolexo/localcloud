package storage

import (
	"io"
	"os"
	"path/filepath"
)

// SaveFile writes the provided reader to destDir/filename
func SaveFile(destDir, filename string, r io.Reader) (string, error) {
	// Prevent path traversal by using base name
	filename = filepath.Base(filename)
	outPath := filepath.Join(destDir, filename)

	// ensure destDir exists
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}

	out, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	if _, err := io.Copy(out, r); err != nil {
		return "", err
	}
	return outPath, nil
}

func DeleteFile(destDir, filename string) error {
	filename = filepath.Base(filename)
	path := filepath.Join(destDir, filename)
	return os.Remove(path)
}
