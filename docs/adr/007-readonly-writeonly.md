# ADR-007: readOnly and writeOnly Field Handling

## Status

Accepted

## Date

2026-03-01

## Context

OpenAPI schemas support `readOnly` and `writeOnly` annotations on properties:

| Annotation | Meaning (OAS 3.0) | Present in request? | Present in response? |
|-----------|---------|--------------------|--------------------|
| (none) | Normal field | Yes | Yes |
| `readOnly: true` | Server-managed field (e.g., `id`, `createdAt`) | SHOULD NOT be sent¹ | Yes |
| `writeOnly: true` | Sensitive input field (e.g., `password`) | Yes | SHOULD NOT be returned¹ |

¹ Per OAS 3.0/3.1, `readOnly`/`writeOnly` use "SHOULD NOT" language — they express expectations, not strict prohibitions. In OAS 3.1 (JSON Schema 2020-12), these are **annotation keywords** (not validation keywords).

**Generator behavior**: For **Go code generation purposes**, we treat these annotations as **hard exclusions** from request/response types respectively. This is a **pragmatic choice** (stricter than the spec's SHOULD NOT), matching the common expectation of API designers. This is a pragmatic choice: generating a request type that includes readOnly fields (because 3.1 technically allows it) would confuse users and defeat the purpose of the annotation. A future `--strict-3.1-annotations` flag could relax this to include readOnly fields as optional in request types.

### The problem

If we generate a single Go struct for a schema, that struct includes all fields regardless of readOnly/writeOnly. This means:

1. Request structs expose `id` and `createdAt` fields that the server ignores — misleading for users
2. Response structs expose `password` fields that the server never returns — misleading for users
3. No compile-time enforcement that readOnly fields aren't sent or writeOnly fields aren't read

### How existing tools handle this

- **openapi-typescript**: Uses marker types `$Read<T>` and `$Write<T>`. At call sites, `Readable<T>` strips write-only fields from responses and `Writable<T>` strips read-only fields from requests. This is elegant in TypeScript's structural type system.
- **oapi-codegen**: All fields in one struct. `readOnly` + `required` fields generate as pointers (configurable). No request/response type separation.
- **ogen**: Single struct. No differentiation.
- **openapi-generator**: Single struct. No differentiation.

### Approaches considered

**Approach A: Single type, all fields**
- Pros: Simple, minimal generated code
- Cons: No compile-time safety, misleading API

**Approach B: Separate request and response types**
- Pros: Maximum type safety, clear intent
- Cons: Type proliferation (potentially 2x types), complicates `$ref` resolution

**Approach C: Single base type + request/response type aliases with field subsetting**
- Pros: Balance of safety and simplicity
- Cons: Go lacks TypeScript's `Omit<T, K>` — must generate actual structs

## Decision

### Generate Separate Request and Response Types When Needed

When a schema **directly or transitively** contains `readOnly` or `writeOnly` fields, we generate **three types**:

1. **Base type**: Contains all fields (used for internal schema references)
2. **Request type**: Excludes `readOnly` fields (used in request bodies)
3. **Response type**: Excludes `writeOnly` fields (used in response bodies)

**"Directly or transitively"** means: a schema needs Request/Response variants if (a) it has its own readOnly/writeOnly fields, OR (b) any schema reachable through its `$ref` graph has readOnly/writeOnly fields. See "Nested `$ref` propagation" below for the full traversal scope.

When a schema has **no** readOnly/writeOnly fields (neither directly nor transitively), we generate only the base type and use it everywhere (no duplication).

### Example

```yaml
components:
  schemas:
    User:
      type: object
      required: [id, name, email]
      properties:
        id:
          type: string
          format: uuid
          readOnly: true
        name:
          type: string
        email:
          type: string
        password:
          type: string
          writeOnly: true
        createdAt:
          type: string
          format: date-time
          readOnly: true
```

```go
// Base type: all fields. Used for schema-level $ref resolution.
type User struct {
    ID        string    `json:"id"`
    Name      string    `json:"name"`
    Email     string    `json:"email"`
    Password  *string   `json:"password,omitzero"`
    CreatedAt time.Time `json:"createdAt"`
}

// Request type: excludes readOnly fields (id, createdAt)
type UserRequest struct {
    Name     string  `json:"name"`
    Email    string  `json:"email"`
    Password *string `json:"password,omitzero"`
}

// Response type: excludes writeOnly fields (password)
type UserResponse struct {
    ID        string    `json:"id"`
    Name      string    `json:"name"`
    Email     string    `json:"email"`
    CreatedAt time.Time `json:"createdAt"`
}
```

### When to Generate Which Type

The generator determines which type to use based on context:

| Context | Type used |
|---------|-----------|
| `requestBody` content schema | Request type (`UserRequest`) |
| `responses` content schema | Response type (`UserResponse`) |
| `parameters` (query, path, header) | Base type (readOnly/writeOnly rarely applies) |
| `$ref` in another schema's `properties` | Base type by default; Request/Response variant when the containing schema is in a request/response context and the referenced schema has readOnly/writeOnly fields (see Nested $ref in Risks) |
| Direct construction by user | Any (user's choice) |

### Operation-Level Typing

In generated client methods, the types are used correctly:

```go
func (c *Client) CreateUser(ctx context.Context, body UserRequest) (*UserResponse, error) {
    // body is UserRequest — no id, no createdAt fields exposed
    // return is *UserResponse — no password field exposed
    // ...
    return nil, nil
}

func (c *Client) GetUser(ctx context.Context, id string) (*UserResponse, error) {
    // return is *UserResponse — no password
    // ...
    return nil, nil
}

func (c *Client) UpdateUser(ctx context.Context, id string, body UserRequest) (*UserResponse, error) {
    // body: UserRequest, response: *UserResponse
    // ...
    return nil, nil
}
```

### Conversion Between Types

We generate conversion functions for moving between base, request, and response types:

```go
// Convert base type to request type (drops readOnly fields)
func (u User) ToRequest() UserRequest {
    return UserRequest{
        Name:     u.Name,
        Email:    u.Email,
        Password: u.Password,
    }
}

// Convert base type to response type (drops writeOnly fields)
func (u User) ToResponse() UserResponse {
    return UserResponse{
        ID:        u.ID,
        Name:      u.Name,
        Email:     u.Email,
        CreatedAt: u.CreatedAt,
    }
}
```

### Naming Convention

| Type | Naming pattern |
|------|---------------|
| Base | `{SchemaName}` (e.g., `User`) |
| Request | `{SchemaName}Request` (e.g., `UserRequest`) |
| Response | `{SchemaName}Response` (e.g., `UserResponse`) |

If the schema name already ends in `Request` or `Response`, we apply suffix rules to avoid collision with both the schema variants and operation-level types (ADR-014):

| Schema name | Request type | Response type |
|------------|-------------|--------------|
| `UserRequest` | `UserRequestInput` | `UserRequestOutput` |
| `UserResponse` | `UserResponseInput` | `UserResponseOutput` |

The generator maintains a global set of all generated type names and detects collisions. When a collision occurs, it applies disambiguating suffixes (`Input`/`Output` instead of `Request`/`Response`). A warning is emitted during generation.

### No readOnly/writeOnly → No Extra Types

When a schema has no readOnly or writeOnly fields, only the base type is generated:

```yaml
Pet:
  type: object
  properties:
    name: { type: string }
    tag: { type: string }
```

```go
// Only one type — no Request/Response variants needed
type Pet struct {
    Name string  `json:"name"`
    Tag  *string `json:"tag,omitzero"`
}
```

### readOnly + required Interaction

A field that is both `readOnly` and `required` is:

- **Required in the response** (server must include it)
- **Absent from the request type** (client doesn't send it)

In the response type, such fields are non-pointer (required). In the request type, they simply don't exist. This is straightforward with separate types and avoids the awkward "required but pointer" pattern that oapi-codegen uses.

```yaml
id:
  type: string
  readOnly: true
# id is in the 'required' array
```

```go
// UserResponse: id is required (non-pointer)
type UserResponse struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}

// UserRequest: id does not exist at all
type UserRequest struct {
    Name string `json:"name"`
}
```

### Schemas Used in Both Request and Response Context

When a schema is referenced in both `requestBody` and `responses` within the same operation, the appropriate variant is used in each position. The generator tracks where each `$ref` appears and applies the correct type.

### CLI Flag

`--no-read-write-types`: Disable request/response type separation. Generate a single type with all fields (for users who prefer simplicity over type safety).

## Consequences

### Positive

- **Compile-time safety**: impossible to accidentally send readOnly fields or read writeOnly fields
- **Clean API surface**: client methods have clear intent — request types for input, response types for output
- **No unnecessary types**: schemas without readOnly/writeOnly generate zero extra types
- **Correct `required` handling**: readOnly + required is no longer awkward

### Negative

- **Up to 3 types per schema**: increases generated code volume for schemas with readOnly/writeOnly
- **Conversion overhead**: moving between types requires copying fields
- **$ref resolution complexity**: the generator must track reference context to determine which type variant to use

### Risks

- **Nested `$ref` propagation**: When a schema is used in a request context, any `$ref`-ed schema within its `properties` that contains readOnly/writeOnly fields uses the **Request/Response variant** (not the Base type). The generator performs **transitive analysis** by walking the full `$ref` graph depth-first through **all reference contexts**: `properties`, `items`, `additionalProperties`, `allOf`/`oneOf`/`anyOf` branches, `prefixItems`, `if`/`then`/`else` schemas, and `dependentSchemas`. **Excluded**: `not` schemas are NOT traversed for variant generation because `not` is validation-only per ADR-002 — schemas reachable only through `not` do not contribute fields to the generated struct and therefore do not affect readOnly/writeOnly type splitting. **Cycle termination**: The traversal maintains a `visited` set of schema identifiers (canonical `$ref` paths). When a schema already in the visited set is encountered, the traversal stops for that branch (returns the previously-computed result). This guarantees termination for self-referential and mutually-recursive schemas. The visited set is initialized per top-level analysis run and is not shared across independent schema analyses. Any schema reachable through any of these reference paths that directly or transitively contains readOnly/writeOnly fields is marked as needing variant generation. Example: `Order.customer → User (has readOnly)` → `OrderRequest.customer` uses `UserRequest`. Example: `Order.items → []LineItem → Product (has readOnly)` → `LineItemRequest` is generated (with `ProductRequest`), and `OrderRequest.items` uses `[]LineItemRequest`. Example: `Order.customer → Profile (no readOnly) → User (has readOnly)` → `ProfileRequest` is generated (containing `UserRequest`), and `OrderRequest.customer` uses `ProfileRequest`.
- **Type name collision resolution**: Two independent collision categories are resolved with different strategies: (a) **Schema-level collisions** (schema named `UserRequest` conflicting with `User`'s generated Request variant): the variant uses `Input`/`Output` suffixes (`UserRequestInput`/`UserRequestOutput`). (b) **Operation-level collisions** (operation-generated `CreateUserRequest` type conflicting with a schema type): the operation type gets a `Params` suffix (`CreateUserRequestParams`). The generator maintains a global type name registry and applies the appropriate disambiguation based on collision category. **Determinism**: Schema-level types are processed in **alphabetical order** of their original schema names (canonical `$ref` paths). The first schema (alphabetically) that would produce a given Go type name wins the base name; subsequent colliders receive the disambiguated suffix. This ensures identical output across runs regardless of YAML/JSON map iteration order. **Convergence guarantee**: If the disambiguated name (e.g., `UserRequestInput`) itself collides with another existing type, a numeric suffix is appended (`UserRequestInput2`). This final numeric fallback is guaranteed to converge because the suffix increments until a unique name is found. The generator emits a warning when numeric suffixes are needed.
- **Type name collision with operation types**: A schema named `CreateUserRequest` would collide with an operation's generated `CreateUserRequest` type. The generator maintains a global type name registry across both schema-derived types and operation-derived types. When a collision is detected, the operation-level type is disambiguated with a `Params` suffix (e.g., `CreateUserRequestParams` for the operation request struct, while `CreateUserRequest` remains the schema type). A warning is emitted.
- Users who use the base type directly (ignoring Request/Response variants) lose the type safety benefits. The generated client methods use the correct variants, so this is mainly a risk for users constructing types manually.
