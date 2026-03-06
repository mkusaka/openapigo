package generate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mkusaka/openapigo/internal/spec"
)

// generateAuth produces the auth.go file content with middleware constructors
// for each security scheme in the spec.
func (g *Generator) generateAuth(source string) string {
	if g.doc.Components == nil || len(g.doc.Components.SecuritySchemes) == 0 {
		return ""
	}

	var w strings.Builder
	w.WriteString(fileHeader(g.config.Package, source))
	w.WriteString("\n")

	imports := map[string]bool{
		"net/http":                     true,
		"github.com/mkusaka/openapigo": true,
	}

	var funcDefs strings.Builder

	// Sort scheme names for deterministic output.
	names := make([]string, 0, len(g.doc.Components.SecuritySchemes))
	for name := range g.doc.Components.SecuritySchemes {
		names = append(names, name)
	}
	sort.Strings(names)

	usedFuncNames := make(map[string]int)
	for _, name := range names {
		ss := g.doc.Components.SecuritySchemes[name]
		if ss == nil {
			continue
		}
		pascal := ToPascalCase(name)
		funcName := "With" + pascal
		if !strings.HasSuffix(pascal, "Auth") {
			funcName += "Auth"
		}
		usedFuncNames[funcName]++
		if usedFuncNames[funcName] > 1 {
			funcName = fmt.Sprintf("%s%d", funcName, usedFuncNames[funcName])
		}
		switch ss.Type {
		case "apiKey":
			emitAPIKeyAuth(&funcDefs, funcName, ss)
		case "http":
			if strings.EqualFold(ss.Scheme, "basic") {
				emitBasicAuth(&funcDefs, funcName, ss)
				imports["encoding/base64"] = true
			} else if strings.EqualFold(ss.Scheme, "bearer") {
				emitBearerAuth(&funcDefs, funcName, ss)
			}
		}
	}

	// If no auth functions were emitted (e.g., only oauth2/openIdConnect schemes),
	// skip writing the file to avoid unused imports.
	if funcDefs.Len() == 0 {
		return ""
	}

	// Write imports.
	w.WriteString("import (\n")
	importPaths := make([]string, 0, len(imports))
	for imp := range imports {
		importPaths = append(importPaths, imp)
	}
	sort.Strings(importPaths)
	for _, imp := range importPaths {
		fmt.Fprintf(&w, "\t%q\n", imp)
	}
	w.WriteString(")\n\n")

	w.WriteString(funcDefs.String())
	return w.String()
}

func emitAPIKeyAuth(w *strings.Builder, funcName string, ss *spec.SecurityScheme) {
	comment := sanitizeComment(ss.Description)
	if comment != "" {
		fmt.Fprintf(w, "// %s returns an Option that sets the %q %s parameter.\n// %s\n",
			funcName, ss.Name, ss.In, comment)
	} else {
		fmt.Fprintf(w, "// %s returns an Option that sets the %q %s parameter.\n",
			funcName, ss.Name, ss.In)
	}
	fmt.Fprintf(w, "func %s(token string) openapigo.Option {\n", funcName)
	switch ss.In {
	case "header":
		fmt.Fprintf(w, "\treturn openapigo.WithMiddleware(openapigo.MiddlewareFunc(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {\n")
		fmt.Fprintf(w, "\t\treq.Header.Set(%q, token)\n", ss.Name)
		fmt.Fprintf(w, "\t\treturn next(req)\n")
		fmt.Fprintf(w, "\t}))\n")
	case "query":
		fmt.Fprintf(w, "\treturn openapigo.WithMiddleware(openapigo.MiddlewareFunc(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {\n")
		fmt.Fprintf(w, "\t\tq := req.URL.Query()\n")
		fmt.Fprintf(w, "\t\tq.Set(%q, token)\n", ss.Name)
		fmt.Fprintf(w, "\t\treq.URL.RawQuery = q.Encode()\n")
		fmt.Fprintf(w, "\t\treturn next(req)\n")
		fmt.Fprintf(w, "\t}))\n")
	case "cookie":
		fmt.Fprintf(w, "\treturn openapigo.WithMiddleware(openapigo.MiddlewareFunc(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {\n")
		fmt.Fprintf(w, "\t\treq.AddCookie(&http.Cookie{Name: %q, Value: token})\n", ss.Name)
		fmt.Fprintf(w, "\t\treturn next(req)\n")
		fmt.Fprintf(w, "\t}))\n")
	}
	fmt.Fprintf(w, "}\n\n")
}

func emitBasicAuth(w *strings.Builder, funcName string, ss *spec.SecurityScheme) {
	comment := sanitizeComment(ss.Description)
	if comment != "" {
		fmt.Fprintf(w, "// %s returns an Option that sets HTTP Basic authentication.\n// %s\n",
			funcName, comment)
	} else {
		fmt.Fprintf(w, "// %s returns an Option that sets HTTP Basic authentication.\n", funcName)
	}
	fmt.Fprintf(w, "func %s(username, password string) openapigo.Option {\n", funcName)
	fmt.Fprintf(w, "\tcred := base64.StdEncoding.EncodeToString([]byte(username + \":\" + password))\n")
	fmt.Fprintf(w, "\treturn openapigo.WithMiddleware(openapigo.MiddlewareFunc(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {\n")
	fmt.Fprintf(w, "\t\treq.Header.Set(\"Authorization\", \"Basic \"+cred)\n")
	fmt.Fprintf(w, "\t\treturn next(req)\n")
	fmt.Fprintf(w, "\t}))\n")
	fmt.Fprintf(w, "}\n\n")
}

func emitBearerAuth(w *strings.Builder, funcName string, ss *spec.SecurityScheme) {
	comment := sanitizeComment(ss.Description)
	if comment != "" {
		fmt.Fprintf(w, "// %s returns an Option that sets Bearer token authentication.\n// %s\n",
			funcName, comment)
	} else {
		fmt.Fprintf(w, "// %s returns an Option that sets Bearer token authentication.\n", funcName)
	}
	fmt.Fprintf(w, "func %s(token string) openapigo.Option {\n", funcName)
	fmt.Fprintf(w, "\treturn openapigo.WithMiddleware(openapigo.MiddlewareFunc(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {\n")
	fmt.Fprintf(w, "\t\treq.Header.Set(\"Authorization\", \"Bearer \"+token)\n")
	fmt.Fprintf(w, "\t\treturn next(req)\n")
	fmt.Fprintf(w, "\t}))\n")
	fmt.Fprintf(w, "}\n\n")
}
