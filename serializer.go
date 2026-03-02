package openapigo

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"
)

// fieldMeta holds parsed struct tag information for a single field.
type fieldMeta struct {
	name     string // parameter or field name
	location string // "path", "query", "header", "cookie", "body"
	index    int    // field index in struct
}

// structMeta holds cached struct tag information.
type structMeta struct {
	fields []fieldMeta
	body   *fieldMeta // non-nil if a body field exists
}

var metaCache sync.Map // map[reflect.Type]*structMeta

// parseStructMeta extracts parameter metadata from struct tags.
// Tags: path:"name", query:"name", header:"name", cookie:"name", body:"mediaType"
func parseStructMeta(t reflect.Type) *structMeta {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if cached, ok := metaCache.Load(t); ok {
		return cached.(*structMeta)
	}

	meta := &structMeta{}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		for _, loc := range []string{"path", "query", "header", "cookie", "body"} {
			tag := f.Tag.Get(loc)
			if tag == "" {
				continue
			}
			// Parse the tag value: first part is the name, rest are options.
			name := tag
			if idx := strings.IndexByte(tag, ','); idx != -1 {
				name = tag[:idx]
			}
			fm := fieldMeta{name: name, location: loc, index: i}
			meta.fields = append(meta.fields, fm)
			if loc == "body" {
				cp := fm
				meta.body = &cp
			}
			break // a field has at most one location tag
		}
	}

	metaCache.Store(t, meta)
	return meta
}

// buildPath substitutes path parameters in the URL template.
// Template: "/pets/{petId}" with field tagged path:"petId" → "/pets/123"
func buildPath(tmpl string, req any) string {
	if req == nil {
		return tmpl
	}
	rv := reflect.ValueOf(req)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return tmpl
	}

	meta := parseStructMeta(rv.Type())
	result := tmpl
	for _, fm := range meta.fields {
		if fm.location != "path" {
			continue
		}
		fv := rv.Field(fm.index)
		val := formatValue(fv)
		result = strings.ReplaceAll(result, "{"+fm.name+"}", url.PathEscape(val))
	}
	return result
}

// buildQuery appends query parameters to the URL.
// Default style: form, explode=true (OpenAPI default for query).
func buildQuery(u *url.URL, req any) {
	if req == nil {
		return
	}
	rv := reflect.ValueOf(req)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return
	}

	meta := parseStructMeta(rv.Type())
	q := u.Query()
	for _, fm := range meta.fields {
		if fm.location != "query" {
			continue
		}
		fv := rv.Field(fm.index)
		if isZeroValue(fv) {
			continue
		}
		// Dereference pointer.
		if fv.Kind() == reflect.Ptr {
			if fv.IsNil() {
				continue
			}
			fv = fv.Elem()
		}
		// Handle slices: form/explode=true → repeated key.
		if fv.Kind() == reflect.Slice {
			for j := range fv.Len() {
				q.Add(fm.name, formatValue(fv.Index(j)))
			}
		} else {
			q.Set(fm.name, formatValue(fv))
		}
	}
	u.RawQuery = q.Encode()
}

// setHeaders sets header parameters on the request.
func setHeaders(header map[string][]string, req any) {
	if req == nil {
		return
	}
	rv := reflect.ValueOf(req)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return
	}

	meta := parseStructMeta(rv.Type())
	for _, fm := range meta.fields {
		if fm.location != "header" {
			continue
		}
		fv := rv.Field(fm.index)
		if isZeroValue(fv) {
			continue
		}
		if fv.Kind() == reflect.Ptr {
			fv = fv.Elem()
		}
		header[fm.name] = []string{formatValue(fv)}
	}
}

// formatValue converts a reflect.Value to its string representation.
func formatValue(v reflect.Value) string {
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	return fmt.Sprintf("%v", v.Interface())
}

// isZeroValue reports whether the value is the zero value for its type.
func isZeroValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		return v.IsNil()
	case reflect.Slice, reflect.Map:
		return v.IsNil()
	case reflect.Struct:
		// Check if it implements IsZero (e.g., Nullable[T]).
		if iz, ok := v.Interface().(interface{ IsZero() bool }); ok {
			return iz.IsZero()
		}
		return v.IsZero()
	default:
		return v.IsZero()
	}
}
