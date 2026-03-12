package openapigo

import (
	"errors"
	"testing"
)

func TestExtractFieldKeys(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    []string
		wantErr bool
	}{
		{"simple object", `{"a":1,"b":"x"}`, []string{"a", "b"}, false},
		{"empty object", `{}`, nil, false},
		{"not an object", `[1,2]`, nil, true},
		{"null", `null`, nil, true},
		{"not JSON", `invalid`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys, err := ExtractFieldKeys([]byte(tt.data))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			for _, k := range tt.want {
				if _, ok := keys[k]; !ok {
					t.Errorf("missing key %q", k)
				}
			}
			if len(keys) != len(tt.want) {
				t.Errorf("got %d keys, want %d", len(keys), len(tt.want))
			}
		})
	}
}

func TestMarshalMerge(t *testing.T) {
	type A struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	type B struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}

	t.Run("merge non-conflicting", func(t *testing.T) {
		a := A{Name: "Alice", Age: 30}
		b := B{Name: "Alice", Email: "alice@example.com"}
		data, err := MarshalMerge(a, b)
		if err != nil {
			t.Fatal(err)
		}
		// Should contain all keys.
		keys, _ := ExtractFieldKeys(data)
		for _, k := range []string{"name", "age", "email"} {
			if _, ok := keys[k]; !ok {
				t.Errorf("missing key %q in merged output", k)
			}
		}
	})

	t.Run("conflicting values", func(t *testing.T) {
		a := A{Name: "Alice", Age: 30}
		b := B{Name: "Bob", Email: "bob@example.com"}
		_, err := MarshalMerge(a, b)
		if err == nil {
			t.Fatal("expected error for conflicting values")
		}
		if !errors.Is(err, ErrAnyOfConflictingKeys) {
			t.Errorf("got %v, want ErrAnyOfConflictingKeys", err)
		}
	})

	t.Run("nil variant skipped", func(t *testing.T) {
		a := A{Name: "Alice", Age: 30}
		data, err := MarshalMerge(nil, a)
		if err != nil {
			t.Fatal(err)
		}
		keys, _ := ExtractFieldKeys(data)
		if len(keys) != 2 {
			t.Errorf("got %d keys, want 2", len(keys))
		}
	})
}
