# ADR-020: Spec Ingestion and $ref Resolution

## Status

Accepted

## Date

2026-03-02

## Context

OpenAPI specs in the real world are rarely single self-contained files. They use `$ref` to reference other schemas — both internally (within the same document) and externally (across files or URLs). The generator must resolve all `$ref` pointers before it can produce Go types.

### $ref varieties

| Kind | Example | Complexity |
|------|---------|------------|
| Local (same file) | `$ref: '#/components/schemas/Pet'` | Simple JSON Pointer |
| Relative file | `$ref: './models/pet.yaml#/Pet'` | File I/O + JSON Pointer |
| Absolute file | `$ref: '/schemas/pet.yaml#/Pet'` | File I/O + JSON Pointer |
| Remote URL | `$ref: 'https://example.com/schemas/pet.yaml#/Pet'` | HTTP fetch + JSON Pointer |
| Relative URL | `$ref: '../common/error.yaml'` | Base URI resolution + HTTP fetch |

### Circular references

OpenAPI allows circular `$ref` chains (e.g., `Tree` → `children: Tree[]`). The generator already handles circular type generation (ADR-009: recursive defined types). However, the **resolver** must also handle circular references without infinite loops during spec loading.

### Existing tools' approaches

- **oapi-codegen**: Supports external references via `import-mapping` configuration. Users map external spec files to Go import paths. No automatic remote fetch.
- **ogen**: Bundles its own resolver that supports local and remote `$ref`. Uses the `kin-openapi` library for parsing.
- **protoc**: Requires all `.proto` files on the local filesystem. Uses `--proto_path` for search paths. No HTTP fetch.

## Decision

### Input Model: Pre-resolved or Auto-resolved

The generator supports two modes:

#### Mode 1: Pre-bundled Input (Default)

The **recommended** workflow. Users pre-bundle their multi-file spec into a single file using existing tools:

```bash
# Using redocly (industry standard)
redocly bundle petstore.yaml -o petstore-bundled.yaml
openapigo generate -i petstore-bundled.yaml -o ./api

# Using swagger-cli
swagger-cli bundle petstore.yaml -o petstore-bundled.json
openapigo generate -i petstore-bundled.json -o ./api
```

This is the approach we optimize for because:

1. **Reproducibility**: The bundled file is checked into version control, ensuring deterministic builds
2. **No network dependency**: CI/CD pipelines work offline
3. **Separation of concerns**: Spec resolution is a well-solved problem — we don't need to re-implement it
4. **Tool agnostic**: Users choose their preferred bundler

#### Mode 2: Built-in Resolution (`--resolve`)

For convenience, the generator can resolve `$ref` itself when `--resolve` is passed:

```bash
openapigo generate -i petstore.yaml -o ./api --resolve
```

**Resolution rules:**

1. **Local `$ref`** (`#/...`): Always resolved (even without `--resolve`)
2. **Relative file `$ref`**: Resolved relative to the referencing file's directory
3. **Absolute file `$ref`**: Resolved from filesystem root
4. **Remote URL `$ref`**: Fetched via HTTPS (HTTP is rejected by default; `--allow-http` to override)
5. **Base URI**: Determined by the input file's location (filesystem path or URL). Follows RFC 3986 for relative reference resolution. **OAS 3.1 `$id` and `$anchor` support**: In OAS 3.1 (JSON Schema 2020-12), `$id` changes the base URI for a subschema and all its descendants. When the resolver encounters `$id` in a schema, it registers the new base URI and uses it for resolving relative `$ref` values within that subschema's scope. `$anchor` creates a named anchor within the current base URI (e.g., `$anchor: "address"` at base `https://example.com/schemas/person` creates `https://example.com/schemas/person#address`). The resolver builds a URI → schema index during parsing, mapping each `$id` and `$anchor` to its schema node. `$ref` values are resolved against this index after base URI computation. In OAS 3.0, `$id` and `$anchor` are NOT part of the Schema Object (OAS 3.0 uses a fixed JSON Schema Draft 4 subset); if encountered, the resolver emits a warning and ignores them: `WARN: $id is not supported in OAS 3.0. Ignoring.` See JSON Schema 2020-12 §8.2.1 (`$id`), §8.2.2 (`$anchor`), §8.2.3.1 (`$ref` resolution).

**Failure policy:**

| Failure | Behavior |
|---------|----------|
| File not found | Fatal error: `ERROR: cannot resolve $ref "models/pet.yaml": file not found (resolved to /abs/path/models/pet.yaml)` |
| HTTP fetch fails | Fatal error with status code and URL |
| HTTP timeout | Default 30s timeout, configurable via `--resolve-timeout` |
| Authentication required (401/403) | Fatal error: `ERROR: $ref "https://example.com/schema.yaml" returned 403. Use --resolve-header to provide credentials.` |
| Circular $ref during loading | Detected and handled — the resolver tracks visited URIs and breaks cycles by marking nodes as "pending resolution" |
| Invalid JSON Pointer | Fatal error: `ERROR: invalid JSON pointer "/foo/bar" in $ref — key "bar" not found in object at "/foo"` |

**Fetch options:**

```bash
# Custom headers for authenticated endpoints
openapigo generate -i petstore.yaml -o ./api --resolve \
  --resolve-header "Authorization: Bearer $TOKEN"

# Custom timeout
openapigo generate -i petstore.yaml -o ./api --resolve \
  --resolve-timeout 60s

# Allow HTTP (not just HTTPS)
openapigo generate -i petstore.yaml -o ./api --resolve \
  --allow-http
```

### Import Mapping for Cross-Package References

When a spec references schemas from an external API that the user has already generated separately, the generator supports import mapping:

```bash
openapigo generate -i petstore.yaml -o ./api \
  --import-mapping "https://example.com/common/error.yaml=github.com/myorg/common-api"
```

This tells the generator: "Don't generate types for schemas in `error.yaml` — import them from the specified Go package instead."

**Configuration via file** (for complex mappings):

```yaml
# openapigo.yaml
input: petstore.yaml
output: ./api
import-mapping:
  "https://example.com/common/error.yaml": "github.com/myorg/common-api"
  "https://example.com/common/pagination.yaml": "github.com/myorg/common-api"
  "./shared/types.yaml": "github.com/myorg/shared-types"
```

```bash
openapigo generate -c openapigo.yaml
```

**How import mapping interacts with resolution**: When a `$ref` points to a file covered by import mapping, the resolver does NOT fetch or parse that file. Instead, it records the Go import path and type name to use. This means:

- The external spec file does not need to exist on the filesystem
- Network access is not required for mapped references
- Type names from the external package must match what the external generator produced (the user is responsible for consistency)

### `$ref` Interpretation Rules: Reference Object vs Schema Object

`$ref` has different semantics depending on where it appears in the spec. The resolver must distinguish these contexts:

**OAS 3.0**: `$ref` always creates a **Reference Object**. All sibling keywords (any key other than `$ref`) are ignored per the spec: "Any members other than `$ref` ... SHALL be ignored." The resolver strips sibling keywords when resolving a Reference Object `$ref`.

**OAS 3.1**: Two distinct `$ref` contexts exist:

| Context | Behavior | Sibling keywords |
|---------|----------|-----------------|
| **Reference Object** (non-Schema locations: parameters, responses, requestBodies, headers, links, callbacks, pathItems) | Replaces the object with the referenced value | Only `summary` and `description` are allowed as overrides; all other siblings are ignored |
| **Schema Object** (inside `components/schemas`, inline schemas, composition subschemas) | JSON Schema 2020-12 `$ref` — an applicator, not a replacement | All sibling keywords are valid and applied alongside the referenced schema (e.g., `$ref` + `description` + `nullable` + `readOnly` all coexist) |
| **Path Item `$ref`** | References a Path Item Object | Sibling fields that appear alongside `$ref` have **undefined behavior** per the OAS spec ("the behavior is undefined"). The generator emits a warning and ignores sibling fields: `WARN: Path Item at "/pets" has fields alongside $ref — behavior is undefined per OAS spec. Sibling fields ignored.` |

The resolver determines the context by tracking where in the document tree the `$ref` occurs:
- Inside a Schema Object → JSON Schema `$ref` semantics (preserve siblings)
- Outside a Schema Object → Reference Object semantics (strip siblings except `summary`/`description`)

This distinction is critical for correct type generation. For example, a Schema Object `$ref` with a sibling `description` should use the sibling description (not the referenced schema's), while a Schema Object `$ref` with a sibling `readOnly: true` should mark the field as read-only even if the referenced schema doesn't have `readOnly`.

### OAS Version Detection

The generator auto-detects the OpenAPI version from the spec:

| Field | Detected version |
|-------|-----------------|
| `openapi: "3.0.x"` | OAS 3.0 |
| `openapi: "3.1.x"` | OAS 3.1 |
| `swagger: "2.0"` | Swagger 2.0 — **not supported**: `ERROR: Swagger 2.0 is not supported. Convert to OpenAPI 3.x using swagger2openapi or redocly.` |
| Neither field present | Fatal error: `ERROR: cannot detect OpenAPI version. Ensure the spec has an "openapi" field.` |

**OAS 3.0 vs 3.1 differences** are handled throughout the ADRs (see ADR-004, ADR-008, ADR-009, ADR-010, ADR-011, ADR-012 for version-specific behaviors).

### Parser Requirements

The generator uses its own YAML/JSON parser (not a third-party OpenAPI-specific parser) to ensure:

1. **Keyword preservation**: All JSON Schema keywords are preserved regardless of OAS version (see ADR-008's `const` keyword discussion)
2. **Comment preservation**: YAML comments are not lost during parsing (not needed for generation, but useful for diagnostics)
3. **Large spec support**: Streaming parser for specs > 100MB (rare but exists in enterprise)
4. **Deterministic output**: Same input always produces the same Go code, regardless of YAML map iteration order. The parser preserves key ordering from the source file, and the generator processes schemas in **alphabetical order by their canonical path** (e.g., `#/components/schemas/Cat` before `#/components/schemas/Dog`) as a fallback for unordered sources (JSON).

### Spec Validation

The generator does **not** fully validate the OpenAPI spec. It validates only what it needs to generate code:

- Schema types and formats used by the generator
- `$ref` targets exist and resolve
- Required fields for code generation (e.g., `operationId` for endpoint variable naming)
- Version-specific keyword validity (e.g., warning when `patternProperties` is used in OAS 3.0 — see ADR-011)

Full spec validation is the user's responsibility (e.g., via `redocly lint`, `spectral`). This aligns with ADR-001's thin wrapper philosophy — we don't duplicate existing tools' functionality.

## Consequences

### Positive

- **Pre-bundled workflow is reproducible**: No network dependency, deterministic builds, works in air-gapped environments
- **Built-in resolution is available**: Convenient for development, prototyping, and simple single-file + local-ref specs
- **Import mapping enables multi-package generation**: Large organizations can generate separate packages per API and share common types
- **No spec validation burden**: Users use their preferred linting tools

### Negative

- **Pre-bundled workflow requires an extra step**: Users must run a bundler before the generator. This is a friction point for new users.
- **Built-in resolver is limited**: No authentication flow support (only static headers), no caching, no retry. For complex setups, users should pre-bundle.
- **Import mapping requires manual consistency**: If the external package is regenerated with different type names, the user's code breaks at compile time (not generation time).

### Risks

- **Bundler compatibility**: Different bundlers may produce subtly different output for the same input (e.g., handling of `$ref` to `$ref` chains, circular reference flattening). We test against `redocly bundle` as the reference bundler.
- **Spec size**: Very large bundled specs (100MB+) may cause memory pressure. The streaming parser mitigates this, but the in-memory schema graph will still be large. We document memory requirements for large specs.
- **JSON Pointer edge cases**: JSON Pointers with URI-encoded characters (`~0`, `~1`, `%20`) must be handled correctly per RFC 6901. We use a tested JSON Pointer library rather than hand-rolling.
