package generate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")

	code := []byte("package foo\n\nvar x = 1\n")
	if err := writeFileAtomic(path, code); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("empty file")
	}

	// No temp files should remain.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "test.go" {
			t.Errorf("unexpected file: %s", e.Name())
		}
	}
}

func TestWriteFileAtomicInvalidGo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.go")

	code := []byte("not valid go code {{{")
	err := writeFileAtomic(path, code)
	if err == nil {
		t.Fatal("expected error for invalid Go code")
	}

	// Unformatted debug file should exist.
	if _, err := os.Stat(path + ".unformatted"); err != nil {
		t.Errorf("expected .unformatted file: %v", err)
	}
}

func TestCleanStaleFiles(t *testing.T) {
	dir := t.TempDir()

	// Create a generated file that will be stale.
	staleContent := generatedFileHeader + "\npackage foo\n"
	if err := os.WriteFile(filepath.Join(dir, "old_types.go"), []byte(staleContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a user-authored file (no generated header).
	userContent := "package foo\n\n// My custom code.\nfunc MyFunc() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "custom.go"), []byte(userContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a current generated file.
	currentContent := generatedFileHeader + "\npackage foo\n\nvar x = 1\n"
	if err := os.WriteFile(filepath.Join(dir, "types.go"), []byte(currentContent), 0o644); err != nil {
		t.Fatal(err)
	}

	currentFiles := map[string]bool{"types.go": true}
	if err := cleanStaleFiles(dir, currentFiles); err != nil {
		t.Fatalf("cleanStaleFiles: %v", err)
	}

	// Stale generated file should be removed.
	if _, err := os.Stat(filepath.Join(dir, "old_types.go")); !os.IsNotExist(err) {
		t.Error("stale file old_types.go should have been removed")
	}

	// User file should be preserved.
	if _, err := os.Stat(filepath.Join(dir, "custom.go")); err != nil {
		t.Error("user file custom.go should be preserved")
	}

	// Current generated file should be preserved.
	if _, err := os.Stat(filepath.Join(dir, "types.go")); err != nil {
		t.Error("current file types.go should be preserved")
	}
}

func TestCleanStaleFilesNonExistentDir(t *testing.T) {
	// Should not error on non-existent directory.
	err := cleanStaleFiles("/nonexistent/path/abc123", nil)
	if err != nil {
		t.Fatalf("expected nil error for nonexistent dir, got: %v", err)
	}
}

func TestSanitizePackageName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"api", "api"},
		{"my-api", "my_api"},
		{"my.api", "my_api"},
		{"MyAPI", "myapi"},
		{"123api", "pkg123api"},
		{"main", "main_api"},
		{"go-client", "go_client"},
		{"", "api"},
		{"type", "type_api"},
		{"func", "func_api"},
		{"api-v2", "api_v2"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SanitizePackageName(tt.input)
			if got != tt.want {
				t.Errorf("SanitizePackageName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsGeneratedFile(t *testing.T) {
	dir := t.TempDir()

	// Generated file.
	genPath := filepath.Join(dir, "gen.go")
	os.WriteFile(genPath, []byte(generatedFileHeader+"\npackage foo\n"), 0o644)
	if !isGeneratedFile(genPath) {
		t.Error("expected gen.go to be detected as generated")
	}

	// User file.
	userPath := filepath.Join(dir, "user.go")
	os.WriteFile(userPath, []byte("package foo\n"), 0o644)
	if isGeneratedFile(userPath) {
		t.Error("expected user.go to NOT be detected as generated")
	}

	// Non-existent file.
	if isGeneratedFile(filepath.Join(dir, "nope.go")) {
		t.Error("expected non-existent file to return false")
	}
}
