package generate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mkusaka/openapigo/internal/spec"
)

// Config holds generator configuration.
type Config struct {
	Input   string // path to OpenAPI spec
	Output  string // output directory
	Package string // Go package name

	// Optional flags
	SkipValidation      bool              // --skip-validation: skip Validate() method generation
	NoReadWriteTypes    bool              // --no-read-write-types: skip Request/Response variant generation
	DryRun              bool              // --dry-run: print file names/sizes without writing
	FormatMapping       map[string]string // --format-mapping: custom format→type mapping (e.g. "uuid=github.com/google/uuid.UUID")
	StrictEnums         bool              // --strict-enums: generate validation for non-string enums
	ValidateOnUnmarshal bool              // --validate-on-unmarshal: generate UnmarshalJSON that calls Validate()
}

// Generator holds state during code generation.
type Generator struct {
	config             Config
	doc                *spec.Document
	schemaNames        map[*spec.Schema]string // schema pointer → Go type name
	inlineSchemas      []namedSchema           // inline schemas to emit
	imports            map[string]bool         // import paths needed
	usedNames          map[string]int          // name → count for collision detection
	opNames            map[string]string       // operationID → Go name (cached, idempotent)
	usedOpNames        map[string]bool         // set of assigned operation Go names (O(1) lookup)
	bodyContentType    string                  // set during body type/field emission
	multipartTypeNames map[string]bool         // Go type names that need File for format:binary
	multipartNameCache map[string]string       // component schema name → generated multipart type name
	extraTypeDefs      []string                // extra type definitions (e.g., union body structs)
}

// Run executes the full generation pipeline.
func Run(cfg Config) error {
	// 1. Load and resolve spec (read file once).
	data, err := os.ReadFile(cfg.Input)
	if err != nil {
		return fmt.Errorf("read spec: %w", err)
	}
	fileExt := filepath.Ext(cfg.Input)
	doc, err := spec.Parse(data, fileExt)
	if err != nil {
		return err
	}
	if _, err := spec.DetectVersion(doc); err != nil {
		return err
	}
	if err := spec.Resolve(doc); err != nil {
		return err
	}

	// 2. Ensure output directory exists.
	if err := os.MkdirAll(cfg.Output, 0o755); err != nil {
		return err
	}

	g := &Generator{
		config:             cfg,
		doc:                doc,
		schemaNames:        make(map[*spec.Schema]string),
		imports:            make(map[string]bool),
		usedNames:          make(map[string]int),
		opNames:            make(map[string]string),
		usedOpNames:        make(map[string]bool),
		multipartTypeNames: make(map[string]bool),
		multipartNameCache: make(map[string]string),
	}

	// 3. Assign names to component schemas.
	g.assignSchemaNames()

	// 4. Parse vendor extensions for all schemas (reuse already-loaded data).
	g.parseExtensions(data, fileExt)

	// 5. Generate files.
	// Operations and endpoints are generated first so that inline schemas
	// (e.g., request body types) are discovered before types.go is emitted.
	source := filepath.Base(cfg.Input)

	opsCode := g.generateOperations(source)
	endpointsCode := g.generateEndpoints(source)
	typesCode := g.generateTypes(source)
	authCode := g.generateAuth(source)

	// 6. Write files atomically and clean up stale generated files.
	files := map[string]string{
		"types.go":      typesCode,
		"operations.go": opsCode,
		"endpoints.go":  endpointsCode,
	}
	if authCode != "" {
		files["auth.go"] = authCode
	}

	// Dry-run: just print file names and sizes.
	if cfg.DryRun {
		fileNames := make([]string, 0, len(files))
		for name := range files {
			fileNames = append(fileNames, name)
		}
		sort.Strings(fileNames)
		for _, name := range fileNames {
			fmt.Printf("%s/%s (%d bytes)\n", cfg.Output, name, len(files[name]))
		}
		return nil
	}

	// Clean stale generated files before writing new ones.
	currentFiles := make(map[string]bool, len(files))
	for name := range files {
		currentFiles[name] = true
	}
	if err := cleanStaleFiles(cfg.Output, currentFiles); err != nil {
		return fmt.Errorf("clean stale files: %w", err)
	}

	for name, code := range files {
		path := filepath.Join(cfg.Output, name)
		if err := writeFileAtomic(path, []byte(code)); err != nil {
			return err
		}
	}

	return nil
}

// assignSchemaNames maps component schemas to Go type names.
func (g *Generator) assignSchemaNames() {
	if g.doc.Components == nil {
		return
	}
	// Sort schema names for deterministic output.
	names := make([]string, 0, len(g.doc.Components.Schemas))
	for name := range g.doc.Components.Schemas {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		s := g.doc.Components.Schemas[name]
		goName := ToPascalCase(name)
		goName = g.uniqueName(goName)
		g.schemaNames[s] = goName
	}
}

// parseExtensions extracts vendor extensions from raw YAML/JSON into the Extensions map.
// This is needed because the Extensions field is tagged json:"-" and not populated by
// standard unmarshalling. The data and fileExt are passed from Run to avoid re-reading
// the spec file.
func (g *Generator) parseExtensions(data []byte, fileExt string) {
	ext := strings.ToLower(fileExt)
	if ext == ".yaml" || ext == ".yml" {
		// Convert YAML to JSON so we can use the same extension parsing code.
		var err error
		data, err = spec.YAMLToJSON(data)
		if err != nil {
			return
		}
	}

	var raw struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}

	if g.doc.Components != nil {
		for name, schemaData := range raw.Components.Schemas {
			s, ok := g.doc.Components.Schemas[name]
			if !ok || s == nil {
				continue
			}
			parseSchemaExtensions(s, schemaData)
		}
	}
}

func parseSchemaExtensions(s *spec.Schema, data json.RawMessage) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	for k, v := range raw {
		if strings.HasPrefix(k, "x-") {
			if s.Extensions == nil {
				s.Extensions = make(map[string]json.RawMessage)
			}
			s.Extensions[k] = v
		}
	}
	// Recurse into properties.
	if propsData, ok := raw["properties"]; ok {
		var props map[string]json.RawMessage
		if err := json.Unmarshal(propsData, &props); err == nil {
			for propName, propData := range props {
				if prop, ok := s.Properties[propName]; ok && prop != nil {
					parseSchemaExtensions(prop, propData)
				}
			}
		}
	}
	// Recurse into items.
	if itemsData, ok := raw["items"]; ok && s.Items != nil {
		parseSchemaExtensions(s.Items, itemsData)
	}
	// Recurse into composition.
	for _, key := range []string{"allOf", "oneOf", "anyOf"} {
		arrData, ok := raw[key]
		if !ok {
			continue
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(arrData, &arr); err != nil {
			continue
		}
		var schemas []*spec.Schema
		switch key {
		case "allOf":
			schemas = s.AllOf
		case "oneOf":
			schemas = s.OneOf
		case "anyOf":
			schemas = s.AnyOf
		}
		for i, elemData := range arr {
			if i < len(schemas) && schemas[i] != nil {
				parseSchemaExtensions(schemas[i], elemData)
			}
		}
	}
	// Recurse into additionalProperties.
	if apData, ok := raw["additionalProperties"]; ok && s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil {
		parseSchemaExtensions(s.AdditionalProperties.Schema, apData)
	}
}

// uniqueName returns a unique Go type name, adding numeric suffix on collision.
func (g *Generator) uniqueName(name string) string {
	name = SafeName(name)
	g.usedNames[name]++
	if g.usedNames[name] > 1 {
		return fmt.Sprintf("%s%d", name, g.usedNames[name])
	}
	return name
}

// generateTypes produces the types.go file content.
func (g *Generator) generateTypes(source string) string {
	var w strings.Builder
	w.WriteString(fileHeader(g.config.Package, source))
	w.WriteString("\n")

	// Collect imports.
	g.imports = make(map[string]bool)
	g.imports["github.com/mkusaka/openapigo"] = true // always needed for compat check

	// Pre-scan for needed imports.
	var typeDefs strings.Builder

	// Emit compat check.
	typeDefs.WriteString("// Ensures generated code is compatible with the runtime library.\nvar _ = openapigo.RuntimeCompatV0_1\n\n")

	// Sort component schemas by name.
	var names []string
	if g.doc.Components != nil {
		names = make([]string, 0, len(g.doc.Components.Schemas))
		for name := range g.doc.Components.Schemas {
			names = append(names, name)
		}
		sort.Strings(names)
	}

	// Emit component schemas.
	type schemaEntry struct {
		name   string
		schema *spec.Schema
	}
	var allSchemas []schemaEntry
	for _, name := range names {
		s := g.doc.Components.Schemas[name]
		goName := g.schemaNames[s]
		g.emitSchemaType(&typeDefs, goName, s)
		// Emit Request/Response variants for schemas with readOnly/writeOnly.
		if !g.config.NoReadWriteTypes {
			if resolved := s.Resolved(); resolved != nil && (resolved.Type == "object" || len(resolved.Properties) > 0) {
				g.emitReadWriteVariants(&typeDefs, goName, resolved)
			}
		}
		allSchemas = append(allSchemas, schemaEntry{goName, s})
	}

	// Emit inline schemas that were discovered during type generation.
	emitted := make(map[string]bool)
	if g.doc.Components != nil {
		for _, ns := range names {
			emitted[g.schemaNames[g.doc.Components.Schemas[ns]]] = true
		}
	}
	// Process inline schemas iteratively (they may generate more inline schemas).
	for i := 0; i < len(g.inlineSchemas); i++ {
		ns := g.inlineSchemas[i]
		if emitted[ns.name] {
			continue
		}
		emitted[ns.name] = true
		g.emitSchemaType(&typeDefs, ns.name, ns.schema)
		// Emit Request/Response variants for inline schemas too.
		if !g.config.NoReadWriteTypes {
			if resolved := ns.schema.Resolved(); resolved != nil && (resolved.Type == "object" || len(resolved.Properties) > 0) {
				g.emitReadWriteVariants(&typeDefs, ns.name, resolved)
			}
		}
		allSchemas = append(allSchemas, schemaEntry{ns.name, ns.schema})
	}

	// Emit pattern variables and Validate() methods.
	if !g.config.SkipValidation {
		for _, entry := range allSchemas {
			resolved := entry.schema.Resolved()
			if resolved != nil {
				g.emitPatternVars(&typeDefs, entry.name, resolved)
				g.emitValidateMethod(&typeDefs, entry.name, resolved)
			}
		}
	}

	// Build imports.
	w.WriteString("import (\n")
	importPaths := make([]string, 0, len(g.imports))
	for imp := range g.imports {
		importPaths = append(importPaths, imp)
	}
	sort.Strings(importPaths)
	for _, imp := range importPaths {
		fmt.Fprintf(&w, "\t%q\n", imp)
	}
	w.WriteString(")\n\n")

	w.WriteString(typeDefs.String())
	return w.String()
}

// emitSchemaType writes a type definition for a schema.
func (g *Generator) emitSchemaType(w *strings.Builder, goName string, s *spec.Schema) {
	if s == nil {
		return
	}
	s = s.Resolved()

	// Set multipart context so stringType maps format:binary to File.
	if g.multipartTypeNames[goName] {
		g.bodyContentType = "multipart/form-data"
		defer func() { g.bodyContentType = "" }()
	}

	// allOf composition.
	if len(s.AllOf) > 0 {
		g.emitAllOfType(w, goName, s)
		return
	}

	// oneOf/anyOf — emit type alias to any.
	if len(s.OneOf) > 0 || len(s.AnyOf) > 0 {
		fmt.Fprintf(w, "// %s is a union type (oneOf/anyOf).\ntype %s = any\n\n", goName, goName)
		return
	}

	// Enum type.
	if len(s.Enum) > 0 && s.Type == "string" {
		emitEnumType(w, goName, s)
		return
	}

	// Object type.
	if s.Type == "object" || (s.Type == "" && len(s.Properties) > 0) {
		g.emitStructType(w, goName, s)
		return
	}

	// Array type alias.
	if s.Type == "array" && s.Items != nil {
		elemType := g.GoType(s.Items, goName+"Item")
		fmt.Fprintf(w, "// %s is a list type.\ntype %s = []%s\n\n", goName, goName, elemType)
		return
	}

	// Simple type alias.
	goType := g.goTypeInner(s, goName)
	if goType != "any" {
		fmt.Fprintf(w, "// %s is a type alias.\ntype %s = %s\n\n", goName, goName, goType)
	}
}

// emitAllOfType handles allOf composition schemas.
func (g *Generator) emitAllOfType(w *strings.Builder, goName string, s *spec.Schema) {
	resolved := make([]*spec.Schema, 0, len(s.AllOf))
	for _, sub := range s.AllOf {
		if sub != nil {
			resolved = append(resolved, sub.Resolved())
		}
	}
	if len(resolved) == 0 {
		return
	}

	// Check if any sub-schemas have properties (object-like).
	hasObject := false
	for _, sub := range resolved {
		if sub.Type == "object" || len(sub.Properties) > 0 {
			hasObject = true
			break
		}
	}

	if hasObject {
		// Merge all properties and emit struct.
		merged := g.mergeAllOfProperties(resolved)
		g.emitStructType(w, goName, merged)
		return
	}

	// All primitives of the same type → type alias.
	if resolved[0].Type != "" {
		best := resolved[0]
		for _, sub := range resolved[1:] {
			if sub.Format != "" && best.Format == "" {
				best = sub
			}
		}
		goType := g.goTypeInner(best, goName)
		if goType != "any" {
			fmt.Fprintf(w, "// %s is a type alias.\ntype %s = %s\n\n", goName, goName, goType)
		}
	}
}

// generateOperations produces the operations.go file content.
func (g *Generator) generateOperations(source string) string {
	var w strings.Builder
	w.WriteString(fileHeader(g.config.Package, source))
	w.WriteString("\n")

	var opDefs strings.Builder
	opImports := map[string]bool{}

	// Iterate paths in order.
	pathOrder := g.doc.PathOrder
	if len(pathOrder) == 0 {
		for p := range g.doc.Paths {
			pathOrder = append(pathOrder, p)
		}
		sort.Strings(pathOrder)
	}

	for _, path := range pathOrder {
		pi := g.doc.Paths[path]
		if pi == nil {
			continue
		}
		// Merge path-level params with operation-level params.
		for _, op := range pi.Operations() {
			g.emitOperation(&opDefs, opImports, path, op.Method, op.Operation, pi.Parameters)
		}
	}

	// Write imports (only if non-empty).
	if len(opImports) > 0 {
		w.WriteString("import (\n")
		importPaths := make([]string, 0, len(opImports))
		for imp := range opImports {
			importPaths = append(importPaths, imp)
		}
		sort.Strings(importPaths)
		for _, imp := range importPaths {
			fmt.Fprintf(&w, "\t%q\n", imp)
		}
		w.WriteString(")\n\n")
	}

	// Write extra type definitions (e.g., union body structs).
	for _, def := range g.extraTypeDefs {
		w.WriteString(def)
	}

	w.WriteString(opDefs.String())
	return w.String()
}

// opNameFor returns the Go name for an operation, avoiding collisions with schema names
// and other operation names. Results are cached so the same operationID always returns
// the same Go name across generateOperations and generateEndpoints.
func (g *Generator) opNameFor(op *spec.Operation) string {
	if cached, ok := g.opNames[op.OperationID]; ok {
		return cached
	}
	opName := ToPascalCase(op.OperationID)
	// Deduplicate against schema names and other operations.
	if g.nameCollides(opName) {
		opName += "Op"
	}
	base := opName
	for n := 2; g.nameCollides(opName); n++ {
		opName = fmt.Sprintf("%s%d", base, n)
	}
	g.opNames[op.OperationID] = opName
	g.usedOpNames[opName] = true
	return opName
}

// nameCollides reports whether name is already used by a schema or operation.
func (g *Generator) nameCollides(name string) bool {
	if _, ok := g.usedNames[name]; ok {
		return true
	}
	return g.usedOpNames[name]
}

// emitOperation writes request/response structs for an operation.
func (g *Generator) emitOperation(w *strings.Builder, imports map[string]bool, path, method string, op *spec.Operation, pathParams []*spec.ParameterOrRef) {
	if op == nil || op.OperationID == "" {
		return
	}

	opName := g.opNameFor(op)

	// Collect all parameters (path-level + operation-level).
	// Per OAS spec, operation-level params override path-level params with same name+in.
	allParams := mergeParams(pathParams, op.Parameters)

	hasParams := len(allParams) > 0
	hasBody := op.RequestBody != nil && op.RequestBody.RequestBody != nil

	// --- Params struct ---
	if hasParams || hasBody {
		fmt.Fprintf(w, "// %sParams is the request parameters for %s.\ntype %sParams struct {\n", opName, sanitizeComment(op.OperationID), opName)

		// Track used field names to disambiguate same-name params across locations.
		usedFieldNames := make(map[string]bool)

		for _, p := range allParams {
			if p == nil || p.Parameter == nil {
				continue
			}
			param := p.Parameter
			// Validate param.In against whitelist to prevent struct tag injection.
			paramIn := param.In
			switch paramIn {
			case "path", "query", "header", "cookie":
				// valid
			default:
				continue
			}
			fieldName := ToFieldName(param.Name)
			if usedFieldNames[fieldName] {
				// Disambiguate by appending location suffix (e.g., IDQuery, IDPath).
				fieldName = ToFieldName(param.Name) + ToPascalCase(paramIn)
			}
			// If still colliding (e.g., "id_query" query + "id" query both → IDQuery),
			// append a numeric suffix.
			base := fieldName
			for n := 2; usedFieldNames[fieldName]; n++ {
				fieldName = fmt.Sprintf("%s%d", base, n)
			}
			usedFieldNames[fieldName] = true
			goType := "string"
			if param.Schema != nil {
				goType = g.GoType(param.Schema.Resolved(), opName+fieldName)
				g.collectImports(imports, param.Schema.Resolved())
			}
			if !param.Required {
				goType = "*" + goType
			}
			tag := buildParamTag(param)
			fmt.Fprintf(w, "\t%s %s `%s:\"%s\"`\n", fieldName, goType, paramIn, tag)
		}

		if hasBody {
			entries := g.requestBodyInfoAll(op.RequestBody.RequestBody)
			if len(entries) > 1 {
				// Multiple media types — generate a union body struct.
				unionName := g.uniqueName(opName + "RequestBody")
				var unionDef strings.Builder
				fmt.Fprintf(&unionDef, "// %s supports multiple media types for the request body.\ntype %s struct {\n", unionName, unionName)
				for _, entry := range entries {
					fieldName := contentTypeFieldName(entry.contentType)
					goType := g.bodyTypeForEntry(entry, opName, fieldName, imports)
					// Interface types (io.Reader) are already nil-able; don't wrap in pointer.
					if goType == "io.Reader" {
						fmt.Fprintf(&unionDef, "\t%s %s `body:\"%s\"`\n", fieldName, goType, sanitizeTagValue(entry.contentType))
					} else {
						fmt.Fprintf(&unionDef, "\t%s *%s `body:\"%s\"`\n", fieldName, goType, sanitizeTagValue(entry.contentType))
					}
				}
				fmt.Fprintf(&unionDef, "}\n\n")
				// Register the union struct as an inline type definition.
				g.extraTypeDefs = append(g.extraTypeDefs, unionDef.String())
				if op.RequestBody.RequestBody.Required {
					fmt.Fprintf(w, "\tBody %s `body:\"*\"`\n", unionName)
				} else {
					fmt.Fprintf(w, "\tBody *%s `body:\"*,omitzero\"`\n", unionName)
				}
			} else if len(entries) == 1 {
				// Single media type — existing behavior.
				entry := entries[0]
				g.emitSingleBodyField(w, entry, opName, imports, op.RequestBody.RequestBody.Required)
			}
		}

		fmt.Fprintf(w, "}\n\n")
	}

	// --- Response type ---
	// Discover the type name (to register inline schemas) but do NOT collect
	// imports here — operations.go defines param structs only. Response types
	// are referenced in endpoints.go, which collects its own imports.
	successSchema := g.findSuccessResponseSchema(op)
	if successSchema != nil {
		_ = g.GoType(successSchema.Resolved(), opName+"Response")
	}

	// --- Error types ---
	// Same: discover type names without collecting imports.
	for code, resp := range op.Responses {
		if code == "default" || (len(code) == 3 && code[0] >= '4') {
			if resp.Response != nil {
				errSchema := g.responseSchema(resp.Response)
				if errSchema != nil {
					errName := opName + codeToName(code) + "Error"
					_ = g.GoType(errSchema.Resolved(), errName)
				}
			}
		}
	}
}

// contentTypeEntry holds a schema and content type pair.
type contentTypeEntry struct {
	schema      *spec.Schema
	contentType string
}

// requestBodyInfoAll returns all content types with schemas for a request body,
// in a deterministic order (json first, then known types, then alphabetical).
func (g *Generator) requestBodyInfoAll(rb *spec.RequestBody) []contentTypeEntry {
	if rb == nil || rb.Content == nil {
		return nil
	}
	var entries []contentTypeEntry
	seen := make(map[string]bool)
	// schemalessTypes are content types that can be emitted without a schema
	// (they map to a fixed Go type like io.Reader or string).
	schemalessTypes := map[string]bool{
		"application/octet-stream": true,
		"text/plain":               true,
	}

	// Preferred order.
	for _, ct := range []string{
		"application/json",
		"multipart/form-data",
		"application/x-www-form-urlencoded",
		"application/octet-stream",
		"text/plain",
	} {
		mt, ok := rb.Content[ct]
		if !ok || mt == nil {
			continue
		}
		if mt.Schema != nil || schemalessTypes[ct] {
			entries = append(entries, contentTypeEntry{schema: mt.Schema, contentType: ct})
			seen[ct] = true
		}
	}
	// Remaining content types in alphabetical order.
	var remaining []string
	for ct := range rb.Content {
		if !seen[ct] {
			remaining = append(remaining, ct)
		}
	}
	sort.Strings(remaining)
	for _, ct := range remaining {
		mt := rb.Content[ct]
		if mt != nil && (mt.Schema != nil || schemalessTypes[ct]) {
			entries = append(entries, contentTypeEntry{schema: mt.Schema, contentType: ct})
		}
	}
	return entries
}

// contentTypeFieldName returns a Go field name for a content type.
func contentTypeFieldName(ct string) string {
	switch ct {
	case "application/json":
		return "JSON"
	case "multipart/form-data":
		return "Multipart"
	case "application/x-www-form-urlencoded":
		return "Form"
	case "application/octet-stream":
		return "OctetStream"
	case "text/plain":
		return "Text"
	default:
		return ToPascalCase(ct)
	}
}

// requestBodyInfo returns the schema and content type for a request body.
// Prefers application/json; falls back to other content types.
func (g *Generator) requestBodyInfo(rb *spec.RequestBody) (*spec.Schema, string) {
	if rb == nil || rb.Content == nil {
		return nil, ""
	}
	// Prefer application/json.
	if mt, ok := rb.Content["application/json"]; ok && mt != nil && mt.Schema != nil {
		return mt.Schema, "application/json"
	}
	// Fall back: try known content types in preference order.
	for _, ct := range []string{"multipart/form-data", "application/x-www-form-urlencoded", "application/octet-stream"} {
		if mt, ok := rb.Content[ct]; ok && mt != nil && mt.Schema != nil {
			return mt.Schema, ct
		}
	}
	// Last resort: pick any content type with a schema.
	var cts []string
	for ct := range rb.Content {
		cts = append(cts, ct)
	}
	sort.Strings(cts) // deterministic order
	for _, ct := range cts {
		mt := rb.Content[ct]
		if mt != nil && mt.Schema != nil {
			return mt.Schema, ct
		}
	}
	return nil, ""
}

// bodyTypeForEntry returns the Go type string for a single content type entry.
// Used both for single-body and union-body code paths.
func (g *Generator) bodyTypeForEntry(entry contentTypeEntry, opName, fieldName string, imports map[string]bool) string {
	switch entry.contentType {
	case "text/plain":
		return "string"
	case "application/octet-stream":
		imports["io"] = true
		return "io.Reader"
	default:
		if entry.schema != nil {
			goType := g.GoTypeForBody(entry.schema, opName+fieldName+"Body", entry.contentType)
			g.collectImports(imports, entry.schema.Resolved())
			return goType
		}
		return "any"
	}
}

// emitSingleBodyField writes a single body field to the param struct.
func (g *Generator) emitSingleBodyField(w *strings.Builder, entry contentTypeEntry, opName string, imports map[string]bool, required bool) {
	switch entry.contentType {
	case "text/plain":
		goType := "string"
		if !required {
			goType = "*string"
		}
		fmt.Fprintf(w, "\tBody %s `body:\"%s\"`\n", goType, sanitizeTagValue(entry.contentType))
	case "application/octet-stream":
		imports["io"] = true
		fmt.Fprintf(w, "\tBody io.Reader `body:\"%s\"`\n", sanitizeTagValue(entry.contentType))
	default:
		if entry.schema != nil {
			goType := g.GoTypeForBody(entry.schema, opName+"Body", entry.contentType)
			g.collectImports(imports, entry.schema.Resolved())
			if !required {
				goType = "*" + goType
			}
			fmt.Fprintf(w, "\tBody %s `body:\"%s\"`\n", goType, sanitizeTagValue(entry.contentType))
		}
	}
}

func (g *Generator) findSuccessResponseSchema(op *spec.Operation) *spec.Schema {
	// Check all numeric 2xx status codes first, preferring lower codes.
	var codes []string
	for code := range op.Responses {
		if len(code) == 3 && code[0] == '2' && code[1] >= '0' && code[1] <= '9' && code[2] >= '0' && code[2] <= '9' {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	for _, code := range codes {
		if s := g.responseSchema(op.Responses[code].Response); s != nil {
			return s
		}
	}
	// Fall back to 2XX wildcard.
	if resp, ok := op.Responses["2XX"]; ok {
		return g.responseSchema(resp.Response)
	}
	return nil
}

func (g *Generator) responseSchema(resp *spec.Response) *spec.Schema {
	if resp == nil || resp.Content == nil {
		return nil
	}
	// Prefer application/json.
	if mt, ok := resp.Content["application/json"]; ok && mt != nil && mt.Schema != nil {
		return mt.Schema
	}
	// Fall back: pick any content type with a schema (deterministic order).
	var cts []string
	for ct := range resp.Content {
		cts = append(cts, ct)
	}
	sort.Strings(cts)
	for _, ct := range cts {
		mt := resp.Content[ct]
		if mt != nil && mt.Schema != nil {
			return mt.Schema
		}
	}
	return nil
}

// mergeParams merges path-level and operation-level parameters.
// Per the OAS spec, operation-level parameters override path-level parameters
// with the same name and location (in).
func mergeParams(pathParams, opParams []*spec.ParameterOrRef) []*spec.ParameterOrRef {
	// Build a set of operation-level param keys (name + in).
	opKeys := make(map[string]bool)
	for _, p := range opParams {
		if p != nil && p.Parameter != nil {
			opKeys[p.Parameter.Name+"|"+p.Parameter.In] = true
		}
	}
	// Start with path-level params, skip those overridden by operation-level.
	var merged []*spec.ParameterOrRef
	for _, p := range pathParams {
		if p != nil && p.Parameter != nil {
			if opKeys[p.Parameter.Name+"|"+p.Parameter.In] {
				continue // overridden by operation-level param
			}
		}
		merged = append(merged, p)
	}
	merged = append(merged, opParams...)
	return merged
}

func (g *Generator) collectImports(imports map[string]bool, s *spec.Schema) {
	if s == nil {
		return
	}
	// Named component schema — its transitive imports are in types.go, not here.
	if _, ok := g.schemaNames[s]; ok {
		return
	}
	switch {
	case s.Type == "string" && s.Format == "date-time":
		imports["time"] = true
	case s.Type == "string" && s.Format == "date":
		imports["github.com/mkusaka/openapigo"] = true
	case s.Type == "array" && s.Items != nil:
		g.collectImports(imports, s.Items.Resolved())
	}
}

func codeToName(code string) string {
	switch code {
	case "default":
		return "Default"
	default:
		return code
	}
}

// generateEndpoints produces the endpoints.go file content.
func (g *Generator) generateEndpoints(source string) string {
	var w strings.Builder
	w.WriteString(fileHeader(g.config.Package, source))
	w.WriteString("\n")

	imports := map[string]bool{}

	var epDefs strings.Builder

	pathOrder := g.doc.PathOrder
	if len(pathOrder) == 0 {
		for p := range g.doc.Paths {
			pathOrder = append(pathOrder, p)
		}
		sort.Strings(pathOrder)
	}

	for _, path := range pathOrder {
		pi := g.doc.Paths[path]
		if pi == nil {
			continue
		}
		for _, op := range pi.Operations() {
			g.emitEndpoint(&epDefs, imports, path, op.Method, op.Operation, pi.Parameters)
		}
	}

	// Only add openapigo import if endpoints were actually emitted.
	if epDefs.Len() > 0 {
		imports["github.com/mkusaka/openapigo"] = true
	}

	// Write imports (only if non-empty).
	if len(imports) > 0 {
		w.WriteString("import (\n")
		importPaths := make([]string, 0, len(imports))
		for imp := range imports {
			importPaths = append(importPaths, imp)
		}
		sort.Strings(importPaths)
		for _, imp := range importPaths {
			fmt.Fprintf(&w, "\t%q\n", imp)
		}
		w.WriteString(")\n\n")
	}

	w.WriteString(epDefs.String())
	return w.String()
}

// emitEndpoint writes a single Endpoint variable declaration.
func (g *Generator) emitEndpoint(w *strings.Builder, imports map[string]bool, path, method string, op *spec.Operation, pathParams []*spec.ParameterOrRef) {
	if op == nil || op.OperationID == "" {
		return
	}

	opName := g.opNameFor(op)

	// Determine request type.
	allParams := mergeParams(pathParams, op.Parameters)
	hasParams := len(allParams) > 0
	hasBody := op.RequestBody != nil && op.RequestBody.RequestBody != nil
	reqType := "openapigo.NoRequest"
	if hasParams || hasBody {
		reqType = opName + "Params"
	}

	// Determine response type.
	respType := "openapigo.NoContent"
	successSchema := g.findSuccessResponseSchema(op)
	if successSchema != nil {
		respType = g.GoType(successSchema.Resolved(), opName+"Response")
		g.collectImports(imports, successSchema.Resolved())
	}

	// Find success codes. Numeric codes are emitted directly; "2XX" wildcard
	// is emitted as -2 (range code, matching 200-299 in the runtime).
	var successCodes []string
	has2XX := false
	for code := range op.Responses {
		if len(code) == 3 && code[0] == '2' && code[1] >= '0' && code[1] <= '9' && code[2] >= '0' && code[2] <= '9' {
			successCodes = append(successCodes, code)
		} else if code == "2XX" {
			has2XX = true
		}
	}
	sort.Strings(successCodes)
	if has2XX {
		successCodes = append(successCodes, "-2") // range code for 2XX
	}
	if len(successCodes) == 0 {
		successCodes = []string{"200"}
	}

	fmt.Fprintf(w, "// %s is the endpoint for %s %s.\n", opName, sanitizeComment(method), sanitizeComment(path))
	if op.Summary != "" {
		fmt.Fprintf(w, "// %s\n", sanitizeComment(op.Summary))
	}
	if op.Deprecated {
		fmt.Fprintf(w, "//\n// Deprecated: this operation is deprecated.\n")
	}
	fmt.Fprintf(w, "var %s = openapigo.NewEndpoint[%s, %s](%q, %q)", opName, reqType, respType, method, path)

	// Success codes.
	if len(successCodes) > 0 && !(len(successCodes) == 1 && successCodes[0] == "200") {
		fmt.Fprintf(w, ".\n\tWithSuccessCodes(")
		for i, code := range successCodes {
			if i > 0 {
				w.WriteString(", ")
			}
			w.WriteString(code)
		}
		w.WriteString(")")
	}

	// Error handlers.
	errorHandlers := g.collectErrorHandlers(op, opName, imports)
	if len(errorHandlers) > 0 {
		imports["net/http"] = true
		w.WriteString(".\n\tWithErrors(\n")
		for _, eh := range errorHandlers {
			fmt.Fprintf(w, "\t\topenapigo.ErrorHandler{StatusCode: %d, Parse: func(code int, header http.Header, body []byte) error {\n", eh.statusCode)
			fmt.Fprintf(w, "\t\t\treturn &openapigo.APIError{StatusCode: code, Status: http.StatusText(code), Header: header, Body: body}\n")
			fmt.Fprintf(w, "\t\t}},\n")
		}
		w.WriteString("\t)")
	}

	w.WriteString("\n\n")
}

// errorHandlerInfo holds metadata for generating an error handler.
type errorHandlerInfo struct {
	statusCode int // 0 = default, negative = range
	goType     string
}

// collectErrorHandlers builds the list of error handlers for an endpoint.
func (g *Generator) collectErrorHandlers(op *spec.Operation, opName string, imports map[string]bool) []errorHandlerInfo {
	var handlers []errorHandlerInfo

	// Collect error codes in deterministic order.
	var errorCodes []string
	for code := range op.Responses {
		if code == "default" || (len(code) == 3 && code[0] >= '4') || (len(code) == 3 && code[0] == '5') {
			errorCodes = append(errorCodes, code)
		}
	}
	sort.Strings(errorCodes)

	for _, code := range errorCodes {
		resp := op.Responses[code]
		if resp == nil || resp.Response == nil {
			continue
		}
		errSchema := g.responseSchema(resp.Response)
		if errSchema == nil {
			continue
		}
		errName := opName + codeToName(code) + "Error"
		goType := g.GoType(errSchema.Resolved(), errName)
		// Note: error handler bodies in endpoints.go only use openapigo.APIError and
		// http.Header, so we do NOT collect schema-level imports here.

		var statusCode int
		if code == "default" {
			statusCode = 0
		} else if len(code) == 3 && (code[1] == 'X' || code[1] == 'x') && (code[2] == 'X' || code[2] == 'x') {
			// Range code like "4XX", "5XX" → negative prefix: -4, -5
			statusCode = -int(code[0] - '0')
		} else {
			fmt.Sscanf(code, "%d", &statusCode)
		}
		handlers = append(handlers, errorHandlerInfo{statusCode: statusCode, goType: goType})
	}
	return handlers
}

// defaultParamStyle returns the default serialization style for a parameter location.
func defaultParamStyle(in string) string {
	switch in {
	case "path":
		return "simple"
	case "query", "cookie":
		return "form"
	case "header":
		return "simple"
	default:
		return ""
	}
}

// defaultParamExplode returns the default explode value for a parameter location.
func defaultParamExplode(in string) bool {
	return in == "query" || in == "cookie"
}

// buildParamTag builds the struct tag value for a parameter,
// including style and explode options when they differ from defaults.
func buildParamTag(param *spec.Parameter) string {
	tag := sanitizeTagValue(param.Name)

	style := param.Style
	if style == "" {
		style = defaultParamStyle(param.In)
	}
	defStyle := defaultParamStyle(param.In)

	explode := defaultParamExplode(param.In)
	if param.Explode != nil {
		explode = *param.Explode
	}
	defExplode := defaultParamExplode(param.In)

	// Only add style if it differs from the default.
	if style != defStyle {
		tag += "," + sanitizeTagValue(style)
	}

	// Only add explode option if it differs from the default.
	if explode != defExplode {
		if explode {
			tag += ",explode"
		} else {
			tag += ",noexplode"
		}
	}

	return tag
}
