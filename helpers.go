package openapigo

import "reflect"

// typeOf returns the reflect.Type of T without needing a value.
func typeOf[T any]() reflect.Type {
	return reflect.TypeFor[T]()
}

// reflectValue returns the reflect.Value of v, dereferencing pointers.
func reflectValue(v any) reflect.Value {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	return rv
}
