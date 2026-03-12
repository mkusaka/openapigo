# openapigo

`openapigo` is an OpenAPI 3 code generator and runtime for Go.

It provides:
- A CLI (`openapigo`) that generates Go client packages from OpenAPI specs.
- A runtime module (`github.com/mkusaka/openapigo`) used by generated code.

## Requirements

- Go 1.24+
- OpenAPI spec version: 3.0.x or 3.1.x

## Installation

Install the CLI:

```bash
go install github.com/mkusaka/openapigo/cmd/openapigo@latest
```

## Quick Start

Generate a Go package from an OpenAPI spec:

```bash
openapigo generate -i ./openapi.yaml -o ./api -package api
```

Then use the generated package with the runtime in your application.

## CLI

```text
Usage: openapigo <command> [options]

Commands:
  generate    Generate Go client code from an OpenAPI spec
  version     Print the generator version
```

`generate` flags:

| Flag | Description |
|------|-------------|
| `-i` | Path to an OpenAPI spec file |
| `-o` | Output directory for generated code |
| `-package` | Go package name (default: output directory name) |
| `--skip-validation` | Skip `Validate()` method generation |
| `--no-read-write-types` | Skip request/response variant type generation |
| `--dry-run` | Print generated file names and sizes without writing |
| `--format-mapping` | Custom format-to-type mapping (e.g. `uuid=github.com/google/uuid.UUID`) |
| `--strict-enums` | Generate validation for non-string enums |
| `--validate-on-unmarshal` | Generate `UnmarshalJSON` methods that call `Validate()` |
| `--resolve` | Resolve external `$ref` (file and URL) |
| `--allow-http` | Allow `http://` URLs for remote `$ref` (requires `--resolve`) |
| `--resolve-header` | Custom headers for remote `$ref` fetches (comma-separated `key:value`) |
| `--resolve-timeout` | Timeout for remote `$ref` fetches (e.g. `30s`, `1m`) |

## Features

### OpenAPI 3.0 / 3.1

- Schema-to-Go type mapping (object, array, primitive, enum)
- `allOf` / `oneOf` / `anyOf` composition (typed union wrappers with strategy-based discrimination)
- `$ref` resolution (local including deep JSON Pointer, file, and URL)
- `$id` / `$anchor` resolution with relative URI chain (OAS 3.1, RFC 3986)
- `if` / `then` / `else` conditional schemas (OAS 3.1)
- `patternProperties` / `additionalProperties`
- `dependentRequired` / `dependentSchemas`
- `prefixItems`
- `unevaluatedProperties` / `unevaluatedItems`
- Request body media types: `application/json`, `multipart/form-data`, `application/x-www-form-urlencoded`, `application/octet-stream`, `text/plain`
- Parameter serialization styles: `simple`, `form`, `label`, `matrix`, `deepObject`, `spaceDelimited`, `pipeDelimited`
- Security schemes: `apiKey`, `http` (basic/bearer)
- `Validate()` methods with pattern, min/max, enum checks
- Read/write variant types (request-only / response-only fields)

### Generated Package Layout

The generator writes files under your output directory:
- `types.go` — Schema types, enums, validation methods
- `operations.go` — Operation functions with request/response types
- `endpoints.go` — Endpoint definitions with HTTP method and path
- `auth.go` — Security scheme helpers (only when auth schemes are present)

## Known Limitations

### `unevaluatedItems: {schema}`

When `unevaluatedItems` specifies a schema object (not just `false`), validation is generated only for primitive types (`string`, `number`, `integer`, `boolean`). Complex schemas (nested objects, `$ref`, composition) are silently skipped.

### `contains`

The `contains` keyword is parsed but not used in type or validation generation. Schemas relying on `contains` for runtime validation are not supported.

### `unevaluatedItems` and `contains`

Because `contains` is not evaluated at codegen time, `unevaluatedItems` validation does not account for indices matched by `contains`.

### `unevaluatedProperties` branch matching

For `oneOf`/`anyOf` with `unevaluatedProperties`, the generator uses a heuristic (required-key presence) to determine which branch matched at runtime. This is a static approximation — full runtime evaluation of each subschema is not performed. In rare cases where branches differ only by non-required properties, the heuristic may select the wrong branch.

### `oneOf`/`anyOf` union wrappers

Multi-branch `oneOf`/`anyOf` **named component schemas** generate typed wrapper structs with `MarshalJSON`/`UnmarshalJSON` when all branches are named `$ref` schemas and a reliable discrimination strategy exists (required field set difference, unique required property, or JSON type difference). Single-branch `oneOf`/`anyOf` collapses to the branch type directly. Inline `oneOf`/`anyOf` (not a named component schema), unions with inline branches, no reliable strategy, or a `discriminator` keyword fall back to `type X = any`.

### `discriminator`

The `discriminator` keyword is parsed but routing is not implemented. `oneOf`/`anyOf` schemas with `discriminator` generate `type X = any` to prevent silently incorrect code. Discriminator-based type narrowing will be added in a future release.

### Deep local `$ref`

Deep local `$ref` paths (`#/components/schemas/<name>/<deep-path>`) are supported within `components/schemas`. The deep path traverses `properties`, `items`, `additionalProperties`, and `allOf`/`oneOf`/`anyOf` segments. RFC 6901 percent-decoding and tilde escapes (`~0`, `~1`) are handled. Other deep ref targets (e.g., `#/paths/...`) and segments not listed above are not supported.

### `const`

The `const` keyword is parsed but not reflected in generated types or validation.

### `oauth2` / `openIdConnect`

Only `apiKey` and `http` (basic/bearer) security schemes generate helper code. `oauth2` and `openIdConnect` schemes are recognized but no client-side token handling is generated.

## Development

CI runs on GitHub Actions (push/PR to `main`):

```
gofmt check → go fix check → golangci-lint → go test -race
```

Run checks locally:

```bash
golangci-lint run ./...
go test ./... -count=1 -race -timeout 300s
```

## Docs

Architecture decisions are documented in [`docs/adr`](./docs/adr).

## License

MIT (see [LICENSE](./LICENSE)).
