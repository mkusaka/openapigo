package generate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	source := filepath.Base(cfg.Input)

	typesCode := g.generateTypes(source)
	opsCode := g.generateOperations(source)
	endpointsCode := g.generateEndpoints(source)

	// 6. Write files.
	for name, code := range map[string]string{
		"types.go":      typesCode,
		"operations.go": opsCode,
		"endpoints.go":  endpointsCode,
	} {
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
// This is needed because gopkg.in/yaml.v3 doesn't automatically populate custom fields.
func (g *Generator) parseExtensions() {
	// For JSON parsing, we need to re-read the raw spec to get extensions.
	// For now, parse extensions from the raw JSON file.
	data, err := os.ReadFile(g.config.Input)
	if err != nil {
		return
	}
	ext := strings.ToLower(filepath.Ext(g.config.Input))
	if ext != ".json" {
		// TODO: YAML extension parsing
		return
	}

	var raw struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}

	for name, schemaData := range raw.Components.Schemas {
		s, ok := g.doc.Components.Schemas[name]
		if !ok || s == nil {
			continue
		}
		parseSchemaExtensions(s, schemaData)
	}

	// Also parse inline schemas in paths.
	g.parsePathExtensions(data)
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

func (g *Generator) parsePathExtensions(data []byte) {
	var raw struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	for _, pathData := range raw.Paths {
		g.parsePathItemExtensions(pathData)
	}
}

func (g *Generator) parsePathItemExtensions(data json.RawMessage) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	for _, method := range []string{"get", "post", "put", "patch", "delete"} {
		opData, ok := raw[method]
		if !ok {
			continue
		}
		g.parseOperationExtensions(opData)
	}
}

func (g *Generator) parseOperationExtensions(data json.RawMessage) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	// Parse requestBody inline schemas.
	if rbData, ok := raw["requestBody"]; ok {
		var rb map[string]json.RawMessage
		if err := json.Unmarshal(rbData, &rb); err == nil {
			if contentData, ok := rb["content"]; ok {
				var content map[string]json.RawMessage
				if err := json.Unmarshal(contentData, &content); err == nil {
					for _, mtData := range content {
						var mt map[string]json.RawMessage
						if err := json.Unmarshal(mtData, &mt); err == nil {
							if schemaData, ok := mt["schema"]; ok {
								g.parseInlineSchemaExtensions(schemaData)
							}
						}
					}
				}
			}
		}
	}
	// Parse response inline schemas.
	if respData, ok := raw["responses"]; ok {
		var responses map[string]json.RawMessage
		if err := json.Unmarshal(respData, &responses); err == nil {
			for _, rData := range responses {
				var resp map[string]json.RawMessage
				if err := json.Unmarshal(rData, &resp); err == nil {
					if contentData, ok := resp["content"]; ok {
						var content map[string]json.RawMessage
						if err := json.Unmarshal(contentData, &content); err == nil {
							for _, mtData := range content {
								var mt map[string]json.RawMessage
								if err := json.Unmarshal(mtData, &mt); err == nil {
									if schemaData, ok := mt["schema"]; ok {
										g.parseInlineSchemaExtensions(schemaData)
									}
								}
							}
						}
					}
				}
			}
		}
	}
}

func (g *Generator) parseInlineSchemaExtensions(data json.RawMessage) {
	// This is for inline schemas in operations.
	// We can't easily match them to our spec.Schema objects here,
	// so this is a best-effort approach.
	// The main extension parsing happens through component schemas.
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
	names := make([]string, 0, len(g.doc.Components.Schemas))
	for name := range g.doc.Components.Schemas {
		names = append(names, name)
	}
	sort.Strings(names)

	// Emit component schemas.
	for _, name := range names {
		s := g.doc.Components.Schemas[name]
		goName := g.schemaNames[s]
		g.emitSchemaType(&typeDefs, goName, s)
	}

	// Emit inline schemas that were discovered during type generation.
	emitted := make(map[string]bool)
	for _, ns := range names {
		emitted[g.schemaNames[g.doc.Components.Schemas[ns]]] = true
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

// emitOperation writes request/response structs for an operation.
func (g *Generator) emitOperation(w *strings.Builder, imports map[string]bool, path, method string, op *spec.Operation, pathParams []*spec.ParameterOrRef) {
	if op == nil || op.OperationID == "" {
		return
	}

	opName := ToPascalCase(op.OperationID)

	// Collect all parameters (path-level + operation-level).
	allParams := append(slices.Clone(pathParams), op.Parameters...)

	hasParams := len(allParams) > 0
	hasBody := op.RequestBody != nil && op.RequestBody.RequestBody != nil

	// --- Params struct ---
	if hasParams || hasBody {
		fmt.Fprintf(w, "// %sParams is the request parameters for %s.\ntype %sParams struct {\n", opName, op.OperationID, opName)

		for _, p := range allParams {
			if p == nil || p.Parameter == nil {
				continue
			}
			param := p.Parameter
			fieldName := ToFieldName(param.Name)
			goType := "string"
			if param.Schema != nil {
				goType = g.GoType(param.Schema.Resolved(), opName+fieldName)
				g.collectImports(imports, param.Schema.Resolved())
			}
			if !param.Required {
				goType = "*" + goType
			}
			fmt.Fprintf(w, "\t%s %s `%s:\"%s\"`\n", fieldName, goType, param.In, param.Name)
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
	for _, code := range []string{"200", "201", "202"} {
		if resp, ok := op.Responses[code]; ok {
			return g.responseSchema(resp.Response)
		}
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

	opName := ToPascalCase(op.OperationID)

	// Determine request type.
	allParams := append(slices.Clone(pathParams), op.Parameters...)
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

	// Find success codes.
	var successCodes []string
	for code := range op.Responses {
		if len(code) == 3 && code[0] == '2' {
			successCodes = append(successCodes, code)
		}
	}
	sort.Strings(successCodes)
	if len(successCodes) == 0 {
		successCodes = []string{"200"}
	}

	fmt.Fprintf(w, "// %s is the endpoint for %s %s.\n", opName, method, path)
	if op.Summary != "" {
		fmt.Fprintf(w, "// %s\n", op.Summary)
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

	w.WriteString("\n\n")
}
