package generate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mkusaka/openapigo/internal/spec"
)

// hasConstraints reports whether a schema has any validation constraints.
func hasConstraints(s *spec.Schema) bool {
	if s == nil {
		return false
	}
	if s.MinLength != nil || s.MaxLength != nil || s.Pattern != "" {
		return true
	}
	if s.Minimum != nil || s.Maximum != nil || s.ExclusiveMinimum != nil || s.ExclusiveMaximum != nil || s.MultipleOf != nil {
		return true
	}
	if s.MinItems != nil || s.MaxItems != nil {
		return true
	}
	if len(s.Enum) > 0 {
		return true
	}
	return false
}

// hasValidatableFields reports whether a struct schema has any fields that need validation
// (either the field itself has constraints, or the field type has a Validate() method).
func hasValidatableFields(s *spec.Schema) bool {
	for _, prop := range s.Properties {
		if prop == nil {
			continue
		}
		resolved := prop.Resolved()
		if hasConstraints(resolved) {
			return true
		}
		// Nested objects with constraints need recursive validation.
		if (resolved.Type == "object" || len(resolved.Properties) > 0) && hasValidatableFields(resolved) {
			return true
		}
		// Array items with constraints.
		if resolved.Type == "array" && resolved.Items != nil && hasConstraints(resolved.Items.Resolved()) {
			return true
		}
	}
	return false
}

// needsValidation reports whether a schema needs a Validate() method.
// Only struct types (objects with properties) get Validate().
// Named enum types do NOT — their validation is done at the field level.
func needsValidation(s *spec.Schema) bool {
	if s == nil {
		return false
	}
	// Only generate Validate() for struct types.
	if s.Type != "object" && len(s.Properties) == 0 {
		return false
	}
	return hasValidatableFields(s)
}

// emitValidateMethod generates a Validate() method for a struct type.
func (g *Generator) emitValidateMethod(w *strings.Builder, typeName string, s *spec.Schema) {
	if s == nil || !needsValidation(s) {
		return
	}

	g.imports["github.com/mkusaka/openapigo"] = true
	g.imports["fmt"] = true

	fmt.Fprintf(w, "// Validate checks all constraints on %s.\nfunc (v %s) Validate() error {\n", typeName, typeName)
	fmt.Fprintf(w, "\tvar errs openapigo.ValidationErrors\n")

	propOrder := s.PropertyOrder
	if len(propOrder) == 0 {
		for name := range s.Properties {
			propOrder = append(propOrder, name)
		}
		// sort for deterministic output
		sortStrings(propOrder)
	}

	for _, propName := range propOrder {
		prop := s.Properties[propName]
		if prop == nil {
			continue
		}
		resolved := prop.Resolved()
		fieldName := g.resolveFieldName(resolved, propName)
		req := isRequired(propName, s.Required)
		nullable := isNullable(resolved)

		if !hasConstraints(resolved) {
			continue
		}

		// For optional/nullable fields, skip validation if nil and dereference.
		isPtr := !req && !nullable // optional non-nullable → *T
		if nullable {
			fmt.Fprintf(w, "\tif !v.%s.IsZero() {\n", fieldName)
			accessor := "v." + fieldName + ".Value"
			emitFieldConstraints(w, fieldName, accessor, resolved, "\t\t")
			emitPatternCheck(w, typeName, fieldName, accessor, resolved, "\t\t")
			fmt.Fprintf(w, "\t}\n")
		} else if isPtr {
			fmt.Fprintf(w, "\tif v.%s != nil {\n", fieldName)
			accessor := "*v." + fieldName
			emitFieldConstraints(w, fieldName, accessor, resolved, "\t\t")
			emitPatternCheck(w, typeName, fieldName, accessor, resolved, "\t\t")
			fmt.Fprintf(w, "\t}\n")
		} else {
			accessor := "v." + fieldName
			emitFieldConstraints(w, fieldName, accessor, resolved, "\t")
			emitPatternCheck(w, typeName, fieldName, accessor, resolved, "\t")
		}
	}

	fmt.Fprintf(w, "\tif len(errs) > 0 {\n\t\treturn errs\n\t}\n")
	fmt.Fprintf(w, "\treturn nil\n}\n\n")
}

func emitFieldConstraints(w *strings.Builder, fieldName, accessor string, s *spec.Schema, indent string) {
	switch s.Type {
	case "string":
		emitStringConstraints(w, fieldName, accessor, s, indent)
	case "integer", "number":
		emitNumericConstraints(w, fieldName, accessor, s, indent)
	case "array":
		emitArrayConstraints(w, fieldName, accessor, s, indent)
	}

	// Enum validation for any type.
	if len(s.Enum) > 0 && s.Type == "string" {
		emitEnumValidation(w, fieldName, accessor, s, indent)
	}
}

func emitPatternCheck(w *strings.Builder, typeName, fieldName, accessor string, s *spec.Schema, indent string) {
	if s.Pattern != "" {
		fmt.Fprintf(w, "%sif !pattern%s%s.MatchString(string(%s)) {\n", indent, typeName, fieldName, accessor)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"pattern\", Message: \"does not match pattern\"})\n",
			indent, fieldName)
		fmt.Fprintf(w, "%s}\n", indent)
	}
}

func emitStringConstraints(w *strings.Builder, fieldName, accessor string, s *spec.Schema, indent string) {
	if s.MinLength != nil {
		fmt.Fprintf(w, "%sif len(%s) < %d {\n", indent, accessor, *s.MinLength)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"minLength\", Message: fmt.Sprintf(\"length %%d is less than minimum %d\", len(%s))})\n",
			indent, fieldName, *s.MinLength, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
	if s.MaxLength != nil {
		fmt.Fprintf(w, "%sif len(%s) > %d {\n", indent, accessor, *s.MaxLength)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"maxLength\", Message: fmt.Sprintf(\"length %%d exceeds maximum %d\", len(%s))})\n",
			indent, fieldName, *s.MaxLength, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
	// Note: pattern validation is handled separately in emitValidateMethod
	// because we need the type name for the pattern variable reference.
}

func emitNumericConstraints(w *strings.Builder, fieldName, accessor string, s *spec.Schema, indent string) {
	if s.Minimum != nil {
		fmt.Fprintf(w, "%sif %s < %v {\n", indent, accessor, *s.Minimum)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"minimum\", Message: fmt.Sprintf(\"value %%v is less than minimum %v\", %s)})\n",
			indent, fieldName, *s.Minimum, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
	if s.Maximum != nil {
		fmt.Fprintf(w, "%sif %s > %v {\n", indent, accessor, *s.Maximum)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"maximum\", Message: fmt.Sprintf(\"value %%v exceeds maximum %v\", %s)})\n",
			indent, fieldName, *s.Maximum, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
	if s.ExclusiveMinimum != nil {
		fmt.Fprintf(w, "%sif %s <= %v {\n", indent, accessor, *s.ExclusiveMinimum)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"exclusiveMinimum\", Message: fmt.Sprintf(\"value %%v must be greater than %v\", %s)})\n",
			indent, fieldName, *s.ExclusiveMinimum, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
	if s.ExclusiveMaximum != nil {
		fmt.Fprintf(w, "%sif %s >= %v {\n", indent, accessor, *s.ExclusiveMaximum)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"exclusiveMaximum\", Message: fmt.Sprintf(\"value %%v must be less than %v\", %s)})\n",
			indent, fieldName, *s.ExclusiveMaximum, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
}

func emitArrayConstraints(w *strings.Builder, fieldName, accessor string, s *spec.Schema, indent string) {
	if s.MinItems != nil {
		fmt.Fprintf(w, "%sif len(%s) < %d {\n", indent, accessor, *s.MinItems)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"minItems\", Message: fmt.Sprintf(\"length %%d is less than minimum %d\", len(%s))})\n",
			indent, fieldName, *s.MinItems, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
	if s.MaxItems != nil {
		fmt.Fprintf(w, "%sif len(%s) > %d {\n", indent, accessor, *s.MaxItems)
		fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"maxItems\", Message: fmt.Sprintf(\"length %%d exceeds maximum %d\", len(%s))})\n",
			indent, fieldName, *s.MaxItems, accessor)
		fmt.Fprintf(w, "%s}\n", indent)
	}
}

func emitEnumValidation(w *strings.Builder, fieldName, accessor string, s *spec.Schema, indent string) {
	var vals []string
	for _, raw := range s.Enum {
		var v string
		if json.Unmarshal(raw, &v) == nil {
			vals = append(vals, fmt.Sprintf("%q", v))
		}
	}
	if len(vals) == 0 {
		return
	}
	// Use string() cast to handle named string types (e.g., PetStatus).
	fmt.Fprintf(w, "%sswitch string(%s) {\n", indent, accessor)
	fmt.Fprintf(w, "%scase %s:\n%s\t// valid\n", indent, strings.Join(vals, ", "), indent)
	fmt.Fprintf(w, "%sdefault:\n", indent)
	fmt.Fprintf(w, "%s\terrs = append(errs, openapigo.ValidationError{Field: %q, Constraint: \"enum\", Message: fmt.Sprintf(\"invalid value %%q\", %s)})\n",
		indent, fieldName, accessor)
	fmt.Fprintf(w, "%s}\n", indent)
}

// sortStrings sorts a string slice in place.
func sortStrings(s []string) {
	// Use slices.Sort if available, otherwise manual sort.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// emitPatternVars emits package-level compiled regexp variables for schema patterns.
// Variable names include the type name to avoid collisions across schemas.
func (g *Generator) emitPatternVars(w *strings.Builder, typeName string, s *spec.Schema) {
	if s == nil || !needsValidation(s) {
		return
	}
	for _, propName := range s.PropertyOrder {
		prop := s.Properties[propName]
		if prop == nil {
			continue
		}
		resolved := prop.Resolved()
		if resolved.Pattern != "" {
			fieldName := g.resolveFieldName(resolved, propName)
			g.imports["regexp"] = true
			fmt.Fprintf(w, "var pattern%s%s = regexp.MustCompile(%q)\n", typeName, fieldName, resolved.Pattern)
		}
	}
}
