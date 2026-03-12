package spec

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testdataDir() string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(f), "..", "..", "testdata")
}

func TestLoad_CircleCISpec(t *testing.T) {
	path := filepath.Join(testdataDir(), "realworld", "circleci-v2.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("CircleCI spec not found at %s", path)
	}

	doc, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Version detection.
	ver, err := DetectVersion(doc)
	if err != nil {
		t.Fatalf("DetectVersion: %v", err)
	}
	if ver != "3.0.3" {
		t.Errorf("version = %q, want 3.0.3", ver)
	}

	// Paths.
	if len(doc.Paths) == 0 {
		t.Fatal("no paths found")
	}
	if len(doc.PathOrder) == 0 {
		t.Error("path order not preserved")
	}

	// Count operations.
	opCount := 0
	for _, pi := range doc.Paths {
		opCount += len(pi.Operations())
	}
	if opCount < 100 {
		t.Errorf("operation count = %d, expected > 100", opCount)
	}
	t.Logf("paths=%d operations=%d", len(doc.Paths), opCount)

	// Components.
	if doc.Components == nil {
		t.Fatal("no components")
	}
	if len(doc.Components.Schemas) == 0 {
		t.Error("no schemas")
	}
	t.Logf("schemas=%d", len(doc.Components.Schemas))

	// Security schemes.
	if len(doc.Components.SecuritySchemes) == 0 {
		t.Error("no security schemes")
	}
	for name, ss := range doc.Components.SecuritySchemes {
		t.Logf("securityScheme %q: type=%s", name, ss.Type)
	}
}

func TestResolve_CircleCISpec(t *testing.T) {
	path := filepath.Join(testdataDir(), "realworld", "circleci-v2.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skipf("CircleCI spec not found at %s", path)
	}

	doc, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if err := Resolve(doc); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Verify some $ref resolutions.
	// All parameters should now have their Parameter field set.
	for path, pi := range doc.Paths {
		for _, op := range pi.Operations() {
			for _, p := range op.Operation.Parameters {
				if p.Ref != "" {
					t.Errorf("%s %s: unresolved parameter $ref %q", op.Method, path, p.Ref)
				}
				if p.Parameter == nil {
					t.Errorf("%s %s: parameter is nil after resolution", op.Method, path)
				}
			}
		}
	}

	// Verify response refs are resolved.
	for path, pi := range doc.Paths {
		for _, op := range pi.Operations() {
			for code, resp := range op.Operation.Responses {
				if resp.Ref != "" {
					t.Errorf("%s %s %s: unresolved response $ref %q", op.Method, path, code, resp.Ref)
				}
			}
		}
	}

	t.Log("all $ref resolved successfully")
}

func TestDetectVersion_Unsupported(t *testing.T) {
	doc := &Document{OpenAPI: "2.0"}
	_, err := DetectVersion(doc)
	if err == nil {
		t.Error("expected error for version 2.0")
	}
}

func TestDetectVersion_Missing(t *testing.T) {
	doc := &Document{}
	_, err := DetectVersion(doc)
	if err == nil {
		t.Error("expected error for missing version")
	}
}

func TestParse_SimpleYAML(t *testing.T) {
	yaml := `
openapi: "3.0.3"
info:
  title: Test
  version: "1.0"
paths:
  /pets:
    get:
      operationId: listPets
      responses:
        "200":
          description: OK
components:
  schemas:
    Pet:
      type: object
      properties:
        id:
          type: integer
        name:
          type: string
      required:
        - id
        - name
`
	doc, err := Parse([]byte(yaml), ".yaml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Paths["/pets"] == nil {
		t.Fatal("missing /pets path")
	}
	if doc.Paths["/pets"].Get == nil {
		t.Fatal("missing GET /pets")
	}
	if doc.Paths["/pets"].Get.OperationID != "listPets" {
		t.Errorf("operationId = %q", doc.Paths["/pets"].Get.OperationID)
	}
	pet := doc.Components.Schemas["Pet"]
	if pet == nil {
		t.Fatal("missing Pet schema")
	}
	if len(pet.Properties) != 2 {
		t.Errorf("properties count = %d", len(pet.Properties))
	}
	if len(pet.Required) != 2 {
		t.Errorf("required count = %d", len(pet.Required))
	}
}

func TestParse_Discriminator(t *testing.T) {
	specJSON := `{
		"openapi": "3.0.3",
		"info": {"title": "Test", "version": "1.0"},
		"paths": {},
		"components": {
			"schemas": {
				"Pet": {
					"oneOf": [
						{"$ref": "#/components/schemas/Cat"},
						{"$ref": "#/components/schemas/Dog"}
					],
					"discriminator": {
						"propertyName": "petType",
						"mapping": {
							"cat": "#/components/schemas/Cat",
							"dog": "#/components/schemas/Dog"
						}
					}
				},
				"Cat": {
					"type": "object",
					"properties": {
						"petType": {"type": "string"},
						"whiskers": {"type": "boolean"}
					}
				},
				"Dog": {
					"type": "object",
					"properties": {
						"petType": {"type": "string"},
						"breed": {"type": "string"}
					}
				}
			}
		}
	}`
	doc, err := Parse([]byte(specJSON), ".json")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	pet := doc.Components.Schemas["Pet"]
	if pet == nil {
		t.Fatal("missing Pet schema")
	}
	if pet.Discriminator == nil {
		t.Fatal("discriminator not parsed")
	}
	if pet.Discriminator.PropertyName != "petType" {
		t.Errorf("propertyName = %q, want petType", pet.Discriminator.PropertyName)
	}
	if len(pet.Discriminator.Mapping) != 2 {
		t.Errorf("mapping count = %d, want 2", len(pet.Discriminator.Mapping))
	}
	if pet.Discriminator.Mapping["cat"] != "#/components/schemas/Cat" {
		t.Errorf("mapping[cat] = %q", pet.Discriminator.Mapping["cat"])
	}
}
