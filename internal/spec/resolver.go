package spec

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// ResolveConfig holds options for external $ref resolution.
type ResolveConfig struct {
	BaseDir        string            // base directory for resolving relative file paths
	AllowHTTP      bool              // allow http:// (not just https://) for remote refs
	Headers        map[string]string // custom headers for remote fetches
	Timeout        time.Duration     // timeout for remote fetches (default 30s)
}

// isOAS31 returns true if the document's OpenAPI version is >= 3.1.
func isOAS31(doc *Document) bool {
	return strings.HasPrefix(doc.OpenAPI, "3.1")
}

// Resolve resolves all local $ref pointers in the document.
// External $ref (file://, http://) are not handled here.
func Resolve(doc *Document) error {
	r := &resolver{
		doc:      doc,
		visited:  make(map[string]bool),
		resolved: make(map[*Schema]bool),
		oas31:    isOAS31(doc),
	}
	return r.resolveAll()
}

// ResolveWithExternal resolves all $ref pointers including external file/URL refs.
func ResolveWithExternal(doc *Document, cfg ResolveConfig) error {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	r := &resolver{
		doc:        doc,
		visited:    make(map[string]bool),
		resolved:   make(map[*Schema]bool),
		extCfg:     &cfg,
		docCache:   make(map[string]*Document),
		httpClient: &http.Client{Timeout: cfg.Timeout},
		oas31:      isOAS31(doc),
	}
	return r.resolveAll()
}

type resolver struct {
	doc        *Document
	visited    map[string]bool  // tracks visited $ref to detect cycles
	resolved   map[*Schema]bool // memoize fully resolved schemas
	extCfg     *ResolveConfig   // nil = no external resolution
	docCache   map[string]*Document // cached external documents
	httpClient *http.Client     // reused HTTP client for remote fetches
	oas31      bool             // true if OAS >= 3.1 (affects $ref sibling keyword handling)
}

func (r *resolver) resolveAll() error {
	// Resolve schemas in components.
	if r.doc.Components != nil {
		for _, s := range r.doc.Components.Schemas {
			if err := r.resolveSchema(s); err != nil {
				return err
			}
		}
	}

	// First pass: resolve all PathItem $refs.
	// Uses recursive resolution so chained refs (A→B→C) are handled
	// regardless of map iteration order.
	visitedPI := make(map[*PathItem]bool)
	for _, pi := range r.doc.Paths {
		r.resolvePathItemRef(pi, visitedPI)
	}

	// Second pass: resolve operations and parameters.
	for _, pi := range r.doc.Paths {
		if err := r.resolvePathItem(pi); err != nil {
			return err
		}
	}
	return nil
}

// resolvePathItemRef resolves a PathItem $ref, recursively resolving the target first
// to handle chained refs. The visited set prevents infinite loops from circular refs.
func (r *resolver) resolvePathItemRef(pi *PathItem, visited map[*PathItem]bool) {
	if pi == nil || pi.Ref == "" || !strings.HasPrefix(pi.Ref, "#/paths/") {
		return
	}
	if visited[pi] {
		pi.Ref = ""
		return
	}
	visited[pi] = true

	// Un-escape JSON Pointer: ~1 → /, ~0 → ~
	pathKey := pi.Ref[len("#/paths/"):]
	pathKey = strings.ReplaceAll(pathKey, "~1", "/")
	pathKey = strings.ReplaceAll(pathKey, "~0", "~")
	if target, ok := r.doc.Paths[pathKey]; ok && target != pi {
		// Resolve target's $ref first (chained refs).
		r.resolvePathItemRef(target, visited)
		pi.Get = target.Get
		pi.Put = target.Put
		pi.Post = target.Post
		pi.Delete = target.Delete
		pi.Patch = target.Patch
		if len(pi.Parameters) == 0 {
			pi.Parameters = target.Parameters
		}
	}
	pi.Ref = ""
}

func (r *resolver) resolvePathItem(pi *PathItem) error {
	if pi == nil {
		return nil
	}
	// PathItem $refs are already resolved in the first pass.
	// Resolve path-level parameters.
	for _, p := range pi.Parameters {
		if err := r.resolveParameterOrRef(p); err != nil {
			return err
		}
	}
	for _, op := range pi.Operations() {
		if err := r.resolveOperation(op.Operation); err != nil {
			return err
		}
	}
	return nil
}

func (r *resolver) resolveOperation(op *Operation) error {
	if op == nil {
		return nil
	}
	for _, p := range op.Parameters {
		if err := r.resolveParameterOrRef(p); err != nil {
			return err
		}
	}
	if op.RequestBody != nil {
		if err := r.resolveRequestBodyOrRef(op.RequestBody); err != nil {
			return err
		}
	}
	for _, resp := range op.Responses {
		if err := r.resolveResponseOrRef(resp); err != nil {
			return err
		}
	}
	return nil
}

func (r *resolver) resolveParameterOrRef(p *ParameterOrRef) error {
	if p == nil {
		return nil
	}
	if p.Ref != "" {
		resolved, err := r.resolveParameterRef(p.Ref)
		if err != nil {
			return err
		}
		p.Parameter = resolved
		p.Ref = "" // clear the ref
	}
	if p.Parameter != nil && p.Parameter.Schema != nil {
		return r.resolveSchema(p.Parameter.Schema)
	}
	return nil
}

func (r *resolver) resolveRequestBodyOrRef(rb *RequestBodyOrRef) error {
	if rb == nil {
		return nil
	}
	if rb.Ref != "" {
		resolved, err := r.resolveRequestBodyRef(rb.Ref)
		if err != nil {
			return err
		}
		rb.RequestBody = resolved
		rb.Ref = ""
	}
	if rb.RequestBody != nil {
		for _, mt := range rb.RequestBody.Content {
			if mt != nil && mt.Schema != nil {
				if err := r.resolveSchema(mt.Schema); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (r *resolver) resolveResponseOrRef(resp *ResponseOrRef) error {
	if resp == nil {
		return nil
	}
	if resp.Ref != "" {
		resolved, err := r.resolveResponseRef(resp.Ref)
		if err != nil {
			return err
		}
		resp.Response = resolved
		resp.Ref = ""
	}
	if resp.Response != nil {
		for _, mt := range resp.Response.Content {
			if mt != nil && mt.Schema != nil {
				if err := r.resolveSchema(mt.Schema); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (r *resolver) resolveSchema(s *Schema) error {
	if s == nil {
		return nil
	}
	if r.resolved[s] {
		return nil
	}
	if s.Ref != "" {
		if r.visited[s.Ref] {
			return nil
		}
		r.visited[s.Ref] = true
		resolved, err := r.resolveSchemaRef(s.Ref)
		if err != nil {
			return err
		}
		// OAS 3.1: $ref in Schema Object can have sibling keywords that override
		// the referenced schema. Create a merged copy if siblings are present.
		if r.oas31 && hasSiblingKeywords(s) {
			merged := *resolved // shallow copy
			applySiblingOverrides(&merged, s)
			s.resolvedRef = &merged
		} else {
			s.resolvedRef = resolved
		}
		if err := r.resolveSchema(resolved); err != nil {
			return err
		}
		delete(r.visited, s.Ref)
		r.resolved[s] = true
		return nil
	}

	// Resolve properties.
	for _, prop := range s.Properties {
		if err := r.resolveSchema(prop); err != nil {
			return err
		}
	}
	// Resolve items.
	if err := r.resolveSchema(s.Items); err != nil {
		return err
	}
	// Resolve additionalProperties.
	if s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil {
		if err := r.resolveSchema(s.AdditionalProperties.Schema); err != nil {
			return err
		}
	}
	// Resolve composition.
	for _, sub := range s.AllOf {
		if err := r.resolveSchema(sub); err != nil {
			return err
		}
	}
	for _, sub := range s.OneOf {
		if err := r.resolveSchema(sub); err != nil {
			return err
		}
	}
	for _, sub := range s.AnyOf {
		if err := r.resolveSchema(sub); err != nil {
			return err
		}
	}
	r.resolved[s] = true
	return nil
}

// hasSiblingKeywords reports whether a $ref schema has sibling keywords
// that should be applied in OAS 3.1 mode.
func hasSiblingKeywords(s *Schema) bool {
	return s.Description != "" || s.Title != "" ||
		s.ReadOnly || s.WriteOnly ||
		s.Deprecated || s.Nullable ||
		s.Default != nil
}

// applySiblingOverrides copies sibling keyword values from src (the $ref schema)
// onto dst (a copy of the resolved target). Only non-zero values are applied.
func applySiblingOverrides(dst *Schema, src *Schema) {
	if src.Description != "" {
		dst.Description = src.Description
	}
	if src.Title != "" {
		dst.Title = src.Title
	}
	if src.ReadOnly {
		dst.ReadOnly = true
	}
	if src.WriteOnly {
		dst.WriteOnly = true
	}
	if src.Deprecated {
		dst.Deprecated = true
	}
	if src.Nullable {
		dst.Nullable = true
	}
	if src.Default != nil {
		dst.Default = src.Default
	}
}

// resolveSchemaRef resolves a $ref string like "#/components/schemas/Pet" or
// an external ref like "models.json#/components/schemas/Pet".
func (r *resolver) resolveSchemaRef(ref string) (*Schema, error) {
	// Check for external ref (doesn't start with #).
	if !strings.HasPrefix(ref, "#") {
		return r.resolveExternalSchemaRef(ref)
	}
	name, err := extractRefName(ref, "schemas")
	if err != nil {
		return nil, err
	}
	if r.doc.Components == nil || r.doc.Components.Schemas == nil {
		return nil, fmt.Errorf("cannot resolve %q: no components/schemas", ref)
	}
	s, ok := r.doc.Components.Schemas[name]
	if !ok {
		return nil, fmt.Errorf("cannot resolve %q: schema %q not found", ref, name)
	}
	return s, nil
}

// resolveExternalSchemaRef resolves an external file/URL $ref.
// Format: "path/to/file.json#/components/schemas/Name" or just "path/to/file.json".
func (r *resolver) resolveExternalSchemaRef(ref string) (*Schema, error) {
	if r.extCfg == nil {
		return nil, fmt.Errorf("external $ref %q not supported (use --resolve)", ref)
	}

	// Split into file part and fragment.
	filePart, fragment := splitRef(ref)

	// Load external document.
	extDoc, err := r.loadExternalDoc(filePart)
	if err != nil {
		return nil, fmt.Errorf("resolve external $ref %q: %w", ref, err)
	}

	// If no fragment, the ref points to the root (not supported for schemas).
	if fragment == "" {
		return nil, fmt.Errorf("external $ref %q has no fragment (expected #/components/schemas/Name)", ref)
	}

	// Resolve fragment within external document.
	return resolveFragmentSchema(extDoc, fragment, ref)
}

// splitRef splits "file.json#/components/schemas/Pet" into ("file.json", "#/components/schemas/Pet").
func splitRef(ref string) (string, string) {
	if idx := strings.IndexByte(ref, '#'); idx >= 0 {
		return ref[:idx], ref[idx:]
	}
	return ref, ""
}

// resolveFragmentSchema resolves a #fragment within a document.
func resolveFragmentSchema(doc *Document, fragment, fullRef string) (*Schema, error) {
	name, err := extractRefName(fragment, "schemas")
	if err != nil {
		return nil, fmt.Errorf("resolve fragment in %q: %w", fullRef, err)
	}
	if doc.Components == nil || doc.Components.Schemas == nil {
		return nil, fmt.Errorf("cannot resolve %q: external doc has no components/schemas", fullRef)
	}
	s, ok := doc.Components.Schemas[name]
	if !ok {
		return nil, fmt.Errorf("cannot resolve %q: schema %q not found in external doc", fullRef, name)
	}
	return s, nil
}

// loadExternalDoc loads and caches an external document.
func (r *resolver) loadExternalDoc(filePart string) (*Document, error) {
	// Normalize to absolute path.
	absPath, err := r.resolveFilePath(filePart)
	if err != nil {
		return nil, err
	}

	// Check cache.
	if doc, ok := r.docCache[absPath]; ok {
		return doc, nil
	}

	var doc *Document
	if isURL(filePart) {
		doc, err = r.fetchRemoteDoc(filePart)
	} else {
		doc, err = Load(absPath)
	}
	if err != nil {
		return nil, err
	}

	// Resolve internal refs within the external document.
	extResolver := &resolver{
		doc:        doc,
		visited:    make(map[string]bool),
		resolved:   make(map[*Schema]bool),
		extCfg:     r.extCfg,
		docCache:   r.docCache,
		httpClient: r.httpClient,
		oas31:      r.oas31,
	}
	// Update extCfg base dir for relative refs in the external doc.
	if !isURL(filePart) {
		savedBaseDir := r.extCfg.BaseDir
		r.extCfg.BaseDir = filepath.Dir(absPath)
		if err := extResolver.resolveAll(); err != nil {
			r.extCfg.BaseDir = savedBaseDir
			return nil, fmt.Errorf("resolve external doc %q: %w", filePart, err)
		}
		r.extCfg.BaseDir = savedBaseDir
	} else {
		if err := extResolver.resolveAll(); err != nil {
			return nil, fmt.Errorf("resolve external doc %q: %w", filePart, err)
		}
	}

	r.docCache[absPath] = doc

	// Inject external schemas into the root document's components so the
	// generator can see them.
	r.injectExternalSchemas(doc)

	return doc, nil
}

// injectExternalSchemas copies schemas from an external document into the root
// document's components so the generator can generate types for them.
func (r *resolver) injectExternalSchemas(extDoc *Document) {
	if extDoc.Components == nil || len(extDoc.Components.Schemas) == 0 {
		return
	}
	if r.doc.Components == nil {
		r.doc.Components = &Components{}
	}
	if r.doc.Components.Schemas == nil {
		r.doc.Components.Schemas = make(map[string]*Schema)
	}
	for name, s := range extDoc.Components.Schemas {
		if _, exists := r.doc.Components.Schemas[name]; !exists {
			r.doc.Components.Schemas[name] = s
		}
	}
}

// resolveFilePath resolves a relative file path against the base directory.
func (r *resolver) resolveFilePath(filePart string) (string, error) {
	if isURL(filePart) {
		return filePart, nil
	}
	if filepath.IsAbs(filePart) {
		return filePart, nil
	}
	abs, err := filepath.Abs(filepath.Join(r.extCfg.BaseDir, filePart))
	if err != nil {
		return "", err
	}
	return abs, nil
}

// isURL checks if a string looks like a URL.
func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// fetchRemoteDoc fetches and parses a remote OpenAPI document.
func (r *resolver) fetchRemoteDoc(rawURL string) (*Document, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	if u.Scheme == "http" && !r.extCfg.AllowHTTP {
		return nil, fmt.Errorf("HTTP not allowed for %q (use --allow-http)", rawURL)
	}

	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range r.extCfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %q: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %q: HTTP %d", rawURL, resp.StatusCode)
	}

	data, err := readAllLimited(resp.Body, 50*1024*1024) // 50MB limit
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", rawURL, err)
	}

	// Detect format from URL extension or content type.
	ext := filepath.Ext(u.Path)
	if ext == "" {
		ct := resp.Header.Get("Content-Type")
		if strings.Contains(ct, "yaml") || strings.Contains(ct, "yml") {
			ext = ".yaml"
		} else {
			ext = ".json"
		}
	}

	return Parse(data, ext)
}

// readAllLimited reads up to limit bytes from r.
func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	lr := io.LimitReader(r, limit+1) // read 1 extra byte to detect overflow
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response too large (>%d bytes)", limit)
	}
	return data, nil
}

func (r *resolver) resolveParameterRef(ref string) (*Parameter, error) {
	name, err := extractRefName(ref, "parameters")
	if err != nil {
		return nil, err
	}
	if r.doc.Components == nil || r.doc.Components.Parameters == nil {
		return nil, fmt.Errorf("cannot resolve %q: no components/parameters", ref)
	}
	p, ok := r.doc.Components.Parameters[name]
	if !ok {
		return nil, fmt.Errorf("cannot resolve %q: parameter %q not found", ref, name)
	}
	return p, nil
}

func (r *resolver) resolveRequestBodyRef(ref string) (*RequestBody, error) {
	name, err := extractRefName(ref, "requestBodies")
	if err != nil {
		return nil, err
	}
	if r.doc.Components == nil || r.doc.Components.RequestBodies == nil {
		return nil, fmt.Errorf("cannot resolve %q: no components/requestBodies", ref)
	}
	rb, ok := r.doc.Components.RequestBodies[name]
	if !ok {
		return nil, fmt.Errorf("cannot resolve %q: requestBody %q not found", ref, name)
	}
	return rb, nil
}

func (r *resolver) resolveResponseRef(ref string) (*Response, error) {
	name, err := extractRefName(ref, "responses")
	if err != nil {
		return nil, err
	}
	if r.doc.Components == nil || r.doc.Components.Responses == nil {
		return nil, fmt.Errorf("cannot resolve %q: no components/responses", ref)
	}
	resp, ok := r.doc.Components.Responses[name]
	if !ok {
		return nil, fmt.Errorf("cannot resolve %q: response %q not found", ref, name)
	}
	return resp, nil
}

// extractRefName extracts the component name from a $ref like "#/components/{section}/Name".
// JSON Pointer escapes (~0 for ~, ~1 for /) are decoded.
func extractRefName(ref, expectedSection string) (string, error) {
	if !strings.HasPrefix(ref, "#/") {
		return "", fmt.Errorf("non-fragment $ref %q: expected #/components/%s/<name>", ref, expectedSection)
	}
	parts := strings.Split(ref[2:], "/")
	if len(parts) != 3 || parts[0] != "components" || parts[1] != expectedSection {
		return "", fmt.Errorf("invalid $ref %q: expected #/components/%s/<name>", ref, expectedSection)
	}
	// Decode JSON Pointer escapes: ~1 → /, ~0 → ~ (order matters per RFC 6901).
	name := strings.ReplaceAll(parts[2], "~1", "/")
	name = strings.ReplaceAll(name, "~0", "~")
	return name, nil
}
