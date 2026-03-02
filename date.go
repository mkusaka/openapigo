package openapigo

import (
	"fmt"
	"time"
)

// Date represents a date without time, formatted as "YYYY-MM-DD" (ISO 8601).
// This maps to the OpenAPI "string" type with "date" format.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// NewDate creates a Date from year, month, and day.
func NewDate(year int, month time.Month, day int) Date {
	return Date{Year: year, Month: month, Day: day}
}

// DateFromTime creates a Date from a time.Time, discarding the time portion.
func DateFromTime(t time.Time) Date {
	y, m, d := t.Date()
	return Date{Year: y, Month: m, Day: d}
}

// String returns the date in "YYYY-MM-DD" format.
func (d Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

// MarshalJSON implements json.Marshaler.
func (d Date) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.String() + `"`), nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *Date) UnmarshalJSON(data []byte) error {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return fmt.Errorf("Date.UnmarshalJSON: expected quoted string, got %s", data)
	}
	s := string(data[1 : len(data)-1])
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return fmt.Errorf("Date.UnmarshalJSON: %w", err)
	}
	d.Year, d.Month, d.Day = t.Date()
	return nil
}

// IsZero reports whether the date is the zero value.
func (d Date) IsZero() bool {
	return d.Year == 0 && d.Month == 0 && d.Day == 0
}
