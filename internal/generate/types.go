package generate

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/mkusaka/openapigo/internal/spec"
)

// GoType returns the Go type string for a schema.
// It also returns any type definitions that need to be emitted (e.g., for inline objects).
func (g *Generator) GoType(s *spec.Schema, name string) string {
	if s == nil {
		return "any"
	}
	s = s.Resolved()

	// Check if this is a named schema with an assigned Go name.
	if goName, ok := g.schemaNames[s]; ok {
		return goName
	}

	return g.goTypeInner(s, name)
}

func (g *Generator) goTypeInner(s *spec.Schema, name string) string {
	// Handle composition.
	if len(s.AllOf) > 0 {
		return g.allOfType(s, name)
	}
	if len(s.OneOf) > 0 || len(s.AnyOf) > 0 {
		return "any"
	}

	switch s.Type {
	case "string":
		return g.stringType(s)
	case "integer":
		return g.integerType(s)
	case "number":
		return g.numberType(s)
	case "boolean":
		return "bool"
	case "array":
		elemType := g.GoType(s.Items, name+"Item")
		return "[]" + elemType
	case "object":
		// No properties → map type.
		if len(s.Properties) == 0 {
			if s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil {
				valType := g.GoType(s.AdditionalProperties.Schema, name+"Value")
				return "map[string]" + valType
			}
			return "map[string]any"
		}
		// Object with properties → needs a named struct.
		// If we don't have a name, it's an inline schema that needs to be registered.
		if name != "" {
			goName := ToPascalCase(name)
			if _, exists := g.schemaNames[s]; !exists {
				g.schemaNames[s] = goName
				g.inlineSchemas = append(g.inlineSchemas, namedSchema{name: goName, schema: s})
			}
			return goName
		}
		return "any"
	case "":
		// No type specified — free-form.
		if len(s.Properties) > 0 {
			// Treat as object.
			if name != "" {
				goName := ToPascalCase(name)
				if _, exists := g.schemaNames[s]; !exists {
					g.schemaNames[s] = goName
					g.inlineSchemas = append(g.inlineSchemas, namedSchema{name: goName, schema: s})
				}
				return goName
			}
		}
		return "any"
	default:
		return "any"
	}
}

func (g *Generator) stringType(s *spec.Schema) string {
	switch s.Format {
	case "date-time":
		g.imports["time"] = true
		return "time.Time"
	case "date":
		return "openapigo.Date"
	case "uuid":
		return "string"
	case "uri", "url":
		return "string"
	case "byte":
		return "[]byte"
	case "binary":
		return "[]byte"
	default:
		return "string"
	}
}

func (g *Generator) integerType(s *spec.Schema) string {
	switch s.Format {
	case "int32":
		return "int32"
	case "int64":
		return "int64"
	default:
		return "int64"
	}
}

func (g *Generator) numberType(s *spec.Schema) string {
	switch s.Format {
	case "float":
		return "float32"
	case "double":
		return "float64"
	default:
		return "float64"
	}
}

// isNullable reports whether a schema produces a nullable Go type.
// Checks both OAS 3.0 nullable and x-nullable vendor extension.
func isNullable(s *spec.Schema) bool {
	if s.Nullable {
		return true
	}
	if v, ok := s.Extensions["x-nullable"]; ok {
		var b bool
		if json.Unmarshal(v, &b) == nil && b {
			return true
		}
	}
	return false
}

// isRequired reports whether a field name is in the required list.
func isRequired(name string, required []string) bool {
	return slices.Contains(required, name)
}

// wrapNullableOptional wraps a Go type based on nullable/optional status.
// required + non-nullable → T
// optional + non-nullable → *T
// nullable → Nullable[T]
func wrapNullableOptional(goType string, required, nullable bool) string {
	if nullable {
		return "openapigo.Nullable[" + goType + "]"
	}
	if !required {
		return "*" + goType
	}
	return goType
}

// emitEnumType writes a Go enum type definition.
func emitEnumType(w *strings.Builder, typeName string, s *spec.Schema) {
	goType := "string" // CircleCI only uses string enums.
	fmt.Fprintf(w, "// %s represents the enum values.\ntype %s %s\n\n", typeName, typeName, goType)

	// Constants.
	fmt.Fprintf(w, "const (\n")
	for _, raw := range s.Enum {
		var val string
		if err := json.Unmarshal(raw, &val); err != nil {
			continue
		}
		constName := ToEnumConstName(typeName, val)
		fmt.Fprintf(w, "\t%s %s = %q\n", constName, typeName, val)
	}
	fmt.Fprintf(w, ")\n\n")

	// Values function.
	fmt.Fprintf(w, "// %sValues returns all valid values of %s.\nfunc %sValues() []%s {\n\treturn []%s{\n",
		typeName, typeName, typeName, typeName, typeName)
	for _, raw := range s.Enum {
		var val string
		if err := json.Unmarshal(raw, &val); err != nil {
			continue
		}
		fmt.Fprintf(w, "\t\t%s,\n", ToEnumConstName(typeName, val))
	}
	fmt.Fprintf(w, "\t}\n}\n\n")
}

// emitStructType writes a Go struct type definition.
func (g *Generator) emitStructType(w *strings.Builder, typeName string, s *spec.Schema) {
	fmt.Fprintf(w, "// %s represents the schema.\ntype %s struct {\n", typeName, typeName)

	propOrder := s.PropertyOrder
	if len(propOrder) == 0 {
		// Fallback: alphabetical order.
		for name := range s.Properties {
			propOrder = append(propOrder, name)
		}
		slices.Sort(propOrder)
	}

	for _, propName := range propOrder {
		prop := s.Properties[propName]
		if prop == nil {
			continue
		}
		resolved := prop.Resolved()

		fieldName := ToFieldName(propName)
		// Check for x-go-name vendor extension.
		if v, ok := resolved.Extensions["x-go-name"]; ok {
			var goName string
			if json.Unmarshal(v, &goName) == nil && goName != "" {
				fieldName = goName
			}
		}

		goType := g.GoType(resolved, typeName+ToPascalCase(propName))
		req := isRequired(propName, s.Required)
		nullable := isNullable(resolved)
		wrapped := wrapNullableOptional(goType, req, nullable)

		// JSON tag.
		tag := propName
		if !req && !nullable {
			tag += ",omitzero"
		}

		fmt.Fprintf(w, "\t%s %s `json:\"%s\"`\n", fieldName, wrapped, tag)
	}

	fmt.Fprintf(w, "}\n\n")
}

// allOfType resolves allOf composition to a Go type.
// - Same-type primitives: use that primitive type (e.g., allOf: [int64, int64(min=0)] → int64)
// - Object merges: merge properties into a single struct
// - Mixed/complex: fall back to any
func (g *Generator) allOfType(s *spec.Schema, name string) string {
	resolved := make([]*spec.Schema, 0, len(s.AllOf))
	for _, sub := range s.AllOf {
		if sub != nil {
			resolved = append(resolved, sub.Resolved())
		}
	}
	if len(resolved) == 0 {
		return "any"
	}

	// Check if all schemas are the same primitive type.
	if resolved[0].Type != "" && resolved[0].Type != "object" {
		allSame := true
		for _, sub := range resolved[1:] {
			if sub.Type != resolved[0].Type {
				allSame = false
				break
			}
		}
		if allSame {
			// Use the first schema (pick most specific format if available).
			best := resolved[0]
			for _, sub := range resolved[1:] {
				if sub.Format != "" && best.Format == "" {
					best = sub
				}
			}
			return g.goTypeInner(best, name)
		}
	}

	// Check if any schemas have properties (object-like) — merge properties.
	hasProps := false
	for _, sub := range resolved {
		if len(sub.Properties) > 0 || sub.Type == "object" {
			hasProps = true
			break
		}
	}
	if hasProps {
		merged := g.mergeAllOfProperties(resolved)
		if name != "" {
			goName := ToPascalCase(name)
			if _, exists := g.schemaNames[s]; !exists {
				g.schemaNames[s] = goName
				g.inlineSchemas = append(g.inlineSchemas, namedSchema{name: goName, schema: merged})
			}
			return goName
		}
	}

	return "any"
}

// mergeAllOfProperties merges properties from all allOf sub-schemas into a single schema.
func (g *Generator) mergeAllOfProperties(schemas []*spec.Schema) *spec.Schema {
	merged := &spec.Schema{
		Type:       "object",
		Properties: make(map[string]*spec.Schema),
	}
	for _, sub := range schemas {
		for propName, prop := range sub.Properties {
			if _, exists := merged.Properties[propName]; !exists {
				merged.Properties[propName] = prop
				merged.PropertyOrder = append(merged.PropertyOrder, propName)
			}
		}
		merged.Required = append(merged.Required, sub.Required...)
	}
	return merged
}

// namedSchema pairs a Go name with its schema for deferred emission.
type namedSchema struct {
	name   string
	schema *spec.Schema
}
