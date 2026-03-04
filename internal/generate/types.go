package generate

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/mkusaka/openapigo/internal/spec"
)

// GoTypeForBody returns the Go type for a request body schema, taking
// the content type into account. For multipart/form-data, format:binary
// fields map to openapigo.File instead of []byte.
// For $ref schemas already registered as component types, a qualified
// copy (e.g., PetMultipart) is created to avoid changing the original type.
func (g *Generator) GoTypeForBody(s *spec.Schema, name, contentType string) string {
	if contentType != "multipart/form-data" {
		return g.GoType(s, name)
	}

	if s == nil {
		return "any"
	}
	resolved := s.Resolved()
	if resolved == nil {
		return "any"
	}

	// If this is a component schema already registered, and it has binary
	// properties, create a qualified inline copy so the original type keeps
	// []byte for JSON contexts.
	if existingName, ok := g.schemaNames[resolved]; ok && hasBinaryProps(resolved) {
		// Return cached multipart name if we already generated one for this schema.
		if cached, ok := g.multipartNameCache[existingName]; ok {
			return cached
		}
		qualName := g.uniqueName(existingName + "Multipart")
		g.multipartTypeNames[qualName] = true
		g.multipartNameCache[existingName] = qualName
		g.inlineSchemas = append(g.inlineSchemas, namedSchema{name: qualName, schema: resolved})
		return qualName
	}

	// For inline schemas, mark the generated type name for multipart treatment.
	if hasBinaryProps(resolved) {
		goName := ToPascalCase(name)
		g.multipartTypeNames[goName] = true
	}

	return g.GoType(s, name)
}

// hasBinaryProps returns true if the schema has any direct properties
// with type:"string" format:"binary", including array items of binary.
func hasBinaryProps(s *spec.Schema) bool {
	for _, prop := range s.Properties {
		if prop == nil {
			continue
		}
		resolved := prop.Resolved()
		if resolved.Type == "string" && resolved.Format == "binary" {
			return true
		}
		// Check array items for binary.
		if resolved.Type == "array" && resolved.Items != nil {
			items := resolved.Items.Resolved()
			if items.Type == "string" && items.Format == "binary" {
				return true
			}
		}
	}
	return false
}

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
	// Custom format mapping takes priority.
	if mapped := g.resolveFormatMapping(s.Format); mapped != "" {
		return mapped
	}
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
		if g.bodyContentType == "multipart/form-data" {
			return "openapigo.File"
		}
		return "[]byte"
	default:
		return "string"
	}
}

// resolveFormatMapping checks Config.FormatMapping for a custom type override.
// Value format: "import/path.TypeName" or just "TypeName".
func (g *Generator) resolveFormatMapping(format string) string {
	if format == "" || len(g.config.FormatMapping) == 0 {
		return ""
	}
	fullType, ok := g.config.FormatMapping[format]
	if !ok {
		return ""
	}
	// Parse "import/path.TypeName" — last dot separates package from type.
	if dotIdx := strings.LastIndex(fullType, "."); dotIdx > 0 {
		importPath := fullType[:dotIdx]
		typeName := fullType[dotIdx+1:]
		g.imports[importPath] = true
		// Use the last segment of the import path as the package alias.
		pkg := importPath
		if slashIdx := strings.LastIndex(importPath, "/"); slashIdx >= 0 {
			pkg = importPath[slashIdx+1:]
		}
		return pkg + "." + typeName
	}
	return fullType
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

	// Pre-compute constant names with collision detection.
	type enumEntry struct {
		constName string
		value     string
	}
	var entries []enumEntry
	usedConsts := make(map[string]int)
	for _, raw := range s.Enum {
		var val string
		if err := json.Unmarshal(raw, &val); err != nil {
			continue
		}
		constName := ToEnumConstName(typeName, val)
		usedConsts[constName]++
		if usedConsts[constName] > 1 {
			constName = fmt.Sprintf("%s%d", constName, usedConsts[constName])
		}
		entries = append(entries, enumEntry{constName: constName, value: val})
	}

	// Constants.
	fmt.Fprintf(w, "const (\n")
	for _, e := range entries {
		fmt.Fprintf(w, "\t%s %s = %q\n", e.constName, typeName, e.value)
	}
	fmt.Fprintf(w, ")\n\n")

	// Values function.
	fmt.Fprintf(w, "// %sValues returns all valid values of %s.\nfunc %sValues() []%s {\n\treturn []%s{\n",
		typeName, typeName, typeName, typeName, typeName)
	for _, e := range entries {
		fmt.Fprintf(w, "\t\t%s,\n", e.constName)
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

	// Pre-compute field names to detect collisions.
	fieldNames := make(map[string]int) // field name → count
	for _, propName := range propOrder {
		prop := s.Properties[propName]
		if prop == nil {
			continue
		}
		fieldName := ToFieldName(propName)
		if v, ok := prop.Resolved().Extensions["x-go-name"]; ok {
			var goName string
			if json.Unmarshal(v, &goName) == nil && goName != "" {
				if sanitized := sanitizeIdentifier(goName); sanitized != "" {
					fieldName = sanitized
				}
			}
		}
		fieldNames[fieldName]++
	}

	usedFields := make(map[string]int)
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
				if sanitized := sanitizeIdentifier(goName); sanitized != "" {
					fieldName = sanitized
				}
			}
		}

		// Resolve field name collision by appending a numeric suffix.
		if fieldNames[fieldName] > 1 {
			usedFields[fieldName]++
			if usedFields[fieldName] > 1 {
				fieldName = fmt.Sprintf("%s%d", fieldName, usedFields[fieldName])
			}
		}

		goType := g.GoType(resolved, typeName+ToPascalCase(propName))
		req := isRequired(propName, s.Required)
		nullable := isNullable(resolved)
		wrapped := wrapNullableOptional(goType, req, nullable)

		// JSON tag (sanitize to prevent struct tag injection).
		tag := sanitizeTagValue(propName)
		if !req {
			tag += ",omitzero"
		}

		// readOnly/writeOnly comment.
		var rwComment string
		if resolved.ReadOnly {
			rwComment = " // read-only"
		} else if resolved.WriteOnly {
			rwComment = " // write-only"
		}

		fmt.Fprintf(w, "\t%s %s `json:\"%s\"`%s\n", fieldName, wrapped, tag, rwComment)
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
		// Use PropertyOrder if available; otherwise sort keys for determinism.
		order := sub.PropertyOrder
		if len(order) == 0 {
			for name := range sub.Properties {
				order = append(order, name)
			}
			slices.Sort(order)
		}
		for _, propName := range order {
			prop, ok := sub.Properties[propName]
			if !ok {
				continue
			}
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

// hasReadWriteProps reports whether any property of a schema is readOnly or writeOnly.
func hasReadWriteProps(s *spec.Schema) (hasRead, hasWrite bool) {
	if s == nil {
		return false, false
	}
	for _, prop := range s.Properties {
		if prop == nil {
			continue
		}
		resolved := prop.Resolved()
		if resolved.ReadOnly {
			hasRead = true
		}
		if resolved.WriteOnly {
			hasWrite = true
		}
		if hasRead && hasWrite {
			return
		}
	}
	return
}

// emitReadWriteVariants generates Request/Response struct variants for schemas
// that have readOnly or writeOnly properties, plus conversion methods.
func (g *Generator) emitReadWriteVariants(w *strings.Builder, typeName string, s *spec.Schema) {
	hasRead, hasWrite := hasReadWriteProps(s)
	if !hasRead && !hasWrite {
		return
	}

	propOrder := s.PropertyOrder
	if len(propOrder) == 0 {
		for name := range s.Properties {
			propOrder = append(propOrder, name)
		}
		slices.Sort(propOrder)
	}

	// Request type: excludes readOnly fields.
	if hasRead {
		reqTypeName := typeName + "Request"
		fmt.Fprintf(w, "// %s is %s without read-only fields (for request bodies).\ntype %s struct {\n", reqTypeName, typeName, reqTypeName)
		for _, propName := range propOrder {
			prop := s.Properties[propName]
			if prop == nil {
				continue
			}
			resolved := prop.Resolved()
			if resolved.ReadOnly {
				continue // skip readOnly
			}
			g.emitStructField(w, typeName, propName, resolved, s.Required)
		}
		fmt.Fprintf(w, "}\n\n")
	}

	// Response type: excludes writeOnly fields.
	if hasWrite {
		respTypeName := typeName + "Response"
		fmt.Fprintf(w, "// %s is %s without write-only fields (for response bodies).\ntype %s struct {\n", respTypeName, typeName, respTypeName)
		for _, propName := range propOrder {
			prop := s.Properties[propName]
			if prop == nil {
				continue
			}
			resolved := prop.Resolved()
			if resolved.WriteOnly {
				continue // skip writeOnly
			}
			g.emitStructField(w, typeName, propName, resolved, s.Required)
		}
		fmt.Fprintf(w, "}\n\n")
	}

	// ToRequest() conversion method.
	if hasRead {
		reqTypeName := typeName + "Request"
		fmt.Fprintf(w, "// ToRequest converts %s to %s, dropping read-only fields.\nfunc (v %s) ToRequest() %s {\n\treturn %s{\n", typeName, reqTypeName, typeName, reqTypeName, reqTypeName)
		for _, propName := range propOrder {
			prop := s.Properties[propName]
			if prop == nil {
				continue
			}
			resolved := prop.Resolved()
			if resolved.ReadOnly {
				continue
			}
			fieldName := g.resolveFieldName(resolved, propName)
			fmt.Fprintf(w, "\t\t%s: v.%s,\n", fieldName, fieldName)
		}
		fmt.Fprintf(w, "\t}\n}\n\n")
	}

	// ToResponse() conversion method.
	if hasWrite {
		respTypeName := typeName + "Response"
		fmt.Fprintf(w, "// ToResponse converts %s to %s, dropping write-only fields.\nfunc (v %s) ToResponse() %s {\n\treturn %s{\n", typeName, respTypeName, typeName, respTypeName, respTypeName)
		for _, propName := range propOrder {
			prop := s.Properties[propName]
			if prop == nil {
				continue
			}
			resolved := prop.Resolved()
			if resolved.WriteOnly {
				continue
			}
			fieldName := g.resolveFieldName(resolved, propName)
			fmt.Fprintf(w, "\t\t%s: v.%s,\n", fieldName, fieldName)
		}
		fmt.Fprintf(w, "\t}\n}\n\n")
	}
}

// emitStructField writes a single struct field (shared between base and variant types).
func (g *Generator) emitStructField(w *strings.Builder, typeName, propName string, resolved *spec.Schema, required []string) {
	fieldName := g.resolveFieldName(resolved, propName)
	goType := g.GoType(resolved, typeName+ToPascalCase(propName))
	req := isRequired(propName, required)
	nullable := isNullable(resolved)
	wrapped := wrapNullableOptional(goType, req, nullable)

	tag := sanitizeTagValue(propName)
	if !req {
		tag += ",omitzero"
	}

	fmt.Fprintf(w, "\t%s %s `json:\"%s\"`\n", fieldName, wrapped, tag)
}

// resolveFieldName returns the Go field name for a property.
func (g *Generator) resolveFieldName(resolved *spec.Schema, propName string) string {
	fieldName := ToFieldName(propName)
	if v, ok := resolved.Extensions["x-go-name"]; ok {
		var goName string
		if json.Unmarshal(v, &goName) == nil && goName != "" {
			if sanitized := sanitizeIdentifier(goName); sanitized != "" {
				fieldName = sanitized
			}
		}
	}
	return fieldName
}
