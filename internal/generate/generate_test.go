package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mkusaka/openapigo/internal/generate"
)

// repoRoot returns the repository root (two levels up from this test file).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// TestGenerateAndCompile generates code from a spec and verifies it compiles.
func TestGenerateAndCompile(t *testing.T) {
	root := repoRoot(t)

	tests := []struct {
		name string
		spec string
	}{
		{"basic-crud", filepath.Join(root, "testdata", "cases", "basic-crud", "spec.json")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outDir := t.TempDir()

			// Generate code.
			err := generate.Run(generate.Config{
				Input:   tt.spec,
				Output:  outDir,
				Package: "generated",
			})
			if err != nil {
				t.Fatalf("generate.Run: %v", err)
			}

			// Write go.mod with replace directive to local runtime.
			goMod := "module test/generated\n\ngo 1.24\n\ntoolchain go1.26.0\n\n" +
				"require github.com/mkusaka/openapigo v0.0.0\n\n" +
				"replace github.com/mkusaka/openapigo => " + root + "\n"
			if err := os.WriteFile(filepath.Join(outDir, "go.mod"), []byte(goMod), 0o644); err != nil {
				t.Fatalf("write go.mod: %v", err)
			}

			// Run go build.
			cmd := exec.Command("go", "build", "./...")
			cmd.Dir = outDir
			out, err := cmd.CombinedOutput()
			if err != nil {
				// List generated files for debugging.
				entries, _ := os.ReadDir(outDir)
				for _, e := range entries {
					if filepath.Ext(e.Name()) == ".go" {
						data, _ := os.ReadFile(filepath.Join(outDir, e.Name()))
						t.Logf("=== %s ===\n%s", e.Name(), data)
					}
				}
				t.Fatalf("go build failed:\n%s\n%v", out, err)
			}
		})
	}
}
