package openapigo

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// fieldMeta holds parsed struct tag information for a single field.
type fieldMeta struct {
	name     string // parameter or field name
	location string // "path", "query", "header", "cookie", "body"
	style    string // serialization style: simple, label, matrix, form, spaceDelimited, pipeDelimited, deepObject
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
	if t.Kind() == reflect.Pointer {
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
			// Default style per location.
			style := defaultStyle(loc)
			for _, opt := range parts[1:] {
				switch opt {
				case "explode", "explode=true":
					explode = true
				case "noexplode", "explode=false":
					explode = false
				case "simple", "label", "matrix", "form",
					"spaceDelimited", "pipeDelimited", "deepObject":
					style = opt
				}
			}
			fm := fieldMeta{name: name, location: loc, style: style, index: i, explode: explode}
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

// defaultStyle returns the default serialization style for a parameter location.
func defaultStyle(loc string) string {
	switch loc {
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

// buildPath substitutes path parameters in the URL template.
// Template: "/pets/{petId}" with field tagged path:"petId" → "/pets/123"
// Supports styles: simple (default), label, matrix.
func buildPath(tmpl string, req any) string {
	if req == nil {
		return tmpl
	}
	rv := reflect.ValueOf(req)
	if rv.Kind() == reflect.Pointer {
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
		if fv.Kind() == reflect.Pointer {
			if fv.IsNil() {
				continue
			}
			fv = fv.Elem()
		}
		var val string
		switch fm.style {
		case "label":
			val = serializePathLabel(fv, fm.explode)
		case "matrix":
			val = serializePathMatrix(fv, fm.name, fm.explode)
		default: // "simple"
			val = serializePathSimple(fv, fm.explode)
		}
		result = strings.ReplaceAll(result, "{"+fm.name+"}", val)
	}
	return result
}

// serializePathSimple serializes a value using the simple style (default for path).
// Primitive: value. Array: comma-separated. Object (map): key,value pairs.
func serializePathSimple(fv reflect.Value, explode bool) string {
	if fv.Kind() == reflect.Slice {
		var vals []string
		for j := range fv.Len() {
			vals = append(vals, url.PathEscape(formatValue(fv.Index(j))))
		}
		return strings.Join(vals, ",")
	}
	if fv.Kind() == reflect.Map {
		sep := ","
		if explode {
			return serializeMapKV(fv, "=", ",", url.PathEscape)
		}
		return serializeMapKV(fv, ",", sep, url.PathEscape)
	}
	return url.PathEscape(formatValue(fv))
}

// serializePathLabel serializes a value using the label style (.prefix).
// Primitive: .value. Array: .v1.v2 (explode) or .v1,v2 (no explode).
func serializePathLabel(fv reflect.Value, explode bool) string {
	if fv.Kind() == reflect.Slice {
		var vals []string
		for j := range fv.Len() {
			vals = append(vals, url.PathEscape(formatValue(fv.Index(j))))
		}
		if explode {
			return "." + strings.Join(vals, ".")
		}
		return "." + strings.Join(vals, ",")
	}
	if fv.Kind() == reflect.Map {
		if explode {
			return "." + serializeMapKV(fv, "=", ".", url.PathEscape)
		}
		return "." + serializeMapKV(fv, ",", ",", url.PathEscape)
	}
	return "." + url.PathEscape(formatValue(fv))
}

// serializePathMatrix serializes a value using the matrix style (;name=value).
// Primitive: ;name=value. Array: ;name=v1,v2 or ;name=v1;name=v2 (explode).
func serializePathMatrix(fv reflect.Value, name string, explode bool) string {
	eName := url.PathEscape(name)
	if fv.Kind() == reflect.Slice {
		if explode {
			var parts []string
			for j := range fv.Len() {
				parts = append(parts, ";"+eName+"="+url.PathEscape(formatValue(fv.Index(j))))
			}
			return strings.Join(parts, "")
		}
		var vals []string
		for j := range fv.Len() {
			vals = append(vals, url.PathEscape(formatValue(fv.Index(j))))
		}
		return ";" + eName + "=" + strings.Join(vals, ",")
	}
	if fv.Kind() == reflect.Map {
		if explode {
			return serializeMapMatrix(fv, true)
		}
		return ";" + eName + "=" + serializeMapKV(fv, ",", ",", url.PathEscape)
	}
	return ";" + eName + "=" + url.PathEscape(formatValue(fv))
}

// serializeMapKV serializes a map as key(kvSep)value pairs joined by pairSep.
func serializeMapKV(fv reflect.Value, kvSep, pairSep string, escape func(string) string) string {
	keys := sortedMapKeys(fv)
	var parts []string
	for _, k := range keys {
		v := fv.MapIndex(reflect.ValueOf(k))
		parts = append(parts, escape(k)+kvSep+escape(formatValue(v)))
	}
	return strings.Join(parts, pairSep)
}

// serializeMapMatrix serializes a map using matrix style with explode.
func serializeMapMatrix(fv reflect.Value, _ bool) string {
	keys := sortedMapKeys(fv)
	var b strings.Builder
	for _, k := range keys {
		v := fv.MapIndex(reflect.ValueOf(k))
		b.WriteString(";")
		b.WriteString(url.PathEscape(k))
		b.WriteString("=")
		b.WriteString(url.PathEscape(formatValue(v)))
	}
	return b.String()
}

// sortedMapKeys returns sorted string keys of a map for deterministic output.
func sortedMapKeys(fv reflect.Value) []string {
	keys := make([]string, 0, fv.Len())
	iter := fv.MapRange()
	for iter.Next() {
		keys = append(keys, fmt.Sprintf("%v", iter.Key().Interface()))
	}
	sort.Strings(keys)
	return keys
}

// buildQuery appends query parameters to the URL.
// Default style: form, explode=true (OpenAPI default for query).
// Also supports spaceDelimited, pipeDelimited, and deepObject styles.
func buildQuery(u *url.URL, req any) {
	if req == nil {
		return
	}
	rv := reflect.ValueOf(req)
	if rv.Kind() == reflect.Pointer {
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
		if shouldSkipParam(fv) {
			continue
		}
		// Dereference pointer.
		if fv.Kind() == reflect.Pointer {
			if fv.IsNil() {
				continue
			}
			fv = fv.Elem()
		}

		switch fm.style {
		case "spaceDelimited":
			serializeQueryDelimited(q, fm.name, fv, fm.explode, " ")
		case "pipeDelimited":
			serializeQueryDelimited(q, fm.name, fv, fm.explode, "|")
		case "deepObject":
			serializeQueryDeepObject(q, fm.name, fv)
		default: // "form"
			serializeQueryForm(q, fm.name, fv, fm.explode)
		}
	}
	u.RawQuery = q.Encode()
}

// serializeQueryForm serializes a query parameter using the form style.
func serializeQueryForm(q url.Values, name string, fv reflect.Value, explode bool) {
	if fv.Kind() == reflect.Slice {
		if explode {
			// form/explode=true → repeated key: ?color=blue&color=black
			for j := range fv.Len() {
				q.Add(name, formatValue(fv.Index(j)))
			}
		} else {
			// form/explode=false → comma-separated: ?color=blue,black
			var vals []string
			for j := range fv.Len() {
				vals = append(vals, formatValue(fv.Index(j)))
			}
			q.Set(name, strings.Join(vals, ","))
		}
	} else if fv.Kind() == reflect.Map {
		if explode {
			// form/explode=true → ?key1=val1&key2=val2
			for _, k := range sortedMapKeys(fv) {
				v := fv.MapIndex(reflect.ValueOf(k))
				q.Set(k, formatValue(v))
			}
		} else {
			// form/explode=false → ?name=key1,val1,key2,val2
			var parts []string
			for _, k := range sortedMapKeys(fv) {
				v := fv.MapIndex(reflect.ValueOf(k))
				parts = append(parts, k, formatValue(v))
			}
			q.Set(name, strings.Join(parts, ","))
		}
	} else {
		q.Set(name, formatValue(fv))
	}
}

// serializeQueryDelimited serializes an array query parameter with a custom delimiter.
// Used by spaceDelimited and pipeDelimited styles.
func serializeQueryDelimited(q url.Values, name string, fv reflect.Value, explode bool, delim string) {
	if fv.Kind() != reflect.Slice {
		// Non-array: fall back to form style.
		q.Set(name, formatValue(fv))
		return
	}
	if explode {
		// explode=true → repeated key (same as form)
		for j := range fv.Len() {
			q.Add(name, formatValue(fv.Index(j)))
		}
	} else {
		// explode=false → delimiter-separated
		var vals []string
		for j := range fv.Len() {
			vals = append(vals, formatValue(fv.Index(j)))
		}
		q.Set(name, strings.Join(vals, delim))
	}
}

// serializeQueryDeepObject serializes a map/struct using deepObject style.
// deepObject always uses explode=true: ?name[key]=value
func serializeQueryDeepObject(q url.Values, name string, fv reflect.Value) {
	if fv.Kind() == reflect.Map {
		for _, k := range sortedMapKeys(fv) {
			v := fv.MapIndex(reflect.ValueOf(k))
			q.Set(name+"["+k+"]", formatValue(v))
		}
	} else if fv.Kind() == reflect.Struct {
		t := fv.Type()
		for i := range t.NumField() {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			fieldVal := fv.Field(i)
			if shouldSkipParam(fieldVal) {
				continue
			}
			// Use json tag name if available.
			fieldName := field.Tag.Get("json")
			if fieldName == "" || fieldName == "-" {
				fieldName = field.Name
			}
			if idx := strings.IndexByte(fieldName, ','); idx != -1 {
				fieldName = fieldName[:idx]
			}
			if fieldVal.Kind() == reflect.Pointer {
				if fieldVal.IsNil() {
					continue
				}
				fieldVal = fieldVal.Elem()
			}
			q.Set(name+"["+fieldName+"]", formatValue(fieldVal))
		}
	} else {
		// Scalar: fall back to form.
		q.Set(name, formatValue(fv))
	}
}

// setHeaders sets header parameters on the request.
func setHeaders(header map[string][]string, req any) {
	if req == nil {
		return
	}
	rv := reflect.ValueOf(req)
	if rv.Kind() == reflect.Pointer {
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
		if shouldSkipParam(fv) {
			continue
		}
		if fv.Kind() == reflect.Pointer {
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
	if rv.Kind() == reflect.Pointer {
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
		if shouldSkipParam(fv) {
			continue
		}
		if fv.Kind() == reflect.Pointer {
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
	if v.Kind() == reflect.Pointer {
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

// isNilValue reports whether the value is nil.
// Unlike isZeroValue, it does not treat scalar zero values (0, false, "") as empty,
// which preserves required non-pointer fields with valid zero values.
func isNilValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		return v.IsNil()
	case reflect.Slice, reflect.Map:
		return v.IsNil()
	default:
		return false
	}
}

// shouldSkipParam reports whether a parameter field should be omitted from the request.
// Nil pointers and nil slices/maps are skipped. Nullable structs with IsZero() are skipped.
// Non-pointer zero values (0, false, "") are NOT skipped — they represent valid required param values.
func shouldSkipParam(fv reflect.Value) bool {
	switch fv.Kind() {
	case reflect.Pointer, reflect.Interface:
		return fv.IsNil()
	case reflect.Slice, reflect.Map:
		return fv.IsNil()
	case reflect.Struct:
		// Nullable[T] implements IsZero() and should be skipped when absent.
		if iz, ok := fv.Interface().(interface{ IsZero() bool }); ok {
			return iz.IsZero()
		}
		return false
	default:
		return false
	}
}
