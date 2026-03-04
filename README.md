# openapigo

`openapigo` is an OpenAPI 3 code generator and runtime for Go.

It provides:
- A CLI (`openapigo`) that generates Go client packages from OpenAPI specs.
- A runtime module (`github.com/mkusaka/openapigo`) used by generated code.

## Current Status

Recent milestone work (M9-M13) is completed on `main`:
- M9: Request body media types (including multipart/form-data and binary payload handling).
- M10: CLI generation flags (`--skip-validation`, `--no-read-write-types`, `--dry-run`, `--format-mapping`, `--strict-enums`, `--validate-on-unmarshal`).
- M11: Expanded test coverage (generator and runtime).
- M12: External `$ref` resolution (file/URL) with OAS 3.1 sibling keyword support.
- M13: OAS 3.1 schema extensions (`if/then/else`, `patternProperties`, `dependentRequired`, `dependentSchemas`, `unevaluated*`, `$id`, `$anchor`, `const`).

## Requirements

- Go 1.24+
- OpenAPI spec version: 3.0.x or 3.1.x

## Installation

Install the CLI:

```bash
go install github.com/mkusaka/openapigo/cmd/openapigo@latest
```

Check the installed version:

```bash
openapigo version
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

- `-i`: Path to an OpenAPI spec file.
- `-o`: Output directory for generated code.
- `-package`: Go package name (default: output directory name).
- `--skip-validation`: Skip `Validate()` method generation.
- `--no-read-write-types`: Skip request/response variant type generation.
- `--dry-run`: Print generated file names and sizes without writing.
- `--format-mapping`: Custom format-to-type mapping (for example `uuid=github.com/google/uuid.UUID`).
- `--strict-enums`: Generate validation for non-string enums.
- `--validate-on-unmarshal`: Generate `UnmarshalJSON` methods that call `Validate()`.
- `--resolve`: Resolve external `$ref` (file and URL).
- `--allow-http`: Allow `http://` URLs for remote `$ref` (requires `--resolve`).

## Generated Package Layout

The generator writes files under your output directory:
- `types.go`
- `operations.go`
- `endpoints.go`
- `auth.go` (only when auth schemes are present)

## Docs

Architecture decisions are documented in [`docs/adr`](./docs/adr).

## Development

Run checks locally:

```bash
go test ./... -count=1 -race -timeout 300s
go vet ./...
```

## License

MIT (see [LICENSE](./LICENSE)).
