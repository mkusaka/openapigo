package openapigo

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNewDate(t *testing.T) {
	d := NewDate(2024, time.March, 15)
	if d.Year != 2024 || d.Month != time.March || d.Day != 15 {
		t.Fatalf("got %v", d)
	}
}

func TestDateFromTime(t *testing.T) {
	tm := time.Date(2024, time.December, 25, 13, 45, 0, 0, time.UTC)
	d := DateFromTime(tm)
	if d.Year != 2024 || d.Month != time.December || d.Day != 25 {
		t.Fatalf("got %v", d)
	}
}

func TestDate_String(t *testing.T) {
	tests := []struct {
		date Date
		want string
	}{
		{NewDate(2024, time.January, 1), "2024-01-01"},
		{NewDate(2024, time.December, 31), "2024-12-31"},
		{NewDate(1, time.January, 1), "0001-01-01"},
	}
	for _, tt := range tests {
		if got := tt.date.String(); got != tt.want {
			t.Errorf("Date(%v).String() = %q, want %q", tt.date, got, tt.want)
		}
	}
}

func TestDate_MarshalJSON(t *testing.T) {
	d := NewDate(2024, time.March, 15)
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `"2024-03-15"` {
		t.Fatalf("got %s", data)
	}
}

func TestDate_UnmarshalJSON(t *testing.T) {
	var d Date
	if err := json.Unmarshal([]byte(`"2024-03-15"`), &d); err != nil {
		t.Fatal(err)
	}
	if d.Year != 2024 || d.Month != time.March || d.Day != 15 {
		t.Fatalf("got %v", d)
	}
}

func TestDate_UnmarshalJSON_Invalid(t *testing.T) {
	var d Date
	if err := json.Unmarshal([]byte(`"not-a-date"`), &d); err == nil {
		t.Fatal("expected error for invalid date")
	}
	if err := json.Unmarshal([]byte(`12345`), &d); err == nil {
		t.Fatal("expected error for non-string")
	}
}

func TestDate_RoundTrip(t *testing.T) {
	type wrapper struct {
		D Date `json:"d"`
	}
	original := wrapper{D: NewDate(2024, time.June, 30)}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded wrapper
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.D != original.D {
		t.Fatalf("round-trip failed: got %v, want %v", decoded.D, original.D)
	}
}

func TestDate_IsZero(t *testing.T) {
	var zero Date
	if !zero.IsZero() {
		t.Fatal("zero Date should be zero")
	}
	if NewDate(2024, time.January, 1).IsZero() {
		t.Fatal("non-zero Date should not be zero")
	}
}

func TestDate_OmitZero(t *testing.T) {
	type wrapper struct {
		D Date `json:"d,omitzero"`
	}
	data, err := json.Marshal(wrapper{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{}` {
		t.Fatalf("expected empty JSON, got %s", data)
	}
}
