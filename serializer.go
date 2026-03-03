package openapigo

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"
)

// fieldMeta holds parsed struct tag information for a single field.
type fieldMeta struct {
	name     string // parameter or field name
	location string // "path", "query", "header", "cookie", "body"
	index    int    // field index in struct
	explode  bool   // explode=true (default for query/cookie)
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
	if t.Kind() != reflect.Struct {
		return &structMeta{}
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
			// Parse the tag value: "name,option1,option2"
			parts := strings.Split(tag, ",")
			name := parts[0]
			// Default explode: true for query/cookie (OAS default), false for path/header.
			explode := loc == "query" || loc == "cookie"
			for _, opt := range parts[1:] {
				switch opt {
				case "explode", "explode=true":
					explode = true
				case "noexplode", "explode=false":
					explode = false
				}
			}
			fm := fieldMeta{name: name, location: loc, index: i, explode: explode}
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
		// Dereference pointer.
		if fv.Kind() == reflect.Ptr {
			if fv.IsNil() {
				continue
			}
			fv = fv.Elem()
		}
		// OpenAPI simple style: arrays are comma-separated.
		var val string
		if fv.Kind() == reflect.Slice {
			var vals []string
			for j := range fv.Len() {
				vals = append(vals, formatValue(fv.Index(j)))
			}
			val = strings.Join(vals, ",")
		} else {
			val = formatValue(fv)
		}
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
		// Handle slices.
		if fv.Kind() == reflect.Slice {
			if fm.explode {
				// form/explode=true → repeated key: ?color=blue&color=black
				for j := range fv.Len() {
					q.Add(fm.name, formatValue(fv.Index(j)))
				}
			} else {
				// form/explode=false → comma-separated: ?color=blue,black
				var vals []string
				for j := range fv.Len() {
					vals = append(vals, formatValue(fv.Index(j)))
				}
				q.Set(fm.name, strings.Join(vals, ","))
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
		// OpenAPI simple style: arrays are comma-separated.
		if fv.Kind() == reflect.Slice {
			var vals []string
			for j := range fv.Len() {
				vals = append(vals, formatValue(fv.Index(j)))
			}
			header[fm.name] = []string{strings.Join(vals, ",")}
		} else {
			header[fm.name] = []string{formatValue(fv)}
		}
	}
}

// setCookies sets cookie parameters on the request.
func setCookies(req *http.Request, reqVal any) {
	if reqVal == nil {
		return
	}
	rv := reflect.ValueOf(reqVal)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return
	}

	meta := parseStructMeta(rv.Type())
	for _, fm := range meta.fields {
		if fm.location != "cookie" {
			continue
		}
		fv := rv.Field(fm.index)
		if isZeroValue(fv) {
			continue
		}
		if fv.Kind() == reflect.Ptr {
			if fv.IsNil() {
				continue
			}
			fv = fv.Elem()
		}
		if fv.Kind() == reflect.Slice {
			if fm.explode {
				for j := range fv.Len() {
					req.AddCookie(&http.Cookie{Name: fm.name, Value: formatValue(fv.Index(j))})
				}
			} else {
				var vals []string
				for j := range fv.Len() {
					vals = append(vals, formatValue(fv.Index(j)))
				}
				req.AddCookie(&http.Cookie{Name: fm.name, Value: strings.Join(vals, ",")})
			}
		} else {
			req.AddCookie(&http.Cookie{Name: fm.name, Value: formatValue(fv)})
		}
	}
}

// formatValue converts a reflect.Value to its string representation.
// time.Time values are formatted as RFC3339 for OpenAPI date-time parameters.
func formatValue(v reflect.Value) string {
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	if t, ok := v.Interface().(time.Time); ok {
		return t.Format(time.RFC3339)
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
