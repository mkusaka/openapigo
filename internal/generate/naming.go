package generate

import (
	"strings"
	"unicode"
)

// Go reserved words and predeclared identifiers.
var reserved = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true,
	"for": true, "func": true, "go": true, "goto": true, "if": true,
	"import": true, "interface": true, "map": true, "package": true,
	"range": true, "return": true, "select": true, "struct": true,
	"switch": true, "type": true, "var": true,
	// Predeclared.
	"bool": true, "byte": true, "complex64": true, "complex128": true,
	"error": true, "float32": true, "float64": true, "int": true,
	"int8": true, "int16": true, "int32": true, "int64": true,
	"rune": true, "string": true, "uint": true, "uint8": true,
	"uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"true": true, "false": true, "iota": true, "nil": true,
	"append": true, "cap": true, "close": true, "complex": true,
	"copy": true, "delete": true, "imag": true, "len": true,
	"make": true, "new": true, "panic": true, "print": true,
	"println": true, "real": true, "recover": true,
}

// ToPascalCase converts a string to PascalCase.
// Handles snake_case, kebab-case, dot.case, and preserves existing acronyms.
func ToPascalCase(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	upper := true
	for _, r := range s {
		switch {
		case r == '_' || r == '-' || r == '.' || r == ' ' || r == '/':
			upper = true
		case unicode.IsDigit(r):
			b.WriteRune(r)
			upper = true
		case upper:
			b.WriteRune(unicode.ToUpper(r))
			upper = false
		default:
			b.WriteRune(r)
		}
	}
	result := b.String()
	// Fix common acronyms: Id → ID, Url → URL, Api → API, etc.
	result = fixAcronyms(result)
	return result
}

// fixAcronyms fixes common Go acronym capitalization.
func fixAcronyms(s string) string {
	acronyms := []struct{ from, to string }{
		{"Id", "ID"},
		{"Url", "URL"},
		{"Uri", "URI"},
		{"Api", "API"},
		{"Http", "HTTP"},
		{"Https", "HTTPS"},
		{"Ssh", "SSH"},
		{"Ssl", "SSL"},
		{"Tls", "TLS"},
		{"Cpu", "CPU"},
		{"Ram", "RAM"},
		{"Uuid", "UUID"},
		{"Json", "JSON"},
		{"Xml", "XML"},
		{"Sql", "SQL"},
		{"Html", "HTML"},
		{"Css", "CSS"},
		{"Ip", "IP"},
		{"Ttl", "TTL"},
		{"Jwt", "JWT"},
	}
	for _, a := range acronyms {
		s = replaceAcronym(s, a.from, a.to)
	}
	return s
}

// replaceAcronym replaces an acronym only at word boundaries.
func replaceAcronym(s, from, to string) string {
	for {
		idx := strings.Index(s, from)
		if idx == -1 {
			return s
		}
		// Check that the match is at a word boundary (next char is uppercase, digit, or end of string).
		end := idx + len(from)
		if end < len(s) {
			next := rune(s[end])
			if unicode.IsLower(next) {
				// Not a word boundary — skip this match.
				// This prevents replacing "Identity" → "IDentity".
				s = s[:idx] + to + s[end:]
				// Actually we need to be careful. Let's only replace
				// if the next char is NOT lowercase.
				// Revert:
				s = s[:idx] + from + s[idx+len(to):]
				// Try to find next occurrence by skipping this one.
				prefix := s[:end]
				rest := replaceAcronym(s[end:], from, to)
				return prefix + rest
			}
		}
		s = s[:idx] + to + s[end:]
	}
}

// SafeName returns a Go-safe identifier. If name collides with a Go keyword,
// it appends an underscore.
func SafeName(name string) string {
	if reserved[name] {
		return name + "_"
	}
	return name
}

// ToEnumConstName converts an enum value to a Go constant name.
// e.g., "active" with type "PetStatus" → "PetStatusActive"
func ToEnumConstName(typeName, value string) string {
	return typeName + ToPascalCase(value)
}

// ToFieldName converts a JSON property name to a Go field name.
func ToFieldName(name string) string {
	result := ToPascalCase(name)
	if result == "" {
		return "Field"
	}
	return SafeName(result)
}

// sanitizeComment removes or replaces characters that could break Go comments.
// Newlines are replaced with " // " to keep each line as a valid comment.
func sanitizeComment(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
