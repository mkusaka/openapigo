# ADR-016: Request Body and Media Types

## Status

Accepted

## Date

2026-03-01

## Context

OpenAPI operations can accept request bodies in various media types:

| Media type | Usage | Go handling |
|-----------|-------|-------------|
| `application/json` | Most common API payload | `json.Marshal` |
| `application/x-www-form-urlencoded` | HTML form submission | `url.Values` |
| `multipart/form-data` | File uploads + form fields | `multipart.Writer` |
| `application/octet-stream` | Raw binary | `io.Reader` |
| `text/plain` | Plain text | `string` or `[]byte` |
| `application/xml` | XML payload | Out of scope (initial version) |

An operation can support **multiple media types** for the same request body, and the consumer must choose which to use.

## Decision

### Single Media Type (Common Case)

When a request body has exactly one media type, the body field in the request struct uses the `body` struct tag:

```yaml
requestBody:
  required: true
  content:
    application/json:
      schema:
        $ref: '#/components/schemas/CreatePetBody'
```

```go
type CreatePetRequest struct {
    Body CreatePetBody `body:"application/json"`
}

type CreatePetBody struct {
    Name string  `json:"name"`
    Tag  *string `json:"tag,omitzero"`
}
```

The runtime's `Do()` function reads the `body` tag, selects the appropriate serializer, and sets `Content-Type` automatically.

### application/json

The default and most common case. The body is serialized via `json.Marshal`:

```go
// Runtime behavior:
// 1. json.Marshal(req.Body)
// 2. Set Content-Type: application/json
// 3. Set body on http.Request
```

### application/x-www-form-urlencoded

Form-encoded bodies serialize struct fields as URL-encoded key-value pairs:

```yaml
requestBody:
  content:
    application/x-www-form-urlencoded:
      schema:
        type: object
        required: [grant_type]
        properties:
          grant_type: { type: string }
          username: { type: string }
          password: { type: string }
```

```go
type TokenRequest struct {
    Body TokenRequestBody `body:"application/x-www-form-urlencoded"`
}

type TokenRequestBody struct {
    GrantType string  `form:"grant_type"`
    Username  *string `form:"username,omitzero"`
    Password  *string `form:"password,omitzero"`
}
```

The `form` struct tag is used instead of `json`. The runtime serializes to `url.Values`:

```go
// Runtime behavior:
// 1. Reflect on Body, collect form-tagged fields
// 2. Encode as url.Values
// 3. Set Content-Type: application/x-www-form-urlencoded
```

### multipart/form-data (File Uploads)

Multipart forms mix file uploads with regular form fields:

```yaml
requestBody:
  content:
    multipart/form-data:
      schema:
        type: object
        required: [file]
        properties:
          file:
            type: string
            format: binary
          description:
            type: string
          tags:
            type: array
            items: { type: string }
```

```go
type UploadRequest struct {
    Body UploadRequestBody `body:"multipart/form-data"`
}

type UploadRequestBody struct {
    File        openapigo.File `form:"file"`
    Description *string        `form:"description,omitzero"`
    Tags        []string       `form:"tags,omitzero"`
}
```

The `openapigo.File` type represents a file upload:

```go
// In the runtime library
type File struct {
    Name   string    // filename
    Reader io.Reader // file content (if io.ReadCloser, closed by runtime after send)
}

// Convenience constructors
//
// FileFromPath opens a file for reading. The returned File.Reader
// implements io.ReadCloser; the runtime's Do() calls Close() on the
// reader after the request body is fully written.
// If the caller does not call Do(), they are responsible for closing
// the underlying *os.File themselves to avoid a resource leak.
func FileFromPath(path string) (File, error) {
    f, err := os.Open(path)
    if err != nil {
        return File{}, err
    }
    return File{Name: filepath.Base(path), Reader: f}, nil
    // f will be closed by the runtime after the request body is sent.
    // If the user does not call Do(), they must close f themselves.
}

func FileFromBytes(name string, data []byte) File {
    return File{Name: name, Reader: bytes.NewReader(data)}
}
```

Runtime behavior:

```go
// 1. Create multipart.Writer
// 2. For each form field:
//    - File fields: writer.CreateFormFile(name, file.Name) + io.Copy
//    - String fields: writer.WriteField(name, value)
//    - Array fields: multiple writer.WriteField calls
// 3. Set Content-Type: multipart/form-data; boundary=...
```

### Multiple Files

When a property is an array of binary strings:

```yaml
files:
  type: array
  items:
    type: string
    format: binary
```

```go
type UploadRequestBody struct {
    Files []openapigo.File `form:"files"`
}
```

Each file is written as a separate form part with the same field name.

### application/octet-stream (Raw Binary)

Raw binary bodies use `io.Reader` directly (not `openapigo.File`, which is for multipart forms only):

```yaml
requestBody:
  content:
    application/octet-stream:
      schema:
        type: string
        format: binary
```

```go
type UploadRawRequest struct {
    Body io.Reader `body:"application/octet-stream"`
}
```

When the `application/octet-stream` body is optional (`requestBody.required` is `false` or absent), the runtime must detect whether a body was provided. Since `io.Reader` is an interface, a **typed nil** (e.g., `var f *os.File = nil; Body = f`) creates a non-nil interface with a nil underlying value — this is indistinguishable from "body provided" via simple `!= nil` check. To handle this correctly, the runtime uses `reflect.ValueOf(body).IsNil()` to detect both true nil interfaces and typed nils:

```go
type OptionalUploadRequest struct {
    Body io.Reader `body:"application/octet-stream,omitzero"` // nil = no body sent
}

// In the runtime's Do():
// isNilReader checks for both nil interface and typed-nil interface.
func isNilReader(r io.Reader) bool {
    if r == nil {
        return true
    }
    rv := reflect.ValueOf(r)
    // Check all nillable kinds, not just Ptr. A typed nil can occur
    // with any nillable interface implementor (e.g., a nil func, map,
    // slice, or chan assigned to io.Reader via a wrapper type).
    switch rv.Kind() {
    case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
        return rv.IsNil()
    }
    return false
}
```

### text/plain

Plain text bodies use `string`:

```yaml
requestBody:
  content:
    text/plain:
      schema:
        type: string
```

```go
type SendMessageRequest struct {
    Body string `body:"text/plain"`
}
```

### Multiple Media Types

When an operation accepts multiple media types, we generate a body type that can hold any of the alternatives:

```yaml
requestBody:
  content:
    application/json:
      schema:
        $ref: '#/components/schemas/Pet'
    multipart/form-data:
      schema:
        type: object
        properties:
          file:
            type: string
            format: binary
          metadata:
            type: string
```

```go
type CreatePetRequest struct {
    Body CreatePetRequestBody
}

// CreatePetRequestBody supports multiple media types.
// Set exactly one field.
type CreatePetRequestBody struct {
    JSON      *Pet                          `body:"application/json"`
    Multipart *CreatePetMultipartBody       `body:"multipart/form-data"`
}

type CreatePetMultipartBody struct {
    File     openapigo.File `form:"file"`
    Metadata *string        `form:"metadata,omitzero"`
}
```

The runtime selects the media type based on which field is non-nil. If **multiple fields** are set, the runtime returns an error (ambiguous media type selection). If **zero fields** are set (all nil): the behavior depends on whether the request body is required. When `requestBody.required: true`, this is an error (no body to send). When `requestBody.required: false` (or absent), no request body is sent (the entire multi-media-type body is optional, and all-nil means "no body"):

```go
// Usage: JSON
jsonReq := api.CreatePetRequest{
    Body: api.CreatePetRequestBody{
        JSON: &api.Pet{Name: "Fluffy"},
    },
}

// Usage: Multipart
file, _ := openapigo.FileFromPath("photo.jpg")
multipartReq := api.CreatePetRequest{
    Body: api.CreatePetRequestBody{
        Multipart: &api.CreatePetMultipartBody{
            File: file,
            Metadata: ptr("pet photo"),
        },
    },
}
```

### Encoding Object (Per-Field Content-Type in Multipart)

OpenAPI's `encoding` object allows specifying per-field content types and serialization in multipart forms:

```yaml
requestBody:
  content:
    multipart/form-data:
      schema:
        type: object
        properties:
          metadata:
            type: object
            properties:
              name: { type: string }
          file:
            type: string
            format: binary
      encoding:
        metadata:
          contentType: application/json
```

```go
type UploadRequestBody struct {
    Metadata UploadMetadata `form:"metadata,json"` // serialized as JSON within the multipart part
    File     openapigo.File `form:"file"`
}
```

The `json` modifier in the `form` tag tells the runtime to JSON-serialize the field's value as the part body, with `Content-Type: application/json` on that specific part.

### Optional Request Body

When `requestBody.required` is `false` (or absent) **and** the body uses a schema reference (e.g., `$ref: '#/components/schemas/PetUpdate'`), the body field is a pointer. For `application/octet-stream` with optional body, `io.Reader` is used with nil detection via `isNilReader` (see above). The pointer pattern applies specifically to JSON/form/multipart bodies:

```yaml
requestBody:
  content:
    application/json:
      schema:
        $ref: '#/components/schemas/PetUpdate'
  # required is absent (defaults to false)
```

```go
type UpdatePetRequest struct {
    PetID string          `path:"petId"`
    Body  *PetUpdateBody  `body:"application/json,omitzero"`
}
```

When `Body` is nil, no request body is sent.

### Content-Type Header

The `Content-Type` header is **always set automatically** by the runtime based on the `body` tag. Users should not set it manually. If middleware needs to override it, it can modify the request in the middleware chain.

## Consequences

### Positive

- **Common case (JSON) is trivial**: just a struct field with `body:"application/json"`
- **File uploads are first-class**: `openapigo.File` with `io.Reader` is natural Go
- **Multiple media types are type-safe**: only one field can be set, compiler guides usage
- **Form encoding uses `form` tags**: familiar pattern, similar to `json` tags

### Negative

- **Multiple media types generate extra types**: one wrapper struct + one struct per media type
- **`openapigo.File` is a runtime dependency**: users must import the runtime for file uploads
- **`format: binary` has context-dependent mapping**: The Go type for `format: binary` depends on the enclosing media type context:

  | Media type context | Go type | Rationale |
  |---|---|---|
  | `application/json` | `[]byte` | Base64-encoded in JSON; `encoding/json` handles `[]byte` ↔ base64 natively. **OAS 3.1 note**: JSON Schema 2020-12 uses `contentEncoding: base64` (not `format: binary`) as the primary mechanism for binary data in JSON. When `contentEncoding: base64` is present, the type is `[]byte` (same mapping). `format: binary` inside `application/json` is a **compatibility interpretation** from OAS 3.0. The generator recognizes both and maps to `[]byte`. |
  | `multipart/form-data` | `openapigo.File` | File upload part with filename and streaming reader |
  | `application/octet-stream` | `io.Reader` | Raw binary stream, not wrapped in File |

  Note: ADR-009's type table lists `format: binary` → `[]byte` as the **context-free default**. The context-dependent overrides above take precedence when the generator knows the media type. The generator determines the mapping based on the media type context, not the schema alone.

  **$ref reuse across media types**: When a schema defined via `$ref` (e.g., `FileContent: {type: string, format: binary}`) is used in both `application/json` and `multipart/form-data` contexts, the generator produces **separate Go types** for each context. The naming is **always context-qualified**: `FileContentJSON` ([]byte, for JSON context) and `FileContentUpload` (openapigo.File, for multipart context). No unqualified base-name alias is generated — every type name includes its context suffix. This avoids a stability problem where the alias target could change when new media type usages are added (e.g., adding an `application/octet-stream` usage would change which context is "most common", silently changing the alias's underlying type). **Stability guarantee**: Adding a new media type usage for the same `$ref` does NOT rename existing types — it only adds a new context-qualified type. This prevents non-breaking API spec changes (adding a new media type) from causing breaking Go type name changes.
- **XML is not supported initially**: declared out of scope. Can be added later with `body:"application/xml"` + xml struct tags.

### Risks

- Streaming request bodies (chunked transfer encoding) are not explicitly handled. Users can implement streaming via `io.Reader` with `application/octet-stream`. For streaming JSON (newline-delimited), a future ADR may address this.
- Very large file uploads: The `io.Reader` approach for `application/octet-stream` naturally supports streaming. For `multipart/form-data`, `multipart.Writer` writes directly to the underlying `io.Writer` (it does **not** buffer the entire content internally). However, the runtime's `Do()` function must provide an `io.Writer` to the `multipart.Writer`. The runtime uses `io.Pipe()` to connect the `multipart.Writer` (writing in a goroutine) to the `http.Request.Body` (reading), enabling true streaming without buffering the entire multipart body in memory. **Content-Length limitation**: With `io.Pipe`, the total body size is unknown upfront, so Go's `http.Client` uses chunked transfer encoding (`Transfer-Encoding: chunked`). Some APIs/proxies require `Content-Length` and reject chunked requests. For these cases, the runtime provides `openapigo.WithBufferedMultipart()` client option which pre-buffers the multipart body into a `bytes.Buffer` to compute `Content-Length`. This trades memory usage for compatibility. The default is streaming (io.Pipe); users opt in to buffering when their API requires `Content-Length`. For `application/octet-stream`, users can provide a reader that implements `io.Seeker` (e.g., `*os.File`) and the runtime will compute `Content-Length` via `Seek` without full buffering.
