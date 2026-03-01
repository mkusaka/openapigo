# ADR-023: Testing and Verification Strategy

## Status

Accepted

## Date

2026-03-02

## Context

ADR-019 states: "Test files — generated code is deterministic, testing the generator is sufficient." While true, this leaves the **how** unspecified. The generator has complex behavior across many dimensions:

- 19 ADRs of schema mapping rules with version-specific behavior (OAS 3.0 vs 3.1)
- Go version-conditional code generation (1.24 vs 1.26+)
- Numerous edge cases: circular references, name collisions, reserved words, multi-type schemas
- Serialization logic: parameter styles, media types, multipart encoding
- Runtime behavior: HTTP client, middleware, error handling, streaming

Without a testing strategy, regression risk is high and contributor confidence is low.

### Existing tools' testing approaches

- **protoc-gen-go**: Golden file tests (`.proto` → expected `.go` output). Conformance test suite across implementations.
- **sqlc**: Golden tests + `sqlc diff` (detect generation drift) + `sqlc vet` (lint SQL) + `sqlc verify` (CI integration to check if regeneration is needed).
- **oapi-codegen**: Golden tests against sample specs. Integration tests with httptest servers.
- **ogen**: Extensive golden tests + generated code compilation checks + integration tests against real API specs (GitHub, Kubernetes).

## Decision

### Test Categories

The testing strategy has four layers, each targeting different failure modes:

```
┌─────────────────────────────────────────┐
│  Layer 4: Real-World Spec Tests         │  ← Confidence
│  (GitHub, Kubernetes, Stripe specs)     │
├─────────────────────────────────────────┤
│  Layer 3: Integration Tests             │  ← End-to-end correctness
│  (generate → compile → HTTP roundtrip)  │
├─────────────────────────────────────────┤
│  Layer 2: Golden File Tests             │  ← Regression detection
│  (spec → expected Go output)            │
├─────────────────────────────────────────┤
│  Layer 1: Unit Tests                    │  ← Logic correctness
│  (individual functions/components)      │
└─────────────────────────────────────────┘
```

### Layer 1: Unit Tests

Standard Go unit tests for individual components:

**Generator components:**
- Schema type resolution (type + format → Go type mapping per ADR-009)
- Name sanitization (PascalCase conversion, collision detection, reserved word handling)
- `$ref` resolution (local, file, URL — per ADR-020)
- Composition analysis (allOf flattening, oneOf/anyOf variant detection — per ADR-002)
- Circular reference detection (cycle detection algorithm — per ADR-009)
- Parameter serialization code generation (style × explode matrix — per ADR-015)

**Runtime components:**
- `Nullable[T]` marshaling/unmarshaling (per ADR-004)
- `Do()` request building (path parameter substitution, query encoding, header injection)
- `Do()` response parsing (success codes, error handler matching, status range matching)
- Middleware chain execution (ordering, error propagation)
- `DoStream()` iterator behavior (per ADR-017)

**Coverage target**: 90%+ line coverage for runtime library, 80%+ for generator logic. Coverage is tracked in CI but not enforced as a gate — a test that exercises important edge cases is worth more than one that inflates coverage on trivial code.

### Layer 2: Golden File Tests

Golden file tests are the **primary regression detection mechanism**. Each test case is a directory containing:

```
testdata/
├── cases/
│   ├── basic-crud/
│   │   ├── spec.yaml          ← Input OpenAPI spec
│   │   ├── config.yaml        ← Generator flags (optional)
│   │   └── expected/          ← Expected generated Go files
│   │       ├── types.go
│   │       ├── operations.go
│   │       ├── endpoints.go
│   │       └── validators.go
│   ├── nullable-optional/
│   │   ├── spec.yaml
│   │   └── expected/
│   │       └── types.go
│   ├── composition-allof/
│   │   └── ...
│   ├── circular-ref/
│   │   └── ...
│   └── oas30-compat/          ← OAS 3.0-specific behavior
│       └── ...
```

**Test execution:**
1. Run the generator on `spec.yaml` with flags from `config.yaml`
2. Compare output against `expected/` directory (byte-for-byte after gofmt normalization)
3. If they differ, the test fails with a diff

**Updating goldens:**
```bash
# Update all golden files after intentional changes
go test ./internal/generator/ -update-golden

# Update specific test case
go test ./internal/generator/ -update-golden -run TestGolden/basic-crud
```

**Required golden test coverage** — at minimum, one golden test per ADR's core decision:

| ADR | Golden test case |
|-----|-----------------|
| 002 | allOf, oneOf, anyOf, allOf+discriminator |
| 003 | discriminator with/without mapping |
| 004 | nullable, optional, nullable+optional |
| 006 | additionalProperties: true, false, schema, absent |
| 007 | readOnly, writeOnly, read+write split |
| 008 | open enum, closed enum, const, default |
| 009 | all type×format combinations, multi-type, prefixItems |
| 010 | if/then/else, dependentRequired, dependentSchemas |
| 011 | patternProperties (single, multi, mixed with properties) |
| 012 | unevaluatedProperties, unevaluatedItems |
| 013 | Validate() with all constraint types |
| 014 | Endpoint + Do() + request struct tags |
| 015 | Parameter styles (form, simple, label, matrix, etc.) |
| 016 | JSON body, multipart, form-urlencoded, octet-stream |
| 017 | Success/error response variants, streaming |
| 018 | Bearer, API key, Basic, OAuth2, mutual TLS |
| 019 | Split-by-tag, package naming |

### Layer 3: Integration Tests

Integration tests verify end-to-end correctness: **generate → compile → roundtrip**.

```go
func TestIntegration_BasicCRUD(t *testing.T) {
    // 1. Generate code from a test spec into a temp directory
    dir := t.TempDir()
    genDir := filepath.Join(dir, "api")
    os.MkdirAll(genDir, 0o755)
    err := generator.Generate(generator.Config{
        Input:  "testdata/integration/petstore.yaml",
        Output: genDir,
    })
    require.NoError(t, err)

    // 2. Initialize a Go module in the temp directory
    //    (ADR-019: go.mod is NOT generated — the test harness creates it)
    repoRoot := mustRepoRoot(t) // locates the openapigo repository root
    writeFile(t, filepath.Join(dir, "go.mod"), fmt.Sprintf(
        "module test\n\ngo 1.24\n\n"+
            "require github.com/mkusaka/openapigo v0.0.0\n\n"+
            "replace github.com/mkusaka/openapigo => %s\n",
        repoRoot,
    ))
    // Run `go mod tidy` to resolve the dependency
    runCmd(t, dir, "go", "mod", "tidy")

    // 3. Compile the generated code
    runCmd(t, dir, "go", "build", "./...")

    // 4. Start a test HTTP server that implements the spec
    srv := httptest.NewServer(petstoreHandler())
    defer srv.Close()

    // 5. Use the generated client to make requests and verify responses
    // (via a thin main.go test binary in dir that imports the generated package)
}
```

**Module initialization in integration tests**: Since ADR-019 specifies that `go.mod` is NOT generated, the test harness must create a temporary `go.mod`. The `require` version (`v0.0.0`) is a placeholder — the `replace` directive overrides it to point to the local repository root, ensuring that integration tests always exercise the **local (possibly uncommitted) runtime code** — not a previously published version. Without `replace`, `go mod tidy` would fetch the published module, making local changes invisible to the test. This mirrors what a real user would have in their project, except for the `replace` directive which is test-only.

**Compilation check**: At minimum, every golden test case MUST also pass `go build`. This catches cases where golden output looks correct but doesn't compile (e.g., missing imports, type errors).

**HTTP roundtrip tests**: A subset of integration tests start an `httptest.Server` that implements the spec and verify:
- Request serialization (path params, query params, headers, body)
- Response deserialization (success and error responses)
- Middleware execution (auth headers, custom headers)
- Error type matching (`errors.As` for typed errors)
- Streaming responses (`DoStream` iterator)

### Layer 4: Real-World Spec Tests

Generate code from large, real-world OpenAPI specs and verify compilation:

| Spec | Source | Purpose |
|------|--------|---------|
| Petstore (OAS 3.0) | OpenAPI Initiative | Baseline compatibility |
| Petstore (OAS 3.1) | OpenAPI Initiative | 3.1 feature coverage |
| GitHub REST API | GitHub | Large spec, many operations, complex auth |
| Kubernetes | CNCF | Very large spec, deep composition |
| Stripe | Stripe | Complex oneOf/anyOf, extensive error types |
| Twilio | Twilio | Multiple API versions, pagination |

**These tests verify compilation only**, not runtime behavior. They catch:
- Name collision bugs at scale
- Performance issues with large specs
- Edge cases that synthetic test specs don't cover

**Spec versioning**: Real-world specs are pinned to specific versions (via commit hash or release tag) in `testdata/realworld/` and checked into the repository. They are NOT fetched at test time — tests must work offline.

### CI Pipeline

```yaml
# .github/workflows/ci.yml (conceptual)
jobs:
  test:
    strategy:
      matrix:
        go-version: ["1.24", "1.25", "1.26"]
    steps:
      - uses: actions/checkout@v4
        with:
          lfs: true  # Required for real-world specs stored in Git LFS
      - uses: actions/setup-go@v5
        with:
          go-version: ${{ matrix.go-version }}

      # Layer 1: Unit tests
      - run: go test ./... -race -count=1

      # Layer 2: Golden tests (included in go test)
      # Layer 3: Integration tests
      - run: go test ./internal/integration/ -tags=integration -race

      # Layer 4: Real-world specs (compilation only)
      - run: go test ./internal/realworld/ -tags=realworld -timeout=10m

      # Verify generated code is up-to-date
      - run: go generate ./... && git diff --exit-code
```

**Go version matrix and golden tests**: Tests run on Go 1.24, 1.25, and 1.26. Per ADR-005, the generator reads the target project's `go.mod` `go` directive to decide which code variant to emit (e.g., `new(expr)` for 1.26+, `ptr(v)` helper for 1.24-1.25). Golden tests use **separate expected directories per Go version** — not build tags:

```
testdata/cases/basic-crud/
├── spec.yaml
├── expected-go1.24/    ← Expected output when go.mod says "go 1.24"
│   ├── types.go
│   └── ...
└── expected-go1.26/    ← Expected output when go.mod says "go 1.26"
    ├── types.go        ← Uses new(expr)
    └── ...
```

The golden test runner passes the target Go version as a generator config parameter (simulating different `go.mod` values), then compares against the corresponding expected directory. When 1.24 and 1.26 output is identical for a test case, a single `expected/` directory suffices — the runner falls back to `expected/` when a version-specific directory does not exist. This avoids duplicating golden files for cases where the output is version-independent.

**Git LFS and CI**: Real-world specs > 1MB are stored in Git LFS. The CI checkout step includes `lfs: true` to ensure LFS pointers are resolved. Without this, Layer 4 tests would receive pointer files instead of actual specs and fail silently or with parse errors.

### CLI Verification Commands

Inspired by sqlc's `diff`/`vet`/`verify`, the generator provides:

#### `openapigo diff`

Compares what the generator would produce against the existing generated files:

```bash
$ openapigo diff -i petstore.yaml -o ./api
api/types.go: differs (2 lines changed)
api/endpoints.go: up-to-date
api/operations.go: up-to-date
```

Exit code 0 if no differences, 1 if differences exist. Useful in CI to verify that generated code is committed and up-to-date.

#### `openapigo verify`

Verifies that the generated code in the output directory was produced by the current generator version and matches the current spec:

```bash
$ openapigo verify -i petstore.yaml -o ./api
✓ Generator version matches (v0.5.0)
✓ Spec hash matches (sha256:abc...)
✓ All generated files present
✓ No stale files detected
```

Exit code 0 if everything matches, 1 otherwise. This is lighter than `diff` — it checks metadata (version + spec hash stored in the header comment) without re-running the full generator.

## Consequences

### Positive

- **Golden tests catch regressions**: Any change to generated output is immediately visible in diffs
- **Integration tests verify end-to-end**: Generated code actually compiles and works
- **Real-world specs catch scale issues**: Large specs exercise code paths that synthetic tests miss
- **CI verification prevents drift**: `openapigo diff` in CI ensures generated code is always up-to-date
- **Go version matrix prevents version-specific bugs**: Tests run on all supported Go versions

### Negative

- **Golden file maintenance burden**: Every intentional change to generation output requires updating golden files. The `-update-golden` flag mitigates this.
- **Real-world spec size**: Checking in large specs (GitHub API is ~30MB) increases repository size. We use Git LFS for specs > 1MB.
- **Integration test complexity**: Compilation-checking tests require subprocess execution and are slower than pure unit tests.

### Risks

- **Golden test brittleness**: Cosmetic changes (import order, whitespace) can cause golden test failures. Mitigation: normalize output with `gofmt` before comparison, and sort imports deterministically.
- **Real-world spec evolution**: Pinned specs may become outdated. We periodically update pinned versions (quarterly) and track spec compatibility in release notes.
- **CI time**: The full test suite (4 layers × 3 Go versions) may take 10+ minutes. Layer 4 (real-world specs) runs only on `main` branch and PRs touching generator code, not on every commit.
