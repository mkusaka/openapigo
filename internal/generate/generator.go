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
}

// Generator holds state during code generation.
type Generator struct {
	config        Config
	doc           *spec.Document
	schemaNames   map[*spec.Schema]string // schema pointer → Go type name
	inlineSchemas []namedSchema           // inline schemas to emit
	imports       map[string]bool         // import paths needed
	usedNames     map[string]int          // name → count for collision detection
}

// Run executes the full generation pipeline.
func Run(cfg Config) error {
	// 1. Load and resolve spec.
	doc, err := spec.Load(cfg.Input)
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
		config:      cfg,
		doc:         doc,
		schemaNames: make(map[*spec.Schema]string),
		imports:     make(map[string]bool),
		usedNames:   make(map[string]int),
	}

	// 3. Assign names to component schemas.
	g.assignSchemaNames()

	// 4. Parse vendor extensions for all schemas.
	g.parseExtensions()

	// 5. Generate files.
	// Operations and endpoints are generated first so that inline schemas
	// (e.g., request body types) are discovered before types.go is emitted.
	source := filepath.Base(cfg.Input)

	opsCode := g.generateOperations(source)
	endpointsCode := g.generateEndpoints(source)
	typesCode := g.generateTypes(source)
	authCode := g.generateAuth(source)

	// 6. Write files.
	files := map[string]string{
		"types.go":      typesCode,
		"operations.go": opsCode,
		"endpoints.go":  endpointsCode,
	}
	if authCode != "" {
		files["auth.go"] = authCode
	}
	for name, code := range files {
		path := filepath.Join(cfg.Output, name)
		if err := writeFile(path, []byte(code)); err != nil {
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
// standard unmarshalling.
func (g *Generator) parseExtensions() {
	data, err := os.ReadFile(g.config.Input)
	if err != nil {
		return
	}
	ext := strings.ToLower(filepath.Ext(g.config.Input))
	if ext == ".yaml" || ext == ".yml" {
		// Convert YAML to JSON so we can use the same extension parsing code.
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
	for _, name := range names {
		s := g.doc.Components.Schemas[name]
		goName := g.schemaNames[s]
		g.emitSchemaType(&typeDefs, goName, s)
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

	w.WriteString(opDefs.String())
	return w.String()
}

// opNameFor returns the Go name for an operation, avoiding collisions with schema names.
func (g *Generator) opNameFor(op *spec.Operation) string {
	opName := ToPascalCase(op.OperationID)
	if _, collision := g.usedNames[opName]; collision {
		opName += "Op"
	}
	return opName
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
		fmt.Fprintf(w, "// %sParams is the request parameters for %s.\ntype %sParams struct {\n", opName, op.OperationID, opName)

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
			tag := sanitizeTagValue(param.Name)
			if param.Explode != nil && !*param.Explode {
				tag += ",noexplode"
			}
			fmt.Fprintf(w, "\t%s %s `%s:\"%s\"`\n", fieldName, goType, paramIn, tag)
		}

		if hasBody {
			bodySchema := g.requestBodySchema(op.RequestBody.RequestBody)
			if bodySchema != nil {
				goType := g.GoType(bodySchema.Resolved(), opName+"Body")
				g.collectImports(imports, bodySchema.Resolved())
				if !op.RequestBody.RequestBody.Required {
					goType = "*" + goType
				}
				fmt.Fprintf(w, "\tBody %s `body:\"application/json\"`\n", goType)
			}
		}

		fmt.Fprintf(w, "}\n\n")
	}

	// --- Response type ---
	// Find the primary success response.
	successSchema := g.findSuccessResponseSchema(op)
	if successSchema != nil {
		// Check if this schema already has a name.
		goType := g.GoType(successSchema.Resolved(), opName+"Response")
		g.collectImports(imports, successSchema.Resolved())
		// If the GoType is just a named type, no need to emit a separate Response type.
		_ = goType
	}

	// --- Error types ---
	for code, resp := range op.Responses {
		if code == "default" || (len(code) == 3 && code[0] >= '4') {
			if resp.Response != nil {
				errSchema := g.responseSchema(resp.Response)
				if errSchema != nil {
					errName := opName + codeToName(code) + "Error"
					// Only emit if this is an inline schema.
					existing := g.GoType(errSchema.Resolved(), errName)
					g.collectImports(imports, errSchema.Resolved())
					_ = existing
				}
			}
		}
	}
}

func (g *Generator) requestBodySchema(rb *spec.RequestBody) *spec.Schema {
	if rb == nil || rb.Content == nil {
		return nil
	}
	mt, ok := rb.Content["application/json"]
	if !ok {
		return nil
	}
	return mt.Schema
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
	mt, ok := resp.Content["application/json"]
	if !ok {
		return nil
	}
	return mt.Schema
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
	if s.Type == "string" && s.Format == "date-time" {
		imports["time"] = true
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

	imports := map[string]bool{
		"github.com/mkusaka/openapigo": true,
	}

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

	// Write imports.
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

	fmt.Fprintf(w, "// %s is the endpoint for %s %s.\n", opName, method, path)
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
		g.collectImports(imports, errSchema.Resolved())

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
