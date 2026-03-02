package openapigo

import "reflect"

// ptr returns a pointer to the given value.
// Used by generated code targeting Go <1.26 (which lacks new(expr)).
func ptr[T any](v T) *T { return &v }

// typeOf returns the reflect.Type of T without needing a value.
func typeOf[T any]() reflect.Type {
	return reflect.TypeOf((*T)(nil)).Elem()
}

// reflectValue returns the reflect.Value of v, dereferencing pointers.
func reflectValue(v any) reflect.Value {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	return rv
}
