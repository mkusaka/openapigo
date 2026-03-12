package spec

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ResolveConfig holds options for external $ref resolution.
type ResolveConfig struct {
	BaseDir   string            // base directory for resolving relative file paths
	AllowHTTP bool              // allow http:// (not just https://) for remote refs
	Headers   map[string]string // custom headers for remote fetches
	Timeout   time.Duration     // timeout for remote fetches (default 30s)
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
	visited    map[string]bool      // tracks visited $ref to detect cycles
	resolved   map[*Schema]bool     // memoize fully resolved schemas
	extCfg     *ResolveConfig       // nil = no external resolution
	docCache   map[string]*Document // cached external documents
	httpClient *http.Client         // reused HTTP client for remote fetches
	oas31      bool                 // true if OAS >= 3.1 (affects $ref sibling keyword handling)
	anchors    map[string]*Schema   // $anchor → schema (OAS 3.1)
	idIndex    map[string]*Schema   // absolute URI (from $id) → schema (OAS 3.1)
}

func (r *resolver) resolveAll() error {
	// Build $anchor index for OAS 3.1.
	if r.oas31 {
		if err := r.buildAnchorIndex(); err != nil {
			return err
		}
	}

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
	// Resolve prefixItems.
	for _, pi := range s.PrefixItems {
		if err := r.resolveSchema(pi); err != nil {
			return err
		}
	}
	// Resolve contains.
	if err := r.resolveSchema(s.Contains); err != nil {
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
	// Resolve conditional schemas (OAS 3.1).
	if err := r.resolveSchema(s.If); err != nil {
		return err
	}
	if err := r.resolveSchema(s.Then); err != nil {
		return err
	}
	if err := r.resolveSchema(s.Else); err != nil {
		return err
	}
	// Resolve dependentSchemas.
	for _, ds := range s.DependentSchemas {
		if err := r.resolveSchema(ds); err != nil {
			return err
		}
	}
	// Resolve patternProperties.
	for _, pp := range s.PatternProperties {
		if err := r.resolveSchema(pp); err != nil {
			return err
		}
	}
	// Resolve unevaluatedProperties/Items.
	if s.UnevaluatedProperties != nil && s.UnevaluatedProperties.Schema != nil {
		if err := r.resolveSchema(s.UnevaluatedProperties.Schema); err != nil {
			return err
		}
	}
	if s.UnevaluatedItems != nil && s.UnevaluatedItems.Schema != nil {
		if err := r.resolveSchema(s.UnevaluatedItems.Schema); err != nil {
			return err
		}
	}
	r.resolved[s] = true
	return nil
}

// buildAnchorIndex walks all component schemas and indexes any with $anchor or $id.
// Returns an error if duplicate $anchor values are found.
func (r *resolver) buildAnchorIndex() error {
	if r.anchors == nil {
		r.anchors = make(map[string]*Schema)
	}
	if r.idIndex == nil {
		r.idIndex = make(map[string]*Schema)
	}
	if r.doc.Components == nil {
		return nil
	}
	for _, s := range r.doc.Components.Schemas {
		if err := r.indexAnchors(s); err != nil {
			return err
		}
	}
	return nil
}

// indexAnchors recursively indexes schemas with $anchor and $id.
// Traversal scope matches resolveSchema to ensure consistency.
func (r *resolver) indexAnchors(s *Schema) error {
	return r.indexAnchorsWithBase(s, "")
}

// indexAnchorsWithBase recursively indexes schemas with $anchor and $id,
// resolving relative $id values against baseURI per RFC 3986.
func (r *resolver) indexAnchorsWithBase(s *Schema, baseURI string) error {
	if s == nil {
		return nil
	}
	currentBase := baseURI
	if s.ID != "" {
		absID := s.ID
		if u, err := url.Parse(absID); err == nil && !u.IsAbs() {
			// Relative $id — resolve against current base URI.
			if baseURI != "" {
				if base, err := url.Parse(baseURI); err == nil {
					absID = base.ResolveReference(u).String()
				}
			}
		}
		if existing, ok := r.idIndex[absID]; ok && existing != s {
			return fmt.Errorf("duplicate $id %q", absID)
		}
		r.idIndex[absID] = s
		currentBase = absID // $id changes the base URI for descendants
	}
	if s.Anchor != "" {
		if existing, ok := r.anchors[s.Anchor]; ok && existing != s {
			return fmt.Errorf("duplicate $anchor %q", s.Anchor)
		}
		r.anchors[s.Anchor] = s
	}
	for _, prop := range s.Properties {
		if err := r.indexAnchorsWithBase(prop, currentBase); err != nil {
			return err
		}
	}
	if err := r.indexAnchorsWithBase(s.Items, currentBase); err != nil {
		return err
	}
	for _, pi := range s.PrefixItems {
		if err := r.indexAnchorsWithBase(pi, currentBase); err != nil {
			return err
		}
	}
	if err := r.indexAnchorsWithBase(s.Contains, currentBase); err != nil {
		return err
	}
	if s.AdditionalProperties != nil {
		if err := r.indexAnchorsWithBase(s.AdditionalProperties.Schema, currentBase); err != nil {
			return err
		}
	}
	for _, sub := range s.AllOf {
		if err := r.indexAnchorsWithBase(sub, currentBase); err != nil {
			return err
		}
	}
	for _, sub := range s.OneOf {
		if err := r.indexAnchorsWithBase(sub, currentBase); err != nil {
			return err
		}
	}
	for _, sub := range s.AnyOf {
		if err := r.indexAnchorsWithBase(sub, currentBase); err != nil {
			return err
		}
	}
	if err := r.indexAnchorsWithBase(s.If, currentBase); err != nil {
		return err
	}
	if err := r.indexAnchorsWithBase(s.Then, currentBase); err != nil {
		return err
	}
	if err := r.indexAnchorsWithBase(s.Else, currentBase); err != nil {
		return err
	}
	// Traverse dependentSchemas, patternProperties, unevaluated* (consistent with resolveSchema).
	for _, ds := range s.DependentSchemas {
		if err := r.indexAnchorsWithBase(ds, currentBase); err != nil {
			return err
		}
	}
	for _, pp := range s.PatternProperties {
		if err := r.indexAnchorsWithBase(pp, currentBase); err != nil {
			return err
		}
	}
	if s.UnevaluatedProperties != nil {
		if err := r.indexAnchorsWithBase(s.UnevaluatedProperties.Schema, currentBase); err != nil {
			return err
		}
	}
	if s.UnevaluatedItems != nil {
		if err := r.indexAnchorsWithBase(s.UnevaluatedItems.Schema, currentBase); err != nil {
			return err
		}
	}
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

// resolveSchemaRef resolves a $ref string like "#/components/schemas/Pet",
// an anchor ref like "#my-anchor" (OAS 3.1), or an external ref like
// "models.json#/components/schemas/Pet".
func (r *resolver) resolveSchemaRef(ref string) (*Schema, error) {
	// Check for external ref (doesn't start with #).
	if !strings.HasPrefix(ref, "#") {
		// OAS 3.1: try $id URI lookup before external resolution.
		if r.oas31 && r.idIndex != nil {
			// Split URI and fragment: "https://example.com/foo#bar" → ("https://example.com/foo", "bar")
			uri, frag := ref, ""
			if before, after, ok := strings.Cut(ref, "#"); ok {
				uri, frag = before, after
			}
			if s, ok := r.idIndex[uri]; ok {
				if frag == "" {
					return s, nil
				}
				// Percent-decode fragment per RFC 6901 URI fragment representation.
				if decoded, err := url.PathUnescape(frag); err == nil {
					frag = decoded
				}
				// Fragment starting with "/" is a JSON Pointer.
				if strings.HasPrefix(frag, "/") {
					return resolveJSONPointerInSchema(s, frag)
				}
				// Otherwise treat as $anchor.
				if r.anchors != nil {
					if anchored, ok := r.anchors[frag]; ok {
						return anchored, nil
					}
				}
				return nil, fmt.Errorf("cannot resolve fragment %q in $id %q", frag, uri)
			}
		}
		return r.resolveExternalSchemaRef(ref)
	}
	// Try standard JSON Pointer path first.
	name, err := extractRefName(ref, "schemas")
	if err == nil {
		if r.doc.Components == nil || r.doc.Components.Schemas == nil {
			return nil, fmt.Errorf("cannot resolve %q: no components/schemas", ref)
		}
		s, ok := r.doc.Components.Schemas[name]
		if !ok {
			return nil, fmt.Errorf("cannot resolve %q: schema %q not found", ref, name)
		}
		return s, nil
	}
	// Try deep local ref (e.g. "#/components/schemas/Foo/properties/bar").
	if s, deepErr := resolveDeepSchemaFragment(r.doc, ref); deepErr == nil {
		return s, nil
	} else if strings.HasPrefix(ref, "#/components/schemas/") {
		// The ref looks like a deep components/schemas ref — return the specific error
		// instead of the generic "not a valid JSON Pointer" message.
		return nil, deepErr
	}
	// Fallback: resolve via document JSON Pointer (e.g. #/paths/.../schema).
	if s, err := resolveDocPointerSchema(r.doc, ref); err == nil {
		return s, nil
	}
	// OAS 3.1: try $anchor lookup for bare fragment refs like "#my-anchor".
	if r.oas31 && r.anchors != nil {
		anchor := strings.TrimPrefix(ref, "#")
		if s, ok := r.anchors[anchor]; ok {
			return s, nil
		}
	}
	return nil, fmt.Errorf("cannot resolve %q: not a valid JSON Pointer or $anchor", ref)
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
// resolveJSONPointerInSchema resolves a JSON Pointer fragment (e.g. "/properties/name")
// within a schema. Supports common paths: properties/<name>, items, allOf/<n>, oneOf/<n>,
// anyOf/<n>, additionalProperties.
func resolveJSONPointerInSchema(s *Schema, pointer string) (*Schema, error) {
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	current := s
	for i := 0; i < len(parts); i++ {
		seg := parts[i]
		// Unescape JSON Pointer encoding: ~1 → /, ~0 → ~
		seg = strings.ReplaceAll(seg, "~1", "/")
		seg = strings.ReplaceAll(seg, "~0", "~")

		switch seg {
		case "properties":
			if i+1 >= len(parts) {
				return nil, fmt.Errorf("JSON Pointer %q: missing property name after 'properties'", pointer)
			}
			i++
			propName := parts[i]
			propName = strings.ReplaceAll(propName, "~1", "/")
			propName = strings.ReplaceAll(propName, "~0", "~")
			prop, ok := current.Properties[propName]
			if !ok {
				return nil, fmt.Errorf("JSON Pointer %q: property %q not found", pointer, propName)
			}
			current = prop
		case "items":
			if current.Items == nil {
				return nil, fmt.Errorf("JSON Pointer %q: no items", pointer)
			}
			current = current.Items
		case "additionalProperties":
			if current.AdditionalProperties == nil || current.AdditionalProperties.Schema == nil {
				return nil, fmt.Errorf("JSON Pointer %q: no additionalProperties schema", pointer)
			}
			current = current.AdditionalProperties.Schema
		case "allOf", "oneOf", "anyOf":
			if i+1 >= len(parts) {
				return nil, fmt.Errorf("JSON Pointer %q: missing index after %q", pointer, seg)
			}
			i++
			var arr []*Schema
			switch seg {
			case "allOf":
				arr = current.AllOf
			case "oneOf":
				arr = current.OneOf
			case "anyOf":
				arr = current.AnyOf
			}
			idxStr := parts[i]
			if idxStr == "" || (len(idxStr) > 1 && idxStr[0] == '0') {
				return nil, fmt.Errorf("JSON Pointer %q: invalid index %q", pointer, idxStr)
			}
			idx := 0
			for _, c := range idxStr {
				if c < '0' || c > '9' {
					return nil, fmt.Errorf("JSON Pointer %q: invalid index %q", pointer, idxStr)
				}
				idx = idx*10 + int(c-'0')
			}
			if idx >= len(arr) {
				return nil, fmt.Errorf("JSON Pointer %q: index %d out of range (len=%d)", pointer, idx, len(arr))
			}
			current = arr[idx]
		default:
			return nil, fmt.Errorf("JSON Pointer %q: unsupported segment %q", pointer, seg)
		}
		if current == nil {
			return nil, fmt.Errorf("JSON Pointer %q: nil schema at segment %q", pointer, seg)
		}
	}
	return current, nil
}

func splitRef(ref string) (string, string) {
	if idx := strings.IndexByte(ref, '#'); idx >= 0 {
		return ref[:idx], ref[idx:]
	}
	return ref, ""
}

// resolveDeepSchemaFragment resolves a deep JSON Pointer fragment like
// "#/components/schemas/Foo/properties/bar" within a document.
// Supports percent-decoding per RFC 6901 §6.
//
// This is a targeted fix for components/schemas deep refs. Non-OAS external
// schema documents (e.g. #/Pet) require a document-level JSON Pointer resolver
// which is out of scope (see ADR-020).
func resolveDeepSchemaFragment(doc *Document, fragment string) (*Schema, error) {
	if !strings.HasPrefix(fragment, "#/") {
		return nil, fmt.Errorf("not a JSON Pointer fragment: %q", fragment)
	}
	// Percent-decode per RFC 6901 §6 (URI fragment representation).
	decoded, err := url.PathUnescape(fragment[2:])
	if err != nil {
		decoded = fragment[2:]
	}
	segments := strings.Split(decoded, "/")
	if len(segments) < 3 || segments[0] != "components" || segments[1] != "schemas" {
		return nil, fmt.Errorf("not a components/schemas ref: %q", fragment)
	}
	// Unescape JSON Pointer encoding for schema name: ~1 → /, ~0 → ~
	baseName := strings.ReplaceAll(segments[2], "~1", "/")
	baseName = strings.ReplaceAll(baseName, "~0", "~")
	if doc.Components == nil || doc.Components.Schemas == nil {
		return nil, fmt.Errorf("no components/schemas in document for %q", fragment)
	}
	base, ok := doc.Components.Schemas[baseName]
	if !ok {
		return nil, fmt.Errorf("schema %q not found for %q", baseName, fragment)
	}
	if len(segments) == 3 {
		return base, nil
	}
	// Remaining segments form a deep path within the schema.
	deepPath := strings.Join(segments[3:], "/")
	return resolveJSONPointerInSchema(base, deepPath)
}

// resolveFragmentSchema resolves a #fragment within a document.
func resolveFragmentSchema(doc *Document, fragment, fullRef string) (*Schema, error) {
	name, err := extractRefName(fragment, "schemas")
	if err == nil {
		if doc.Components == nil || doc.Components.Schemas == nil {
			return nil, fmt.Errorf("cannot resolve %q: external doc has no components/schemas", fullRef)
		}
		s, ok := doc.Components.Schemas[name]
		if !ok {
			return nil, fmt.Errorf("cannot resolve %q: schema %q not found in external doc", fullRef, name)
		}
		return s, nil
	}
	// Try deep schema fragment (e.g. "#/components/schemas/Foo/properties/bar").
	if s, deepErr := resolveDeepSchemaFragment(doc, fragment); deepErr == nil {
		return s, nil
	} else if strings.HasPrefix(fragment, "#/components/schemas/") {
		return nil, fmt.Errorf("resolve fragment in %q: %w", fullRef, deepErr)
	}
	return nil, fmt.Errorf("resolve fragment in %q: %w", fullRef, err)
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
	defer func() { _ = resp.Body.Close() }()

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
	if err == nil {
		if r.doc.Components == nil || r.doc.Components.Parameters == nil {
			return nil, fmt.Errorf("cannot resolve %q: no components/parameters", ref)
		}
		p, ok := r.doc.Components.Parameters[name]
		if !ok {
			return nil, fmt.Errorf("cannot resolve %q: parameter %q not found", ref, name)
		}
		return p, nil
	}
	// Fallback: resolve via document JSON Pointer (e.g. #/paths/.../parameters/0).
	return resolveDocPointerParameter(r.doc, ref)
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
	if err == nil {
		if r.doc.Components == nil || r.doc.Components.Responses == nil {
			return nil, fmt.Errorf("cannot resolve %q: no components/responses", ref)
		}
		resp, ok := r.doc.Components.Responses[name]
		if !ok {
			return nil, fmt.Errorf("cannot resolve %q: response %q not found", ref, name)
		}
		return resp, nil
	}
	// Fallback: resolve via document JSON Pointer (e.g. #/paths/.../responses/4XX).
	return resolveDocPointerResponse(r.doc, ref)
}

// extractRefName extracts the component name from a $ref like "#/components/{section}/Name".
// Percent-encoding (RFC 6901 §6) and JSON Pointer escapes (~0 for ~, ~1 for /) are decoded.
func extractRefName(ref, expectedSection string) (string, error) {
	if !strings.HasPrefix(ref, "#/") {
		return "", fmt.Errorf("non-fragment $ref %q: expected #/components/%s/<name>", ref, expectedSection)
	}
	// Percent-decode per RFC 6901 §6 (URI fragment representation).
	raw := ref[2:]
	if decoded, err := url.PathUnescape(raw); err == nil {
		raw = decoded
	}
	parts := strings.Split(raw, "/")
	if len(parts) != 3 || parts[0] != "components" || parts[1] != expectedSection {
		return "", fmt.Errorf("invalid $ref %q: expected #/components/%s/<name>", ref, expectedSection)
	}
	// Decode JSON Pointer escapes: ~1 → /, ~0 → ~ (order matters per RFC 6901).
	name := strings.ReplaceAll(parts[2], "~1", "/")
	name = strings.ReplaceAll(name, "~0", "~")
	return name, nil
}

// --- Document-level JSON Pointer resolution for #/paths/... refs ---

// parseDocPointerSegments decodes a fragment ref like "#/paths/~1v1~1auth/post/responses/4XX"
// into unescaped segments: ["paths", "/v1/auth", "post", "responses", "4XX"].
func parseDocPointerSegments(ref string) ([]string, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("not a fragment ref: %q", ref)
	}
	raw := ref[2:]
	// Percent-decode per RFC 6901 §6 (URI fragment representation).
	if decoded, err := url.PathUnescape(raw); err == nil {
		raw = decoded
	}
	parts := strings.Split(raw, "/")
	for i, p := range parts {
		// JSON Pointer escapes: ~1 → /, ~0 → ~ (order matters per RFC 6901).
		p = strings.ReplaceAll(p, "~1", "/")
		p = strings.ReplaceAll(p, "~0", "~")
		parts[i] = p
	}
	return parts, nil
}

// operationByMethod returns the Operation on a PathItem for the given HTTP method.
func operationByMethod(pi *PathItem, method string) *Operation {
	switch strings.ToLower(method) {
	case "get":
		return pi.Get
	case "put":
		return pi.Put
	case "post":
		return pi.Post
	case "delete":
		return pi.Delete
	case "patch":
		return pi.Patch
	default:
		return nil
	}
}

// walkToOperation walks parsed segments to locate an Operation within the document.
// It handles direct paths (paths/<key>/<method>) and callback chains
// (paths/<key>/<method>/callbacks/<name>/<expression>/<method>/...).
// Returns the operation and the index of the first segment after the method.
func walkToOperation(doc *Document, segs []string) (*Operation, int, error) {
	if len(segs) < 3 || segs[0] != "paths" {
		return nil, 0, fmt.Errorf("expected paths/<key>/<method>, got %d segments", len(segs))
	}
	pi, ok := doc.Paths[segs[1]]
	if !ok {
		return nil, 0, fmt.Errorf("path %q not found in document", segs[1])
	}
	op := operationByMethod(pi, segs[2])
	if op == nil {
		return nil, 0, fmt.Errorf("method %q not found on path %q", segs[2], segs[1])
	}
	idx := 3
	// Follow callback chains: callbacks/<name>/<expression>/<method>
	for idx < len(segs) && segs[idx] == "callbacks" {
		if idx+3 >= len(segs) {
			return nil, 0, fmt.Errorf("incomplete callbacks path at segment %d", idx)
		}
		cbName := segs[idx+1]
		cbExpr := segs[idx+2]
		cbMethod := segs[idx+3]
		if op.Callbacks == nil {
			return nil, 0, fmt.Errorf("no callbacks on operation")
		}
		cb, ok := op.Callbacks[cbName]
		if !ok {
			return nil, 0, fmt.Errorf("callback %q not found", cbName)
		}
		cbPI, ok := cb[cbExpr]
		if !ok {
			return nil, 0, fmt.Errorf("callback expression %q not found in %q", cbExpr, cbName)
		}
		op = operationByMethod(cbPI, cbMethod)
		if op == nil {
			return nil, 0, fmt.Errorf("method %q not found on callback %q/%q", cbMethod, cbName, cbExpr)
		}
		idx += 4
	}
	return op, idx, nil
}

// resolveDocPointerResponse resolves a document-level $ref like
// "#/paths/~1v1~1auth/post/responses/4XX" to a *Response.
func resolveDocPointerResponse(doc *Document, ref string) (*Response, error) {
	segs, err := parseDocPointerSegments(ref)
	if err != nil {
		return nil, err
	}
	op, idx, err := walkToOperation(doc, segs)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", ref, err)
	}
	// Expect: responses/<code>
	if idx+1 >= len(segs) || segs[idx] != "responses" {
		return nil, fmt.Errorf("resolve %q: expected responses/<code> after method, got %v", ref, segs[idx:])
	}
	code := segs[idx+1]
	respOrRef, ok := op.Responses[code]
	if !ok {
		return nil, fmt.Errorf("resolve %q: response %q not found", ref, code)
	}
	if respOrRef.Ref != "" {
		return nil, fmt.Errorf("resolve %q: target is itself a $ref %q", ref, respOrRef.Ref)
	}
	if respOrRef.Response == nil {
		return nil, fmt.Errorf("resolve %q: response %q is nil", ref, code)
	}
	return respOrRef.Response, nil
}

// resolveDocPointerParameter resolves a document-level $ref like
// "#/paths/~1v1~1customers/delete/parameters/1" to a *Parameter.
func resolveDocPointerParameter(doc *Document, ref string) (*Parameter, error) {
	segs, err := parseDocPointerSegments(ref)
	if err != nil {
		return nil, err
	}
	op, idx, err := walkToOperation(doc, segs)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", ref, err)
	}
	// Expect: parameters/<index>
	if idx+1 >= len(segs) || segs[idx] != "parameters" {
		return nil, fmt.Errorf("resolve %q: expected parameters/<index> after method, got %v", ref, segs[idx:])
	}
	idxStr := segs[idx+1]
	paramIdx, parseErr := strconv.Atoi(idxStr)
	if parseErr != nil || paramIdx < 0 {
		return nil, fmt.Errorf("resolve %q: invalid parameter index %q", ref, idxStr)
	}
	if paramIdx >= len(op.Parameters) {
		return nil, fmt.Errorf("resolve %q: parameter index %d out of range (len=%d)", ref, paramIdx, len(op.Parameters))
	}
	p := op.Parameters[paramIdx]
	if p.Ref != "" {
		return nil, fmt.Errorf("resolve %q: target is itself a $ref %q", ref, p.Ref)
	}
	if p.Parameter == nil {
		return nil, fmt.Errorf("resolve %q: parameter at index %d is nil", ref, paramIdx)
	}
	return p.Parameter, nil
}

// resolveDocPointerSchema resolves a document-level $ref like
// "#/paths/.../post/requestBody/content/application~1json/schema" to a *Schema.
func resolveDocPointerSchema(doc *Document, ref string) (*Schema, error) {
	segs, err := parseDocPointerSegments(ref)
	if err != nil {
		return nil, err
	}
	op, idx, err := walkToOperation(doc, segs)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", ref, err)
	}
	remaining := segs[idx:]
	// Walk remaining segments to reach a Schema.
	return walkOperationToSchema(op, remaining, ref)
}

// walkOperationToSchema walks segments within an Operation to locate a Schema.
// Supported paths:
//   - requestBody/content/<mediaType>/schema[/...]
//   - responses/<code>/content/<mediaType>/schema[/...]
func walkOperationToSchema(op *Operation, segs []string, ref string) (*Schema, error) {
	if len(segs) == 0 {
		return nil, fmt.Errorf("resolve %q: no segments after operation", ref)
	}
	switch segs[0] {
	case "requestBody":
		if op.RequestBody == nil {
			return nil, fmt.Errorf("resolve %q: no requestBody", ref)
		}
		if op.RequestBody.Ref != "" {
			return nil, fmt.Errorf("resolve %q: requestBody is a $ref %q", ref, op.RequestBody.Ref)
		}
		rb := op.RequestBody.RequestBody
		if rb == nil {
			return nil, fmt.Errorf("resolve %q: requestBody is nil", ref)
		}
		// Expect: content/<mediaType>/schema
		if len(segs) < 4 || segs[1] != "content" || segs[3] != "schema" {
			return nil, fmt.Errorf("resolve %q: expected requestBody/content/<mediaType>/schema", ref)
		}
		mt, ok := rb.Content[segs[2]]
		if !ok {
			return nil, fmt.Errorf("resolve %q: media type %q not found", ref, segs[2])
		}
		if mt.Schema == nil {
			return nil, fmt.Errorf("resolve %q: schema is nil for media type %q", ref, segs[2])
		}
		// If there are deeper segments, walk into the schema.
		if len(segs) > 4 {
			return resolveJSONPointerInSchema(mt.Schema, strings.Join(segs[4:], "/"))
		}
		return mt.Schema, nil
	case "responses":
		if len(segs) < 2 {
			return nil, fmt.Errorf("resolve %q: expected responses/<code> with content path", ref)
		}
		code := segs[1]
		respOrRef, ok := op.Responses[code]
		if !ok {
			return nil, fmt.Errorf("resolve %q: response %q not found", ref, code)
		}
		if respOrRef.Ref != "" {
			return nil, fmt.Errorf("resolve %q: response is a $ref %q", ref, respOrRef.Ref)
		}
		resp := respOrRef.Response
		if resp == nil {
			return nil, fmt.Errorf("resolve %q: response %q is nil", ref, code)
		}
		// Expect: content/<mediaType>/schema
		if len(segs) < 5 || segs[2] != "content" || segs[4] != "schema" {
			return nil, fmt.Errorf("resolve %q: expected responses/<code>/content/<mediaType>/schema", ref)
		}
		mt, ok := resp.Content[segs[3]]
		if !ok {
			return nil, fmt.Errorf("resolve %q: media type %q not found", ref, segs[3])
		}
		if mt.Schema == nil {
			return nil, fmt.Errorf("resolve %q: schema is nil for media type %q", ref, segs[3])
		}
		if len(segs) > 5 {
			return resolveJSONPointerInSchema(mt.Schema, strings.Join(segs[5:], "/"))
		}
		return mt.Schema, nil
	default:
		return nil, fmt.Errorf("resolve %q: unsupported operation segment %q (expected requestBody or responses)", ref, segs[0])
	}
}
