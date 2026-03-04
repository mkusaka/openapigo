package openapigo

import (
	"fmt"
	"reflect"
	"strings"
)

// Validatable is implemented by generated types that have validation constraints.
type Validatable interface {
	Validate() error
}

// ValidationError represents a single validation failure.
type ValidationError struct {
	Field      string // field path (e.g., "user.name")
	Constraint string // constraint name (e.g., "minLength")
	Message    string // human-readable description
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// ValidationErrors is a collection of validation errors.
type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return "no validation errors"
	}
	if len(e) == 1 {
		return e[0].Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d validation errors:", len(e))
	for _, ve := range e {
		b.WriteString("\n  - ")
		b.WriteString(ve.Error())
	}
	return b.String()
}

// IsZero reports whether a value is the zero value for its type.
// Used by generated dependentRequired validation code.
func IsZero(v any) bool {
	if v == nil {
		return true
	}
	return reflect.ValueOf(v).IsZero()
}

// AddFieldPrefix prepends a field path prefix to all errors in the collection.
func (e ValidationErrors) AddFieldPrefix(prefix string) ValidationErrors {
	for i := range e {
		if e[i].Field == "" {
			e[i].Field = prefix
		} else {
			e[i].Field = prefix + "." + e[i].Field
		}
	}
	return e
}
