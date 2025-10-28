package storage

import (
	"io"
	"os"
	"path/filepath"
)

// SaveFile writes the provided reader to destDir/filename
func SaveFile(destDir, filename string, r io.Reader) (string, error) {
	filename = filepath.Base(filename) // prevent path traversal
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

// DeleteFile removes a file under destDir (filename is sanitized)
func DeleteFile(destDir, filename string) error {
	filename = filepath.Base(filename)
	path := filepath.Join(destDir, filename)
	return os.Remove(path)
}

// CopyFile copies src -> dst, creating parent directories as needed, using atomic tmp->rename
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	// write to temp file then rename for safety
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}

	// atomic rename
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
