# ADR-018: Security Schemes and Authentication

## Status

Accepted

## Date

2026-03-01

## Context

OpenAPI defines security schemes that describe how an API is protected:

| Type | Subtypes | Where credentials go |
|------|----------|---------------------|
| `apiKey` | header, query, cookie | Named header/query param/cookie |
| `http` | basic, bearer, digest | `Authorization` header |
| `oauth2` | implicit, password, clientCredentials, authorizationCode | `Authorization: Bearer` header |
| `openIdConnect` | — | `Authorization: Bearer` header |
| `mutualTLS` (OAS 3.1) | — | TLS client certificate |

Security requirements are declared at the operation level and/or globally:

```yaml
security:
  - bearerAuth: []      # Global default
  - apiKey: []           # Alternative

paths:
  /public:
    get:
      security: []       # Override: no auth required
  /admin:
    get:
      security:
        - oauth2: [admin]  # Requires 'admin' scope (scopes are only valid for oauth2/openIdConnect)
```

### Design philosophy

Per ADR-001, authentication is **middleware-based**. We do not embed auth logic into generated client methods. Instead:

1. Generate **convenience functions** from the spec's `securitySchemes` definition (except `mutualTLS`, which is configured at the `http.Client` transport level — see below)
2. These functions return **middleware** that the user adds to the client
3. The middleware handles credential injection

## Decision

### Auth Middleware Generation

For each security scheme defined in the spec, we generate a middleware constructor. **Function names are derived from the scheme name** (not the scheme type) to avoid collisions when multiple schemes of the same type exist:

| Scheme name | Type | Generated function |
|---|---|---|
| `bearerAuth` | http/bearer | `WithBearerAuth(token)` |
| `internalBearerAuth` | http/bearer | `WithInternalBearerAuth(token)` |
| `apiKeyAuth` | apiKey/header | `WithAPIKeyAuth(key)` |
| `legacyApiKey` | apiKey/query | `WithLegacyApiKey(key)` |

The scheme name is converted to PascalCase for the Go function name. **Collision handling** applies to **all generated symbols** from security schemes — not just middleware function names, but also const/var declarations (TokenURL, Scopes, etc.). Multiple scheme names may produce the same PascalCase identifier (e.g., `foo-bar` and `foo_bar` both become `FooBar`). The generator detects collisions after PascalCase conversion across all generated symbols and disambiguates by appending the scheme type as a suffix (e.g., `WithFooBarBearer`, `WithFooBarAPIKey`, `FooBarBearerTokenURL`). Note: scope-related constants are only generated for `oauth2` and `openIdConnect` schemes (the only types where `scopes` is valid per the OpenAPI specification). If that still collides, a numeric suffix is added (`WithFooBar2`). **Deterministic ordering**: Collision resolution processes scheme names in **alphabetical order** (by their original OpenAPI key name) to ensure deterministic output regardless of YAML/JSON map iteration order. The generator emits a warning when disambiguation is needed.

```yaml
components:
  securitySchemes:
    bearerAuth:
      type: http
      scheme: bearer
      bearerFormat: JWT
    apiKeyAuth:
      type: apiKey
      in: header
      name: X-API-Key
    basicAuth:
      type: http
      scheme: basic
    oauth2:
      type: oauth2
      flows:
        clientCredentials:
          tokenUrl: https://auth.example.com/token
          scopes:
            read: Read access
            write: Write access
```

```go
// ===== Generated: auth.go =====

// WithBearerAuth returns middleware that adds Bearer token authentication.
// Security scheme: bearerAuth (http/bearer, JWT)
func WithBearerAuth(token string) openapigo.Middleware {
    return openapigo.MiddlewareFunc(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
        req.Header.Set("Authorization", "Bearer "+token)
        return next(req)
    })
}

// WithBearerAuthFunc returns middleware that calls tokenFunc before each request.
// Use this for token refresh scenarios.
func WithBearerAuthFunc(tokenFunc func(ctx context.Context) (string, error)) openapigo.Middleware {
    return openapigo.MiddlewareFunc(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
        token, err := tokenFunc(req.Context())
        if err != nil {
            return nil, fmt.Errorf("obtaining bearer token: %w", err)
        }
        req.Header.Set("Authorization", "Bearer "+token)
        return next(req)
    })
}

// WithAPIKeyAuth returns middleware that adds API key authentication.
// Security scheme: apiKeyAuth (apiKey in header "X-API-Key")
func WithAPIKeyAuth(apiKey string) openapigo.Middleware {
    return openapigo.MiddlewareFunc(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
        req.Header.Set("X-API-Key", apiKey)
        return next(req)
    })
}

// WithBasicAuth returns middleware that adds HTTP Basic authentication.
// Security scheme: basicAuth (http/basic)
func WithBasicAuth(username, password string) openapigo.Middleware {
    return openapigo.MiddlewareFunc(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
        req.SetBasicAuth(username, password)
        return next(req)
    })
}
```

**Other HTTP schemes** (e.g., `digest`, `hoba`, `mutual`, `negotiate`, `vapid`): The OpenAPI `http` security scheme's `scheme` field accepts any value from the [IANA HTTP Authentication Scheme Registry](https://www.iana.org/assignments/http-authschemes/). For schemes beyond `basic` and `bearer`, we generate a generic middleware that sets the `Authorization` header with the scheme name:

```go
// Generated for: type: http, scheme: digest
// WithDigestAuth returns middleware for HTTP Digest authentication.
// NOTE: Digest authentication requires challenge-response negotiation.
// This middleware sets the initial Authorization header; for full digest
// flow, use a custom http.RoundTripper or middleware instead.
func WithDigestAuth(credentials string) openapigo.Middleware {
    return openapigo.MiddlewareFunc(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
        req.Header.Set("Authorization", "Digest "+credentials)
        return next(req)
    })
}
```

For complex authentication flows (digest challenge-response, negotiate/SPNEGO), the generated middleware provides only the initial header. A doc comment recommends using a specialized `http.RoundTripper` for the full protocol.

### API Key Variants

API keys can be sent in different locations:

```go
// apiKey in header
func WithAPIKeyHeader(name, value string) openapigo.Middleware { ... }

// apiKey in query parameter
func WithAPIKeyQuery(name, value string) openapigo.Middleware {
    return openapigo.MiddlewareFunc(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
        q := req.URL.Query()
        q.Set(name, value)
        req.URL.RawQuery = q.Encode()
        return next(req)
    })
}

// apiKey in cookie
func WithAPIKeyCookie(name, value string) openapigo.Middleware {
    return openapigo.MiddlewareFunc(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
        req.AddCookie(&http.Cookie{Name: name, Value: value})
        return next(req)
    })
}
```

### OAuth2

OAuth2 token management is deliberately **not** built into the generated code. The complexity of OAuth2 flows (token refresh, PKCE, scopes, token storage) varies wildly by application.

We generate:

1. **Per-flow metadata constants** (token URL, auth URL, scopes — namespaced by scheme name and flow type)
2. **Bearer middleware** (same as `WithBearerAuth`, named by scheme)
3. **Scope constants** (namespaced by scheme name **and flow type** — see generated code below)

When a scheme defines **multiple flows**, each flow's metadata is generated separately:

```go
// ===== Generated: auth.go =====

// OAuth2 flow metadata — namespaced by scheme name ("oauth2") and flow type
// Scheme "oauth2", flow "clientCredentials"
const OAuth2ClientCredentialsTokenURL = "https://auth.example.com/token"

// If the scheme also had an authorizationCode flow:
// const OAuth2AuthorizationCodeTokenURL = "https://auth.example.com/token"
// const OAuth2AuthorizationCodeAuthorizationURL = "https://auth.example.com/authorize"

// OAuth2 scopes — namespaced by scheme AND flow to avoid collisions
// when multiple flows define different scope sets.
// Flow processing order: flows are enumerated in alphabetical order
// (authorizationCode, clientCredentials, implicit, password) to ensure
// deterministic output regardless of YAML/JSON map iteration order.
const (
    OAuth2ClientCredentialsScopeRead  = "read"
    OAuth2ClientCredentialsScopeWrite = "write"
)

// OAuth2ClientCredentialsScopes lists scopes for the clientCredentials flow.
var OAuth2ClientCredentialsScopes = map[string]string{
    OAuth2ClientCredentialsScopeRead:  "Read access",
    OAuth2ClientCredentialsScopeWrite: "Write access",
}

// If the authorizationCode flow had different scopes (e.g., "profile", "email"):
// const OAuth2AuthorizationCodeScopeProfile = "profile"
// const OAuth2AuthorizationCodeScopeEmail   = "email"
// var OAuth2AuthorizationCodeScopes = map[string]string{...}

// WithOAuth2 returns middleware that adds OAuth2 Bearer token authentication.
// Token acquisition and refresh are the caller's responsibility.
// NOTE: This generates its own inline Bearer middleware — it does NOT depend
// on a separate "bearerAuth" scheme being defined in the spec.
func WithOAuth2(tokenFunc func(ctx context.Context) (string, error)) openapigo.Middleware {
    return openapigo.MiddlewareFunc(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
        token, err := tokenFunc(req.Context())
        if err != nil {
            return nil, fmt.Errorf("obtaining OAuth2 token: %w", err)
        }
        req.Header.Set("Authorization", "Bearer "+token)
        return next(req)
    })
}
```

Users integrate with their preferred OAuth2 library:

```go
import "golang.org/x/oauth2/clientcredentials"

cfg := &clientcredentials.Config{
    ClientID:     "my-client-id",
    ClientSecret: "my-client-secret",
    TokenURL:     api.OAuth2ClientCredentialsTokenURL,
    Scopes:       []string{api.OAuth2ClientCredentialsScopeRead, api.OAuth2ClientCredentialsScopeWrite},
}

client := openapigo.NewClient(
    openapigo.WithBaseURL("https://api.example.com"),
    openapigo.WithHTTPClient(cfg.Client(ctx)),
    // Or use middleware approach:
    // api.WithOAuth2(func(ctx context.Context) (string, error) {
    //     token, err := cfg.Token(ctx)
    //     return token.AccessToken, err
    // }),
)
```

### OpenID Connect

Similar to OAuth2 — we generate the discovery URL and use Bearer middleware:

```go
const OpenIDConnectURL = "https://auth.example.com/.well-known/openid-configuration"

// NOTE: Self-contained — does NOT depend on a separate "bearerAuth" scheme.
func WithOpenIDConnect(tokenFunc func(ctx context.Context) (string, error)) openapigo.Middleware {
    return openapigo.MiddlewareFunc(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
        token, err := tokenFunc(req.Context())
        if err != nil {
            return nil, fmt.Errorf("obtaining OIDC token: %w", err)
        }
        req.Header.Set("Authorization", "Bearer "+token)
        return next(req)
    })
}
```

### Mutual TLS

For `mutualTLS`, no middleware is needed — it's configured at the `http.Client` transport level:

```go
// Generated: documentation comment only
//
// This API requires mutual TLS authentication.
// Configure your http.Client with a TLS certificate:
//
//     cert, _ := tls.LoadX509KeyPair("client.crt", "client.key")
//     tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}}
//     httpClient := &http.Client{
//         Transport: &http.Transport{TLSClientConfig: tlsConfig},
//     }
//     client := openapigo.NewClient(
//         openapigo.WithHTTPClient(httpClient),
//     )
```

### Multiple Security Requirements (OR / AND)

OpenAPI security requirements support OR (multiple entries in the array) and AND (multiple schemes in one entry):

```yaml
# OR: either bearerAuth OR apiKey
security:
  - bearerAuth: []
  - apiKey: []

# AND: both headerAuth AND queryAuth required
security:
  - headerAuth: []
    queryAuth: []
```

Since auth is middleware-based, users compose as needed:

```go
// OR: user chooses one
client := openapigo.NewClient(
    openapigo.WithBaseURL("https://api.example.com"),
    openapigo.WithMiddleware(api.WithBearerAuth(token)), // OR
    // openapigo.WithMiddleware(api.WithAPIKeyAuth(key)), // choose one
)

// AND: user adds both
client := openapigo.NewClient(
    openapigo.WithBaseURL("https://api.example.com"),
    openapigo.WithMiddleware(api.WithHeaderAuth(headerKey), api.WithQueryAuth(queryKey)), // both applied
)
```

We generate doc comments on the client indicating which schemes are required:

```go
// GetAdminData requires authentication.
// Security (one of):
//   - bearerAuth (http/bearer)
//   - apiKey (header: X-API-Key)
var GetAdminData = openapigo.Endpoint[...]{...}
```

### Per-Operation Security Override

When an operation overrides the global security, we generate a doc comment on the endpoint variable:

```go
// ListPublicPets does not require authentication.
// Security: none
var ListPublicPets = openapigo.Endpoint[...]{...}

// DeletePet requires admin authentication.
// Security: oauth2 (scopes: admin)
var DeletePet = openapigo.Endpoint[...]{...}
```

We do **not** enforce security requirements at runtime — the middleware approach means the user is responsible for configuring auth. This is consistent with the thin wrapper philosophy.

## Consequences

### Positive

- **Generated convenience functions**: users don't manually construct auth headers
- **Dynamic tokens supported**: `WithBearerAuthFunc` accepts a function for token refresh
- **OAuth2 is not over-engineered**: we provide metadata, users bring their preferred OAuth2 library
- **Standard Go patterns**: `http.Client` for mTLS, middleware for headers
- **Composable**: multiple auth schemes via multiple middleware

### Negative

- **No automatic security enforcement**: if a user forgets to add auth middleware, they get a 401 at runtime, not a compile error
- **OAuth2 requires external library**: users must integrate `golang.org/x/oauth2` or similar themselves
- **Doc comments are the only security "enforcement"**: the generator documents requirements but doesn't enforce them

### Risks

- Users may misconfigure OR vs AND security requirements. We generate clear doc comments to guide them.
- Token refresh races: if `tokenFunc` is slow and multiple requests fire concurrently, each may call `tokenFunc` independently. This is the user's responsibility to handle (e.g., via `sync.Mutex`, `singleflight.Group`, or a token cache with expiry-aware refresh).
