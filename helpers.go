package openapigo

import (
	"encoding/json"
	"fmt"
	"reflect"
)

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

// ExtractFieldKeys decodes a JSON object and returns the set of top-level keys.
// Returns an error if data is not a JSON object (including null).
func ExtractFieldKeys(data []byte) (map[string]struct{}, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("extractFieldKeys: not a JSON object: %w", err)
	}
	if obj == nil {
		return nil, fmt.Errorf("extractFieldKeys: not a JSON object: got null")
	}
	keys := make(map[string]struct{}, len(obj))
	for k := range obj {
		keys[k] = struct{}{}
	}
	return keys, nil
}

// MarshalMerge merges multiple JSON-serializable values into a single JSON object.
// All values must marshal to JSON objects. Conflicting keys (same key, different values)
// cause an error (ErrAnyOfConflictingKeys).
func MarshalMerge(variants ...any) ([]byte, error) {
	merged := make(map[string]json.RawMessage)
	for _, v := range variants {
		if v == nil {
			continue
		}
		data, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshalMerge: marshal variant: %w", err)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(data, &obj); err != nil {
			return nil, fmt.Errorf("marshalMerge: variant is not a JSON object: %w", err)
		}
		if obj == nil {
			return nil, fmt.Errorf("marshalMerge: variant is not a JSON object: got null")
		}
		for k, val := range obj {
			if existing, ok := merged[k]; ok {
				if string(existing) != string(val) {
					return nil, fmt.Errorf("%w: key %q", ErrAnyOfConflictingKeys, k)
				}
			}
			merged[k] = val
		}
	}
	return json.Marshal(merged)
}
