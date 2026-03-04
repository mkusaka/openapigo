package openapigo

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
)

// File represents a file for multipart/form-data uploads.
type File struct {
	Name   string    // filename
	Reader io.Reader // file content (if io.ReadCloser, closed by runtime after send)
}

// FileFromPath opens a file for reading. The returned File.Reader
// implements io.ReadCloser; the runtime's Do() calls Close() on the
// reader after the request body is fully written.
// If the caller does not call Do(), they are responsible for closing
// the underlying *os.File themselves to avoid a resource leak.
func FileFromPath(path string) (File, error) {
	f, err := os.Open(path)
	if err != nil {
		return File{}, err
	}
	return File{Name: filepath.Base(path), Reader: f}, nil
}

// FileFromBytes creates a File from in-memory bytes.
func FileFromBytes(name string, data []byte) File {
	return File{Name: name, Reader: bytes.NewReader(data)}
}

// FileFromReader creates a File from an io.Reader.
func FileFromReader(name string, r io.Reader) File {
	return File{Name: name, Reader: r}
}
