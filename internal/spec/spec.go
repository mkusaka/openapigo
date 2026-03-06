// Package spec provides an in-memory representation of an OpenAPI 3.0/3.1
// document and functions to parse and resolve $ref pointers.
package spec

// Document is the root of an OpenAPI specification.
type Document struct {
	OpenAPI    string                `json:"openapi" yaml:"openapi"`
	Info       Info                  `json:"info" yaml:"info"`
	Paths      map[string]*PathItem  `json:"paths" yaml:"paths"`
	Components *Components           `json:"components,omitempty" yaml:"components,omitempty"`
	Security   []SecurityRequirement `json:"security,omitempty" yaml:"security,omitempty"`
	Tags       []Tag                 `json:"tags,omitempty" yaml:"tags,omitempty"`

	// PathOrder preserves the iteration order of paths from the source file.
	PathOrder []string `json:"-" yaml:"-"`
}

// Info holds metadata about the API.
type Info struct {
	Title       string `json:"title" yaml:"title"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Version     string `json:"version" yaml:"version"`
}

// Tag describes a tag used by operations.
type Tag struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// PathItem describes operations available on a single path.
type PathItem struct {
	Ref         string            `json:"$ref,omitempty" yaml:"$ref,omitempty"`
	Summary     string            `json:"summary,omitempty" yaml:"summary,omitempty"`
	Description string            `json:"description,omitempty" yaml:"description,omitempty"`
	Get         *Operation        `json:"get,omitempty" yaml:"get,omitempty"`
	Put         *Operation        `json:"put,omitempty" yaml:"put,omitempty"`
	Post        *Operation        `json:"post,omitempty" yaml:"post,omitempty"`
	Delete      *Operation        `json:"delete,omitempty" yaml:"delete,omitempty"`
	Patch       *Operation        `json:"patch,omitempty" yaml:"patch,omitempty"`
	Parameters  []*ParameterOrRef `json:"parameters,omitempty" yaml:"parameters,omitempty"`
}

// Operations returns all non-nil operations with their HTTP method.
func (p *PathItem) Operations() []struct {
	Method    string
	Operation *Operation
} {
	var ops []struct {
		Method    string
		Operation *Operation
	}
	if p.Get != nil {
		ops = append(ops, struct {
			Method    string
			Operation *Operation
		}{"GET", p.Get})
	}
	if p.Post != nil {
		ops = append(ops, struct {
			Method    string
			Operation *Operation
		}{"POST", p.Post})
	}
	if p.Put != nil {
		ops = append(ops, struct {
			Method    string
			Operation *Operation
		}{"PUT", p.Put})
	}
	if p.Patch != nil {
		ops = append(ops, struct {
			Method    string
			Operation *Operation
		}{"PATCH", p.Patch})
	}
	if p.Delete != nil {
		ops = append(ops, struct {
			Method    string
			Operation *Operation
		}{"DELETE", p.Delete})
	}
	return ops
}

// Operation describes a single API operation on a path.
type Operation struct {
	OperationID string                    `json:"operationId,omitempty" yaml:"operationId,omitempty"`
	Summary     string                    `json:"summary,omitempty" yaml:"summary,omitempty"`
	Description string                    `json:"description,omitempty" yaml:"description,omitempty"`
	Tags        []string                  `json:"tags,omitempty" yaml:"tags,omitempty"`
	Parameters  []*ParameterOrRef         `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	RequestBody *RequestBodyOrRef         `json:"requestBody,omitempty" yaml:"requestBody,omitempty"`
	Responses   map[string]*ResponseOrRef `json:"responses,omitempty" yaml:"responses,omitempty"`
	Security    []SecurityRequirement     `json:"security,omitempty" yaml:"security,omitempty"`
	Deprecated  bool                      `json:"deprecated,omitempty" yaml:"deprecated,omitempty"`
}

// ParameterOrRef is either a Parameter or a $ref.
type ParameterOrRef struct {
	Ref string `json:"$ref,omitempty" yaml:"$ref,omitempty"`
	*Parameter
}

// Parameter describes a single operation parameter.
type Parameter struct {
	Name        string  `json:"name" yaml:"name"`
	In          string  `json:"in" yaml:"in"` // path, query, header, cookie
	Description string  `json:"description,omitempty" yaml:"description,omitempty"`
	Required    bool    `json:"required,omitempty" yaml:"required,omitempty"`
	Deprecated  bool    `json:"deprecated,omitempty" yaml:"deprecated,omitempty"`
	Schema      *Schema `json:"schema,omitempty" yaml:"schema,omitempty"`
	Style       string  `json:"style,omitempty" yaml:"style,omitempty"`
	Explode     *bool   `json:"explode,omitempty" yaml:"explode,omitempty"`
}

// RequestBodyOrRef is either a RequestBody or a $ref.
type RequestBodyOrRef struct {
	Ref string `json:"$ref,omitempty" yaml:"$ref,omitempty"`
	*RequestBody
}

// RequestBody describes a request body.
type RequestBody struct {
	Description string                `json:"description,omitempty" yaml:"description,omitempty"`
	Required    bool                  `json:"required,omitempty" yaml:"required,omitempty"`
	Content     map[string]*MediaType `json:"content,omitempty" yaml:"content,omitempty"`
}

// MediaType describes a media type with schema.
type MediaType struct {
	Schema *Schema `json:"schema,omitempty" yaml:"schema,omitempty"`
}

// ResponseOrRef is either a Response or a $ref.
type ResponseOrRef struct {
	Ref string `json:"$ref,omitempty" yaml:"$ref,omitempty"`
	*Response
}

// Response describes a single response from an API operation.
type Response struct {
	Description string                  `json:"description,omitempty" yaml:"description,omitempty"`
	Content     map[string]*MediaType   `json:"content,omitempty" yaml:"content,omitempty"`
	Headers     map[string]*HeaderOrRef `json:"headers,omitempty" yaml:"headers,omitempty"`
}

// HeaderOrRef is either a Header or a $ref.
type HeaderOrRef struct {
	Ref string `json:"$ref,omitempty" yaml:"$ref,omitempty"`
	*Header
}

// Header describes a response header.
type Header struct {
	Description string  `json:"description,omitempty" yaml:"description,omitempty"`
	Schema      *Schema `json:"schema,omitempty" yaml:"schema,omitempty"`
}

// Components holds reusable objects.
type Components struct {
	Schemas         map[string]*Schema         `json:"schemas,omitempty" yaml:"schemas,omitempty"`
	Parameters      map[string]*Parameter      `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	Responses       map[string]*Response       `json:"responses,omitempty" yaml:"responses,omitempty"`
	RequestBodies   map[string]*RequestBody    `json:"requestBodies,omitempty" yaml:"requestBodies,omitempty"`
	SecuritySchemes map[string]*SecurityScheme `json:"securitySchemes,omitempty" yaml:"securitySchemes,omitempty"`
}

// SecurityScheme describes a security scheme.
type SecurityScheme struct {
	Type             string      `json:"type" yaml:"type"` // apiKey, http, oauth2, openIdConnect
	Description      string      `json:"description,omitempty" yaml:"description,omitempty"`
	Name             string      `json:"name,omitempty" yaml:"name,omitempty"`     // for apiKey
	In               string      `json:"in,omitempty" yaml:"in,omitempty"`         // for apiKey: query, header, cookie
	Scheme           string      `json:"scheme,omitempty" yaml:"scheme,omitempty"` // for http: bearer, basic
	BearerFormat     string      `json:"bearerFormat,omitempty" yaml:"bearerFormat,omitempty"`
	Flows            *OAuthFlows `json:"flows,omitempty" yaml:"flows,omitempty"`
	OpenIDConnectURL string      `json:"openIdConnectUrl,omitempty" yaml:"openIdConnectUrl,omitempty"`
}

// OAuthFlows describes OAuth2 flows.
type OAuthFlows struct {
	Implicit          *OAuthFlow `json:"implicit,omitempty" yaml:"implicit,omitempty"`
	Password          *OAuthFlow `json:"password,omitempty" yaml:"password,omitempty"`
	ClientCredentials *OAuthFlow `json:"clientCredentials,omitempty" yaml:"clientCredentials,omitempty"`
	AuthorizationCode *OAuthFlow `json:"authorizationCode,omitempty" yaml:"authorizationCode,omitempty"`
}

// OAuthFlow describes a single OAuth2 flow.
type OAuthFlow struct {
	AuthorizationURL string            `json:"authorizationUrl,omitempty" yaml:"authorizationUrl,omitempty"`
	TokenURL         string            `json:"tokenUrl,omitempty" yaml:"tokenUrl,omitempty"`
	RefreshURL       string            `json:"refreshUrl,omitempty" yaml:"refreshUrl,omitempty"`
	Scopes           map[string]string `json:"scopes,omitempty" yaml:"scopes,omitempty"`
}

// SecurityRequirement maps security scheme names to scopes.
type SecurityRequirement map[string][]string
