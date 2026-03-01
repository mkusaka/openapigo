# ADR-015: Parameter Serialization (style / explode)

## Status

Accepted

## Date

2026-03-01

## Context

OpenAPI defines how parameters are serialized into HTTP requests using `style` and `explode` keywords. Different combinations produce very different URL encodings:

### style × explode Matrix for Query Parameters

Given `color = ["blue", "black", "brown"]`:

| style | explode | Serialization |
|-------|---------|---------------|
| `form` (default) | `true` (default) | `?color=blue&color=black&color=brown` |
| `form` | `false` | `?color=blue,black,brown` |
| `spaceDelimited` | `false` | `?color=blue%20black%20brown` |
| `pipeDelimited` | `false` | `?color=blue\|black\|brown` |
| `deepObject` | `true` | N/A — see note² |

² `deepObject` with array values is **undefined** in the OpenAPI specification (OAS 3.0.x §4.8.12.1 defines `deepObject` only for `type: object`). The generator emits a generation-time error for this combination. See Risks section for details.

Given `point = {x: 1, y: 2}`:

| style | explode | Serialization |
|-------|---------|---------------|
| `form` | `true` | `?x=1&y=2` |
| `form` | `false` | `?point=x,1,y,2` |
| `deepObject` | `true` | `?point[x]=1&point[y]=2` |

### style × explode Matrix for Path Parameters

Given `color = ["blue", "black", "brown"]`:

| style | explode | Serialization |
|-------|---------|---------------|
| `simple` (default) | `false` (default) | `blue,black,brown` |
| `simple` | `true` | `blue,black,brown` |
| `label` | `false` | `.blue,black,brown` |
| `label` | `true` | `.blue.black.brown` |
| `matrix` | `false` | `;color=blue,black,brown` |
| `matrix` | `true` | `;color=blue;color=black;color=brown` |

### Header and Cookie Parameters

| Location | Default style | Behavior |
|----------|--------------|----------|
| `header` | `simple` | Comma-separated values |
| `cookie` | `form` | Standard cookie encoding |

### Defaults per location

| Location | Default `style` | Default `explode` |
|----------|----------------|-------------------|
| `path` | `simple` | `false` |
| `query` | `form` | `true` |
| `header` | `simple` | `false` |
| `cookie` | `form` | `true`¹ |

¹ **Cookie explode default**: Per the OpenAPI spec, cookie uses `style: form` which defaults to `explode: true`. However, [OAI/OpenAPI-Specification#1528](https://github.com/OAI/OpenAPI-Specification/issues/1528) documents that `explode: true` produces serialization incompatible with most cookie parsers (RFC 6570 cookies expect `name=a,b,c`, not `name=a&name=b&name=c`). **Our behavior**: When the spec explicitly sets `explode: true` on a cookie parameter, we honor it (tag: `cookie:"name,explode"`). When `explode` is **not specified** (implicit default) **and the parameter schema is `array` or `object` type**, the generator emits a **generation-time error** (not just a warning) requiring the user to make an explicit choice: `ERROR: cookie parameter "name" (array/object type) has no explicit 'explode' setting. The OpenAPI default (explode: true) is incompatible with most cookie parsers. Add 'explode: false' (recommended) or 'explode: true' (spec-literal) to the parameter definition.` This fail-fast approach prevents silent interoperability issues that a mere warning (easily overlooked in CI) would allow. **Primitive types** (string, integer, number, boolean) are exempt from this error because `explode` has no effect on their serialization — a single value serializes the same way regardless of `explode`.

## Decision

### Struct Tags Encode Serialization Style

The generated request struct tags encode the parameter location, name, and serialization style:

```
`path:"name"`                     → simple, explode=false (defaults)
`path:"name,label"`               → label, explode=false
`path:"name,matrix,explode"`      → matrix, explode=true
`query:"name"`                    → form, explode=true (defaults)
`query:"name,explode=false"`      → form, explode=false
`query:"name,pipe"`               → pipeDelimited, explode=false
`query:"name,space"`              → spaceDelimited, explode=false
`query:"name,deep"`               → deepObject, explode=true
`header:"name"`                   → simple (only option)
`cookie:"name"`                   → form, explode=false (generated only when spec has explicit `explode: false` — see ¹ in defaults table)
```

### Generated Examples

**Path parameters:**

```yaml
parameters:
  - name: petId
    in: path
    required: true
    schema: { type: string }
  - name: colors
    in: path
    style: label
    explode: true
    schema:
      type: array
      items: { type: string }
```

```go
type GetPetRequest struct {
    PetID  string   `path:"petId"`
    Colors []string `path:"colors,label,explode"`
}
```

**Query parameters:**

```yaml
parameters:
  - name: limit
    in: query
    schema: { type: integer }
  - name: tags
    in: query
    explode: false
    schema:
      type: array
      items: { type: string }
  - name: filter
    in: query
    style: deepObject
    explode: true
    schema:
      type: object
      properties:
        status: { type: string }
        minAge: { type: integer }
```

```go
type ListPetsRequest struct {
    Limit  *int            `query:"limit,omitzero"`
    Tags   []string        `query:"tags,explode=false,omitzero"`
    Filter *ListPetsFilter `query:"filter,deep,omitzero"`
}

type ListPetsFilter struct {
    Status *string `json:"status,omitzero"`
    MinAge *int    `json:"minAge,omitzero"`
}
```

### Runtime Serializer

The runtime library provides serializers for each style:

```go
// Internal serializer registry
type paramSerializer struct {
    // Cached per-type metadata (computed once via sync.Once)
    cache sync.Map
}

func (s *paramSerializer) serializePath(tmpl string, req any) (string, error) {
    // Reflect on req struct, find fields with `path` tags
    // Serialize each field according to its style/explode
    // Replace {name} placeholders in template
    // ...
    return tmpl, nil
}

func (s *paramSerializer) serializeQuery(req any) (url.Values, error) {
    // Reflect on req struct, find fields with `query` tags
    // Serialize each field according to its style/explode
    // Return url.Values
    // ...
    return nil, nil
}

func (s *paramSerializer) serializeHeaders(req any) (http.Header, error) {
    // Reflect on req struct, find fields with `header` tags
    // Serialize each field
    // Return http.Header
    // ...
    return nil, nil
}
```

### Serialization Implementation

For each style, the serializer follows the OpenAPI specification. **Note**: The path serializers shown below are simplified; production implementations apply `pathEscape` to individual values before joining with delimiters (see URL Encoding section below). Query serializers use `url.Values` as a data structure for collecting key-value pairs, but the final URL string is built using the custom `queryEscape` function (see URL Encoding section below), NOT `url.Values.Encode()` (which encodes spaces as `+` instead of `%20`):

```go
// query, form style, explode=true (default)
// []string{"a", "b"} → "name=a&name=b"
func serializeFormExplode(name string, values []string) url.Values {
    v := url.Values{}
    for _, val := range values {
        v.Add(name, val)
    }
    return v
}

// query, form style, explode=false
// []string{"a", "b"} → "name=a,b"
func serializeFormNoExplode(name string, values []string) url.Values {
    return url.Values{name: {strings.Join(values, ",")}}
}

// query, deepObject style, explode=true
// map[string]string{"x": "1", "y": "2"} → "name[x]=1&name[y]=2"
func serializeDeepObject(name string, obj map[string]string) url.Values {
    v := url.Values{}
    for key, val := range obj {
        v.Set(fmt.Sprintf("%s[%s]", name, key), val)
    }
    return v
}

// path, simple style, explode=false (default)
// []string{"a", "b"} → "a,b"
func serializeSimple(values []string) string {
    return strings.Join(values, ",")
}

// path, label style, explode=true
// []string{"a", "b"} → ".a.b"
func serializeLabelExplode(values []string) string {
    return "." + strings.Join(values, ".")
}

// path, matrix style, explode=true
// []string{"a", "b"}, name="color" → ";color=a;color=b"
func serializeMatrixExplode(name string, values []string) string {
    var b strings.Builder
    for _, val := range values {
        b.WriteString(";")
        b.WriteString(name)
        b.WriteString("=")
        b.WriteString(val)
    }
    return b.String()
}
```

### Type-to-String Conversion

Primitive types are converted to strings using `fmt.Sprint` or specialized formatters:

| Go type | Conversion |
|---------|-----------|
| `string` | as-is |
| `int`, `int32`, `int64` | `strconv.FormatInt` |
| `float32`, `float64` | `strconv.FormatFloat` |
| `bool` | `strconv.FormatBool` |
| `time.Time` | RFC 3339 |
| `Date` (custom) | `2006-01-02` |
| `[]T` | serialize each element, join per style |
| `struct` | serialize each field as key-value per style |
| `*T` | dereference, skip if nil |
| `Nullable[T]` | skip if not set, serialize value if set |

### URL Encoding

All query parameter values are URL-encoded using a custom `queryEscape` function that percent-encodes per RFC 3986 (encoding spaces as `%20`, not `+`). Go's `url.QueryEscape` uses the WHATWG `application/x-www-form-urlencoded` encoding (spaces as `+`), which does not match the OpenAPI specification's expectation of RFC 3986 percent-encoding. Path parameter **individual values** (each element in an array, each key/value in an object) are URL-encoded using a custom `pathEscape` function (see below) **before** joining with style delimiters.

```go
// queryEscape percent-encodes a string per RFC 3986 for query values.
// Unlike url.QueryEscape, this encodes spaces as %20 (not +) and only
// encodes characters outside the RFC 3986 unreserved set.
func queryEscape(s string) string {
    var b strings.Builder
    for i := 0; i < len(s); i++ {
        ch := s[i]
        if isUnreserved(ch) {
            b.WriteByte(ch)
        } else {
            fmt.Fprintf(&b, "%%%02X", ch)
        }
    }
    return b.String()
}

// pathEscape percent-encodes a string per RFC 3986 for path segments.
// Unlike url.PathEscape, this encodes all characters except the RFC 3986
// unreserved set (ALPHA / DIGIT / "-" / "." / "_" / "~"). Go's
// url.PathEscape allows additional characters (sub-delims, ':', '@')
// that are technically valid in path segments but should be encoded
// when they are part of parameter values (not structural path characters).
func pathEscape(s string) string {
    var b strings.Builder
    for i := 0; i < len(s); i++ {
        ch := s[i]
        if isUnreserved(ch) {
            b.WriteByte(ch)
        } else {
            fmt.Fprintf(&b, "%%%02X", ch)
        }
    }
    return b.String()
}

// isUnreserved returns true for RFC 3986 unreserved characters.
func isUnreserved(ch byte) bool {
    return (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') ||
        (ch >= '0' && ch <= '9') || ch == '-' || ch == '.' || ch == '_' || ch == '~'
}

// isHex returns true if ch is a hexadecimal digit (0-9, A-F, a-f).
func isHex(ch byte) bool {
    return (ch >= '0' && ch <= '9') || (ch >= 'A' && ch <= 'F') || (ch >= 'a' && ch <= 'f')
}

// isRFC3986Allowed returns true for characters allowed in RFC 3986 URIs
// (unreserved + reserved). Characters outside this set (control characters,
// non-ASCII bytes, space, {, }, |, \, ^, `) must be percent-encoded.
func isRFC3986Allowed(ch byte) bool {
    if isUnreserved(ch) {
        return true
    }
    // RFC 3986 reserved = gen-delims / sub-delims
    // gen-delims: : / ? # [ ] @
    // sub-delims: ! $ & ' ( ) * + , ; =
    switch ch {
    case ':', '/', '?', '#', '[', ']', '@',
        '!', '$', '&', '\'', '(', ')', '*', '+', ',', ';', '=':
        return true
    }
    return false
}
``` The delimiters themselves (`,` for simple, `.` for label, `;` and `=` for matrix) are **not** encoded — they are part of the serialization format, not part of the value.

**Important**: `pathEscape` (our custom function) encodes `,` and `;`, which are style delimiters. Therefore, the serializer must:
1. Encode each **individual value** with `pathEscape`
2. Join the encoded values with the style delimiter (literal, not encoded)
3. NOT call `url.PathEscape` on the entire serialized result

```go
// CORRECT: encode values individually, then join with literal delimiter
func serializeSimple(values []string) string {
    encoded := make([]string, len(values))
    for i, v := range values {
        encoded[i] = pathEscape(v) // custom RFC 3986 unreserved-only encoder
    }
    return strings.Join(encoded, ",") // literal comma, not encoded
}
```

Reserved characters (`:`, `/`, `?`, `#`, `[`, `]`, `@`, `!`, `$`, `&`, `'`, `(`, `)`, `*`, `+`, `,`, `;`, `=`) are handled according to `allowReserved`:

```yaml
parameters:
  - name: path
    in: query
    allowReserved: true
    schema: { type: string }
```

```go
// allowReserved=true: do not encode most reserved characters
type Request struct {
    Path string `query:"path,allowReserved"`
}
```

**Important**: When `allowReserved: true` is set, we **cannot** use `url.Values.Encode()` because it always percent-encodes reserved characters. Instead, we build the query string manually. However, even with `allowReserved: true`, certain characters **must still be encoded** because they have structural meaning in query strings:

| Character | Reason | Encoded? |
|---|---|---|
| `&` | Separates query parameters | **Yes** — must encode as `%26` |
| `#` | Starts the fragment identifier | **Yes** — must encode as `%23` |
| `%` (bare, not part of `%HH`) | Ambiguous percent-encoding | **Yes** — must encode as `%25` (see below) |
| `=` | Separates key from value | **Always** encoded as `%3D`. While RFC 3986 `sub-delims` includes `=` as technically allowed in query values, many servers/proxies/middleware split on all `=` characters, which can misparse values containing `=` (e.g., Base64 tokens, signatures). Encoding `=` unconditionally maximizes interoperability with no downside (servers that handle `=` correctly also handle `%3D`). |
| `+` | Ambiguous: space in form encoding | **Yes** — must encode as `%2B`. Although `+` is a valid `sub-delim` in RFC 3986, many servers apply `application/x-www-form-urlencoded` decoding to query strings, interpreting `+` as a space. Encoding `+` unconditionally avoids this ambiguity. |
| `[` | gen-delim not in `pchar` | **Yes** — must encode as `%5B`. RFC 3986 `query = *( pchar / "/" / "?" )` and `pchar` does not include `[`. Although `[` is in the `reserved` set, it is a gen-delim used for IPv6 address syntax in the authority component, not for query values. |
| `]` | gen-delim not in `pchar` | **Yes** — must encode as `%5D`. Same reasoning as `[`. |
| All others in RFC 3986 reserved set | Part of the value | No (pass through) |

**Percent-sign handling**: When `allowReserved: true`, existing percent-encoded triplets (`%HH` where H is a hex digit) in the value must be preserved as-is (they represent already-encoded characters, per RFC 6570 `+` operator semantics). A bare `%` that is NOT followed by two hex digits must be encoded as `%25` to avoid creating an invalid percent-encoding sequence.

```go
func serializeQueryAllowReserved(name, value string) string {
    // Encode characters that break query string structure or are outside RFC 3986:
    // & (parameter separator), # (fragment start), bare % (invalid encoding),
    // = (key-value separator), + (form-encoding ambiguity), [ and ] (gen-delims
    // not in pchar). All other RFC 3986 reserved characters (:/@!$'()*,;) pass
    // through unencoded. Existing %HH triplets are preserved (not double-encoded).
    // Characters outside RFC 3986's unreserved + reserved sets (control characters,
    // non-ASCII bytes, space, {, }, |, \, ^, `) are percent-encoded.
    var b strings.Builder
    for i := 0; i < len(value); i++ {
        ch := value[i]
        switch {
        case ch == '&':
            b.WriteString("%26")
        case ch == '#':
            b.WriteString("%23")
        case ch == '=':
            b.WriteString("%3D")
        case ch == '+':
            b.WriteString("%2B")
        case ch == '[':
            b.WriteString("%5B")
        case ch == ']':
            b.WriteString("%5D")
        case ch == '%':
            // Preserve valid %HH triplets; encode bare %
            if i+2 < len(value) && isHex(value[i+1]) && isHex(value[i+2]) {
                b.WriteByte('%')
            } else {
                b.WriteString("%25")
            }
        case isRFC3986Allowed(ch):
            // RFC 3986 unreserved + reserved (except &, #, =, % handled above)
            b.WriteByte(ch)
        default:
            // Encode non-RFC3986 characters: control chars, non-ASCII, space,
            // {, }, |, \, ^, ` etc.
            b.WriteString(fmt.Sprintf("%%%02X", ch))
        }
    }
    return name + "=" + b.String()
}
```

This requires constructing the final URL query string ourselves rather than relying on `url.Values` for parameters with `allowReserved: true`.

### content Instead of schema

OpenAPI allows parameters to use `content` instead of `schema` for complex encoding. In this case, the parameter value is serialized as the specified media type (usually JSON):

```yaml
parameters:
  - name: filter
    in: query
    content:
      application/json:
        schema:
          type: object
          properties:
            status: { type: string }
```

```go
type ListRequest struct {
    Filter *ListRequestFilter `query:"filter,json,omitzero"` // serialized as JSON string
}

// Generated from the inline schema in the filter parameter's content
type ListRequestFilter struct {
    Status *string `json:"status,omitzero"`
}
```

The `json` modifier in the tag tells the serializer to `json.Marshal` the value and use the resulting string as the query parameter value.

### Performance: Cached Struct Metadata

Struct tag parsing and reflection happen once per type and are cached:

```go
type structMeta struct {
    pathFields   []fieldMeta
    queryFields  []fieldMeta
    headerFields []fieldMeta
    cookieFields []fieldMeta
    bodyField    *fieldMeta
}

type fieldMeta struct {
    index     int        // struct field index
    name      string     // parameter name
    style     string     // serialization style
    explode   bool
    omitZero  bool
    reserved  bool       // allowReserved
    jsonParam bool       // content: application/json
}

var metaCache sync.Map // map[reflect.Type]*structMeta
```

## Consequences

### Positive

- **All OpenAPI serialization styles supported**: form, simple, label, matrix, spaceDelimited, pipeDelimited, deepObject
- **Struct tags are declarative**: the generated code is self-documenting — you can read the tag to know how a parameter is serialized
- **Performance**: struct metadata is computed once per type, not per request
- **deepObject support**: nested query params (common in filtering APIs) work correctly

### Negative

- **Struct tags syntax is custom**: not a standard Go convention. Developers must learn the tag format. However, it's similar to `json` tags.
- **Reflection at runtime**: struct tag parsing uses `reflect`, which is slower than direct code. The caching mitigates this.
- **Complex tag strings**: a fully-specified tag like `query:"filter,deep,explode,omitzero"` is verbose

### Risks

- Some APIs use non-standard serialization. Our serializers strictly follow the OpenAPI spec. If a server deviates, users must use middleware to customize the request URL.
- `deepObject` with nested objects (objects within objects) is not well-specified in OpenAPI. We support one level of nesting and document this limitation.
- `deepObject` with array values is **undefined** in the OpenAPI specification. The spec only defines `deepObject` for `type: object` schemas (OAS 3.0.x §4.8.12.1). When `deepObject` is used with an array schema, the generator emits a **generation-time error**: `ERROR: deepObject style is only defined for object schemas. Parameter "name" has array schema. Use 'style: form' for array parameters.` This fail-fast approach prevents silent interoperability issues, as implementations disagree on array serialization (`name[0]=a` vs `name[]=a` vs other conventions).
