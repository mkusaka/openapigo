package spec

import (
	"fmt"
	"strings"
)

// Resolve resolves all local $ref pointers in the document.
// External $ref (file://, http://) are not handled here.
func Resolve(doc *Document) error {
	r := &resolver{
		doc:      doc,
		visited:  make(map[string]bool),
		resolved: make(map[*Schema]bool),
	}
	return r.resolveAll()
}

type resolver struct {
	doc      *Document
	visited  map[string]bool  // tracks visited $ref to detect cycles
	resolved map[*Schema]bool // memoize fully resolved schemas
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
		s.resolvedRef = resolved
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

// resolveSchemaRef resolves a $ref string like "#/components/schemas/Pet".
func (r *resolver) resolveSchemaRef(ref string) (*Schema, error) {
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
		return "", fmt.Errorf("external $ref %q not supported (use --resolve)", ref)
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
