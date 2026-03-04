package openapigo

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileFromBytes(t *testing.T) {
	f := FileFromBytes("test.txt", []byte("hello"))
	if f.Name != "test.txt" {
		t.Errorf("Name = %q, want %q", f.Name, "test.txt")
	}
	data, err := io.ReadAll(f.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Errorf("content = %q, want %q", data, "hello")
	}
}

func TestFileFromReader(t *testing.T) {
	f := FileFromReader("data.bin", strings.NewReader("binary"))
	if f.Name != "data.bin" {
		t.Errorf("Name = %q, want %q", f.Name, "data.bin")
	}
	data, err := io.ReadAll(f.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "binary" {
		t.Errorf("content = %q, want %q", data, "binary")
	}
}

func TestFileFromPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "upload.txt")
	if err := os.WriteFile(path, []byte("file content"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := FileFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "upload.txt" {
		t.Errorf("Name = %q, want %q", f.Name, "upload.txt")
	}
	data, err := io.ReadAll(f.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "file content" {
		t.Errorf("content = %q, want %q", data, "file content")
	}
	// Reader should be an *os.File (io.ReadCloser).
	if rc, ok := f.Reader.(io.ReadCloser); ok {
		rc.Close()
	}
}

func TestFileFromPath_NotFound(t *testing.T) {
	_, err := FileFromPath("/nonexistent/file.txt")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}
