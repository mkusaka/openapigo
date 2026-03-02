package openapigo

import (
	"bytes"
	"encoding/json"
)

// Nullable represents a value that can be absent, null, or present.
// It is used for OpenAPI fields that are nullable.
//
// Three states:
//   - Absent: field was not present in JSON (zero value of Nullable)
//   - Null: field was present and explicitly set to null
//   - Value: field was present with a non-null value
type Nullable[T any] struct {
	value T
	set   bool // true if field was present in JSON (null or value)
	valid bool // true if present AND not null
}

// Value creates a Nullable holding a non-null value.
func Value[T any](v T) Nullable[T] {
	return Nullable[T]{value: v, set: true, valid: true}
}

// Null creates a Nullable representing an explicit null.
func Null[T any]() Nullable[T] {
	return Nullable[T]{set: true, valid: false}
}

// Get returns the contained value and whether it is valid (non-null and present).
func (n Nullable[T]) Get() (T, bool) {
	return n.value, n.valid
}

// IsSet reports whether the field was present in JSON (either null or a value).
func (n Nullable[T]) IsSet() bool {
	return n.set
}

// IsNull reports whether the field was present and explicitly null.
func (n Nullable[T]) IsNull() bool {
	return n.set && !n.valid
}

// IsValue reports whether the field contains a non-null value.
func (n Nullable[T]) IsValue() bool {
	return n.valid
}

// IsZero reports whether the Nullable is in its zero state (absent).
// This enables json:",omitzero" to omit absent fields.
func (n Nullable[T]) IsZero() bool {
	return !n.set
}

// MarshalJSON implements json.Marshaler.
// Absent → field is omitted (via omitzero), Null → "null", Value → marshaled T.
func (n Nullable[T]) MarshalJSON() ([]byte, error) {
	if !n.valid {
		return []byte("null"), nil
	}
	return json.Marshal(n.value)
}

// UnmarshalJSON implements json.Unmarshaler.
func (n *Nullable[T]) UnmarshalJSON(data []byte) error {
	n.set = true
	if bytes.Equal(data, []byte("null")) {
		n.valid = false
		var zero T
		n.value = zero
		return nil
	}
	n.valid = true
	return json.Unmarshal(data, &n.value)
}
