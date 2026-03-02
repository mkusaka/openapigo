package openapigo

import (
	"encoding/json"
	"testing"
)

func TestNullable_ZeroValue(t *testing.T) {
	var n Nullable[string]
	if n.IsSet() {
		t.Error("zero value should not be set")
	}
	if n.IsNull() {
		t.Error("zero value should not be null")
	}
	if n.IsValue() {
		t.Error("zero value should not be value")
	}
	if !n.IsZero() {
		t.Error("zero value should report IsZero")
	}
}

func TestNullable_Value(t *testing.T) {
	n := Value("hello")
	if !n.IsSet() {
		t.Error("should be set")
	}
	if n.IsNull() {
		t.Error("should not be null")
	}
	if !n.IsValue() {
		t.Error("should be value")
	}
	if n.IsZero() {
		t.Error("should not be zero")
	}
	v, ok := n.Get()
	if !ok || v != "hello" {
		t.Errorf("Get() = %q, %v; want %q, true", v, ok, "hello")
	}
}

func TestNullable_Null(t *testing.T) {
	n := Null[int]()
	if !n.IsSet() {
		t.Error("should be set")
	}
	if !n.IsNull() {
		t.Error("should be null")
	}
	if n.IsValue() {
		t.Error("should not be value")
	}
	_, ok := n.Get()
	if ok {
		t.Error("Get() should return false for null")
	}
}

func TestNullable_MarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		n    Nullable[string]
		want string
	}{
		{"absent", Nullable[string]{}, "null"},
		{"null", Null[string](), "null"},
		{"value", Value("hello"), `"hello"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.n)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != tt.want {
				t.Errorf("got %s, want %s", b, tt.want)
			}
		})
	}
}

func TestNullable_UnmarshalJSON(t *testing.T) {
	type S struct {
		Name Nullable[string] `json:"name,omitzero"`
	}

	t.Run("absent", func(t *testing.T) {
		var s S
		if err := json.Unmarshal([]byte(`{}`), &s); err != nil {
			t.Fatal(err)
		}
		if s.Name.IsSet() {
			t.Error("absent field should not be set")
		}
	})

	t.Run("null", func(t *testing.T) {
		var s S
		if err := json.Unmarshal([]byte(`{"name":null}`), &s); err != nil {
			t.Fatal(err)
		}
		if !s.Name.IsNull() {
			t.Error("null field should be null")
		}
	})

	t.Run("value", func(t *testing.T) {
		var s S
		if err := json.Unmarshal([]byte(`{"name":"alice"}`), &s); err != nil {
			t.Fatal(err)
		}
		v, ok := s.Name.Get()
		if !ok || v != "alice" {
			t.Errorf("Get() = %q, %v; want %q, true", v, ok, "alice")
		}
	})
}

func TestNullable_RoundTrip(t *testing.T) {
	type S struct {
		A Nullable[int]    `json:"a,omitzero"`
		B Nullable[string] `json:"b,omitzero"`
		C Nullable[bool]   `json:"c,omitzero"`
	}
	original := S{
		A: Value(42),
		B: Null[string](),
		// C is absent
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":42,"b":null}`
	if string(data) != want {
		t.Errorf("Marshal = %s, want %s", data, want)
	}

	var decoded S
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if v, ok := decoded.A.Get(); !ok || v != 42 {
		t.Errorf("A = %v, %v", v, ok)
	}
	if !decoded.B.IsNull() {
		t.Error("B should be null")
	}
	if decoded.C.IsSet() {
		t.Error("C should be absent")
	}
}
