package openapigo

// ptr returns a pointer to the given value.
// Used by generated code targeting Go <1.26 (which lacks new(expr)).
func ptr[T any](v T) *T { return &v }
