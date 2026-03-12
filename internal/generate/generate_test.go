package generate_test

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mkusaka/openapigo/internal/generate"
)

var updateGolden = flag.Bool("update-golden", false, "update golden files")

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
		name    string
		spec    string
		resolve bool
	}{
		{"basic-crud", filepath.Join(root, "testdata", "cases", "basic-crud", "spec.json"), false},
		{"composition", filepath.Join(root, "testdata", "cases", "composition", "spec.json"), false},
		{"readwrite", filepath.Join(root, "testdata", "cases", "readwrite", "spec.json"), false},
		{"validation", filepath.Join(root, "testdata", "cases", "validation", "spec.json"), false},
		{"circleci-v2", filepath.Join(root, "testdata", "realworld", "circleci-v2.json"), false},
		{"media-types", filepath.Join(root, "testdata", "cases", "media-types", "spec.json"), false},
		{"petstore-3.0", filepath.Join(root, "testdata", "realworld", "petstore-3.0.json"), false},
		{"multi-file", filepath.Join(root, "testdata", "cases", "multi-file", "spec.json"), true},
		{"conditional", filepath.Join(root, "testdata", "cases", "conditional", "spec.json"), false},
		{"pattern-props", filepath.Join(root, "testdata", "cases", "pattern-props", "spec.json"), false},
		{"unevaluated", filepath.Join(root, "testdata", "cases", "unevaluated", "spec.json"), false},
		{"oneof-union", filepath.Join(root, "testdata", "cases", "oneof-union", "spec.json"), false},
		{"github-api-subset", filepath.Join(root, "testdata", "realworld", "github-api-subset.json"), false},
		{"stripe-api-subset", filepath.Join(root, "testdata", "realworld", "stripe-api-subset.json"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outDir := t.TempDir()

			// Generate code.
			err := generate.Run(generate.Config{
				Input:   tt.spec,
				Output:  outDir,
				Package: "generated",
				Resolve: tt.resolve,
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

// TestGenerateGolden compares generated code against expected golden files.
// Run with -update-golden to update the golden files.
func TestGenerateGolden(t *testing.T) {
	root := repoRoot(t)

	tests := []struct {
		name        string
		spec        string
		expectedDir string
		resolve     bool
	}{
		{
			"basic-crud",
			filepath.Join(root, "testdata", "cases", "basic-crud", "spec.json"),
			filepath.Join(root, "testdata", "cases", "basic-crud", "expected"),
			false,
		},
		{
			"composition",
			filepath.Join(root, "testdata", "cases", "composition", "spec.json"),
			filepath.Join(root, "testdata", "cases", "composition", "expected"),
			false,
		},
		{
			"readwrite",
			filepath.Join(root, "testdata", "cases", "readwrite", "spec.json"),
			filepath.Join(root, "testdata", "cases", "readwrite", "expected"),
			false,
		},
		{
			"validation",
			filepath.Join(root, "testdata", "cases", "validation", "spec.json"),
			filepath.Join(root, "testdata", "cases", "validation", "expected"),
			false,
		},
		{
			"media-types",
			filepath.Join(root, "testdata", "cases", "media-types", "spec.json"),
			filepath.Join(root, "testdata", "cases", "media-types", "expected"),
			false,
		},
		{
			"multi-file",
			filepath.Join(root, "testdata", "cases", "multi-file", "spec.json"),
			filepath.Join(root, "testdata", "cases", "multi-file", "expected"),
			true,
		},
		{
			"conditional",
			filepath.Join(root, "testdata", "cases", "conditional", "spec.json"),
			filepath.Join(root, "testdata", "cases", "conditional", "expected"),
			false,
		},
		{
			"pattern-props",
			filepath.Join(root, "testdata", "cases", "pattern-props", "spec.json"),
			filepath.Join(root, "testdata", "cases", "pattern-props", "expected"),
			false,
		},
		{
			"unevaluated",
			filepath.Join(root, "testdata", "cases", "unevaluated", "spec.json"),
			filepath.Join(root, "testdata", "cases", "unevaluated", "expected"),
			false,
		},
		{
			"oneof-union",
			filepath.Join(root, "testdata", "cases", "oneof-union", "spec.json"),
			filepath.Join(root, "testdata", "cases", "oneof-union", "expected"),
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outDir := t.TempDir()

			err := generate.Run(generate.Config{
				Input:   tt.spec,
				Output:  outDir,
				Package: "generated",
				Resolve: tt.resolve,
			})
			if err != nil {
				t.Fatalf("generate.Run: %v", err)
			}

			if *updateGolden {
				// Update golden files from generated output.
				if err := os.MkdirAll(tt.expectedDir, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				entries, err := os.ReadDir(outDir)
				if err != nil {
					t.Fatalf("readdir: %v", err)
				}
				for _, e := range entries {
					if filepath.Ext(e.Name()) != ".go" {
						continue
					}
					data, err := os.ReadFile(filepath.Join(outDir, e.Name()))
					if err != nil {
						t.Fatalf("read %s: %v", e.Name(), err)
					}
					if err := os.WriteFile(filepath.Join(tt.expectedDir, e.Name()), data, 0o644); err != nil {
						t.Fatalf("write %s: %v", e.Name(), err)
					}
				}
				t.Logf("updated golden files in %s", tt.expectedDir)
				return
			}

			// Compare generated files against golden files.
			expectedEntries, err := os.ReadDir(tt.expectedDir)
			if err != nil {
				t.Fatalf("readdir %s: %v (run with -update-golden to create)", tt.expectedDir, err)
			}

			expectedFiles := make(map[string][]byte)
			for _, e := range expectedEntries {
				if filepath.Ext(e.Name()) != ".go" {
					continue
				}
				data, err := os.ReadFile(filepath.Join(tt.expectedDir, e.Name()))
				if err != nil {
					t.Fatalf("read golden %s: %v", e.Name(), err)
				}
				expectedFiles[e.Name()] = data
			}

			generatedEntries, err := os.ReadDir(outDir)
			if err != nil {
				t.Fatalf("readdir: %v", err)
			}

			generatedFiles := make(map[string][]byte)
			for _, e := range generatedEntries {
				if filepath.Ext(e.Name()) != ".go" {
					continue
				}
				data, err := os.ReadFile(filepath.Join(outDir, e.Name()))
				if err != nil {
					t.Fatalf("read generated %s: %v", e.Name(), err)
				}
				generatedFiles[e.Name()] = data
			}

			// Check for missing or extra files.
			for name := range expectedFiles {
				if _, ok := generatedFiles[name]; !ok {
					t.Errorf("expected file %s not generated", name)
				}
			}
			for name := range generatedFiles {
				if _, ok := expectedFiles[name]; !ok {
					t.Errorf("unexpected generated file %s", name)
				}
			}

			// Compare contents.
			for name, expected := range expectedFiles {
				got, ok := generatedFiles[name]
				if !ok {
					continue
				}
				if string(got) != string(expected) {
					t.Errorf("%s: content mismatch (run with -update-golden to update)\n--- expected ---\n%s\n--- got ---\n%s",
						name, expected, got)
				}
			}
		})
	}
}

// TestConfigFlags tests that CLI config flags affect generation correctly.
func TestConfigFlags(t *testing.T) {
	root := repoRoot(t)
	// Use validation spec since it has Validate() methods and readOnly/writeOnly.
	validationSpec := filepath.Join(root, "testdata", "cases", "validation", "spec.json")

	// Helper to generate and return types.go content.
	genTypes := func(t *testing.T, cfg generate.Config) string {
		t.Helper()
		outDir := t.TempDir()
		cfg.Input = validationSpec
		cfg.Output = outDir
		cfg.Package = "generated"
		if err := generate.Run(cfg); err != nil {
			t.Fatalf("generate.Run: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(outDir, "types.go"))
		if err != nil {
			t.Fatalf("read types.go: %v", err)
		}
		return string(data)
	}

	t.Run("skip-validation", func(t *testing.T) {
		normal := genTypes(t, generate.Config{})
		skipped := genTypes(t, generate.Config{SkipValidation: true})

		if !strings.Contains(normal, "func (v ") || !strings.Contains(normal, "Validate()") {
			t.Fatal("normal output should contain Validate() methods")
		}
		if strings.Contains(skipped, "Validate()") {
			t.Error("--skip-validation output should not contain Validate() methods")
		}
	})

	t.Run("no-read-write-types", func(t *testing.T) {
		// Use readwrite spec which has readOnly/writeOnly properties.
		rwSpec := filepath.Join(root, "testdata", "cases", "readwrite", "spec.json")
		genRW := func(cfg generate.Config) string {
			outDir := t.TempDir()
			cfg.Input = rwSpec
			cfg.Output = outDir
			cfg.Package = "generated"
			if err := generate.Run(cfg); err != nil {
				t.Fatalf("generate.Run: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(outDir, "types.go"))
			if err != nil {
				t.Fatalf("read types.go: %v", err)
			}
			return string(data)
		}
		normal := genRW(generate.Config{})
		noRW := genRW(generate.Config{NoReadWriteTypes: true})

		if !strings.Contains(normal, "Request struct") {
			t.Fatal("normal readwrite output should contain Request/Response variant structs")
		}
		if strings.Contains(noRW, "Request struct") {
			t.Error("--no-read-write-types should not contain Request variant structs")
		}
	})

	t.Run("dry-run", func(t *testing.T) {
		outDir := t.TempDir()
		err := generate.Run(generate.Config{
			Input:   validationSpec,
			Output:  outDir,
			Package: "generated",
			DryRun:  true,
		})
		if err != nil {
			t.Fatalf("generate.Run: %v", err)
		}
		// No files should be written.
		entries, _ := os.ReadDir(outDir)
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".go" {
				t.Errorf("--dry-run should not write files, found %s", e.Name())
			}
		}
	})

	t.Run("validate-on-unmarshal", func(t *testing.T) {
		normal := genTypes(t, generate.Config{})
		withUnmarshal := genTypes(t, generate.Config{ValidateOnUnmarshal: true})

		if strings.Contains(normal, "UnmarshalJSON") {
			t.Fatal("normal output should not contain UnmarshalJSON")
		}
		if !strings.Contains(withUnmarshal, "UnmarshalJSON") {
			t.Error("--validate-on-unmarshal output should contain UnmarshalJSON")
		}
	})

	t.Run("deterministic-output", func(t *testing.T) {
		// Regression test: generation must produce byte-identical output
		// across repeated runs. This catches nondeterministic map iteration
		// (e.g. over op.Responses) leaking into inline schema discovery order.
		specPath := filepath.Join(root, "testdata", "cases", "determinism", "spec.json")

		// Generate once to get the reference output.
		refDir := t.TempDir()
		if err := generate.Run(generate.Config{
			Input:   specPath,
			Output:  refDir,
			Package: "generated",
		}); err != nil {
			t.Fatalf("generate.Run (reference): %v", err)
		}
		refFiles := readGoFiles(t, refDir)

		// Re-generate 20 times and compare.
		for i := range 20 {
			dir := t.TempDir()
			if err := generate.Run(generate.Config{
				Input:   specPath,
				Output:  dir,
				Package: "generated",
			}); err != nil {
				t.Fatalf("generate.Run (iteration %d): %v", i, err)
			}
			got := readGoFiles(t, dir)
			for name, ref := range refFiles {
				if got[name] != ref {
					t.Fatalf("iteration %d: %s differs from reference output", i, name)
				}
			}
		}
	})

	t.Run("validate-on-unmarshal-compiles", func(t *testing.T) {
		outDir := t.TempDir()
		err := generate.Run(generate.Config{
			Input:               validationSpec,
			Output:              outDir,
			Package:             "generated",
			ValidateOnUnmarshal: true,
		})
		if err != nil {
			t.Fatalf("generate.Run: %v", err)
		}
		goMod := "module test/generated\n\ngo 1.24\n\ntoolchain go1.26.0\n\n" +
			"require github.com/mkusaka/openapigo v0.0.0\n\n" +
			"replace github.com/mkusaka/openapigo => " + root + "\n"
		os.WriteFile(filepath.Join(outDir, "go.mod"), []byte(goMod), 0o644)
		cmd := exec.Command("go", "build", "./...")
		cmd.Dir = outDir
		out, err := cmd.CombinedOutput()
		if err != nil {
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

// readGoFiles reads all .go files from a directory into a map.
func readGoFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	files := make(map[string]string)
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".go" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		files[e.Name()] = string(data)
	}
	return files
}
