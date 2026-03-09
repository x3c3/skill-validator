# Guidelines Advisor Report: skill-validator

## 1. System Documentation

### Plain English Description

**skill-validator** is a CLI tool and Go library that validates Agent Skill packages against the Anthropic skill specification. It performs:

- **Structure validation**: Directory layout, YAML frontmatter, token counting (o200k_base), code fence integrity, internal link resolution, orphan file detection
- **Link validation**: Concurrent HEAD requests to external HTTP/HTTPS URLs
- **Content analysis**: Word count, code ratios, imperative density, information density, instruction specificity
- **Contamination detection**: Cross-language code mixing that could confuse LLMs
- **LLM scoring** (experimental): Quality assessment across 6 dimensions via Anthropic/OpenAI APIs with file-based caching

### Architecture

```
CLI (cmd/) → Orchestrate → Structure / Links / Content / Contamination
                         → Evaluate → Judge (LLM clients) → Cache
                         → Report (text / JSON / markdown / annotations)
```

**Key design decisions:**

- Flat package layout (no `internal/`) — all packages are library-consumable
- Sequential validation pipeline with early exit on missing SKILL.md
- Concurrent HTTP link checks and LLM scoring calls
- `types.ResultContext` builder pattern for consistent result creation

### Documentation Gaps

- No godoc on exported `CheckGroup` constants beyond the comment
- The `judge` package is marked EXPERIMENTAL but the boundary between stable/experimental isn't documented in a user-facing way (e.g., Go module stability annotation)
- `rootTextFiles()` in `orphans.go` has a misleading name (returns only extraneous text files, not all root text files)

---

## 2. Architecture Analysis

### On-chain/Off-chain

**N/A** — this is a local CLI/library tool, not a blockchain project.

### Upgradeability

**N/A** — no proxy or upgrade patterns.

### External Service Integration

The project makes outbound network calls in two areas:

1. **Link checking** (`links/check.go`): HEAD requests to arbitrary URLs from skill content
2. **LLM scoring** (`judge/client.go`): POST requests to Anthropic/OpenAI APIs

Both have timeouts (10s links, 30s LLM). The link checker does not use context cancellation, which could be an issue for long-running checks.

---

## 3. Implementation Review

### Function Composition — GOOD

- Functions are small and focused
- Clear separation between parsing, validation, analysis, and reporting
- `ResultContext` builder simplifies result construction across packages

### Inheritance — N/A (Go)

- Interface usage is minimal and appropriate (`LLMClient`, `types.Scored`)
- No deep embedding hierarchies

### Events/Logging

- No structured logging — errors surface through `types.Result` or returned `error`
- Exit codes encode severity (0=pass, 1=errors, 2=warnings, 3=CLI error) — well-designed

### Test Coverage — EXCELLENT

| Package       | Coverage  |
| ------------- | --------- |
| types         | 100%      |
| contamination | 97.9%     |
| content       | 98.4%     |
| evaluate      | 97.1%     |
| orchestrate   | 98.7%     |
| structure     | 94.3%     |
| links         | 95.1%     |
| report        | 94.6%     |
| skill         | 94.8%     |
| skillcheck    | 95.0%     |
| judge         | 90.5%     |
| util          | 96.0%     |
| **cmd**       | **27.7%** |

All tests pass with `-race`.

---

## 4. Common Pitfalls & Security Findings

### CRITICAL

_None identified._

### HIGH

**H1. Symlink traversal in `DetectSkills`** — `skillcheck/validator.go:41`

- `os.Stat()` follows symlinks, meaning a crafted skill directory with symlinks could cause the validator to read files outside the intended directory tree.
- **Impact**: Information disclosure if the tool is used in CI pipelines or untrusted environments. A malicious skill package could symlink to sensitive files.
- **Recommendation**: Use `os.Lstat()` first and reject symlinks pointing outside the skill directory, or document this as an accepted risk for trusted-input scenarios.

**H2. Unbounded response body reads** — `judge/client.go:141,260`

- `io.ReadAll(resp.Body)` reads the entire response from LLM APIs without size limits.
- **Impact**: A malicious or misconfigured API endpoint (especially with custom `--base-url`) could return an extremely large response, causing OOM.
- **Recommendation**: Use `io.LimitReader(resp.Body, maxSize)` with a reasonable limit (e.g., 1 MB).

**H3. Unbounded file reads** — `skill/skill.go:84`, `skillcheck/validator.go:60,85`

- `os.ReadFile()` reads entire files without size limits.
- **Impact**: A skill directory containing very large files could cause OOM.
- **Recommendation**: Check file size before reading and enforce a reasonable limit (e.g., 10 MB). Token limits apply downstream but don't prevent the initial memory allocation.

### MEDIUM

**M1. No context cancellation in link checker** — `links/check.go:82`

- `checkHTTPLink` creates requests with `http.NewRequest` (no context), ignoring the `ctx` parameter passed to `CheckLinks`.
- **Impact**: If the caller cancels the context, in-flight HTTP requests continue until their 10s timeout expires.
- **Recommendation**: Use `http.NewRequestWithContext(ctx, ...)` in `checkHTTPLink`.

**M2. API error responses may leak sensitive information** — `judge/client.go:147,265`

- Error messages include the full response body from the API (`string(respBody)`).
- **Impact**: If the API returns unexpected content (e.g., HTML error pages with internal details), this surfaces to the user.
- **Recommendation**: Truncate error response bodies to a reasonable length (e.g., 500 chars).

**M3. Cache directory permissions** — `judge/cache.go:65`

- Cache directory created with `0o755` and files with `0o644`. API responses cached could contain sensitive scoring data.
- **Impact**: Low — scores aren't particularly sensitive, but the default permissions are world-readable.
- **Recommendation**: Consider `0o700`/`0o600` for cache directory and files, especially since they may contain content hashes that reveal file contents.

**M4. Link checker SSRF potential** — `links/check.go:82`

- URLs extracted from skill content are fetched without validation. In CI environments, this could allow SSRF against internal networks.
- **Impact**: A malicious skill could include links to `http://169.254.169.254/` (cloud metadata) or internal services.
- **Recommendation**: Document this risk for CI users. Consider an opt-in allowlist or denying RFC 1918 / link-local addresses.

### LOW

**L1. `useMaxCompletionTokens` heuristic** — `judge/client.go:189-200`

- The model detection (`strings.HasPrefix(m, "o")`) is fragile — any model starting with "o" gets treated as a reasoning model.
- **Recommendation**: Maintain an explicit list or make this configurable.

**L2. Race condition window in httpResults** — `links/check.go:68`

- The mutex is used correctly, but writing to `httpResults[idx]` is already goroutine-safe since each goroutine writes to a unique index. The mutex is unnecessary overhead.
- **Recommendation**: Remove the mutex since indexed writes to a pre-allocated slice are safe when each goroutine writes to a unique index.

**L3. SHA-256 truncation to 16 hex chars** — `judge/cache.go:33,39`

- 16 hex characters = 64 bits of hash space. Collision probability is low for expected use but not negligible for adversarial inputs.
- **Impact**: Cache poisoning via hash collision is theoretically possible but impractical.
- **Recommendation**: Acceptable for the use case. Document the truncation if cache integrity is a concern.

---

## 5. Dependencies Review

### Quality Assessment — EXCELLENT

| Dependency              | Version | Assessment                                                                    |
| ----------------------- | ------- | ----------------------------------------------------------------------------- |
| `spf13/cobra`           | v1.10.2 | Industry standard CLI framework. Well-maintained.                             |
| `tiktoken-go/tokenizer` | v0.7.0  | Pure Go port of OpenAI's tiktoken. Appropriate for the use case.              |
| `gopkg.in/yaml.v3`      | v3.0.1  | Standard YAML parser. Note: yaml.v3 has had CVEs historically — keep updated. |

**Strengths:**

- Only 3 direct dependencies — minimal attack surface
- No CGO — pure Go builds
- All dependencies are well-known, actively maintained libraries

**No copied code detected.** All external functionality comes through proper module imports.

---

## 6. Testing Evaluation

### Strengths

- **94%+ coverage** across all core packages
- **Race detection** enabled in CI (`-race` flag)
- **Fixture-based testing** with `testdata/` directories covering valid, invalid, multi-skill, broken-frontmatter, warnings-only, rich, and flat-skill scenarios
- **Shared test helpers** (`structure/helpers_test.go`) reduce duplication
- **Example tests** in `orchestrate`, `evaluate`, and `judge` serve as documentation
- **Integration tests** for exit codes (`cmd/exitcode_integration_test.go`)

### Gaps

- **`cmd/` coverage is 27.7%** — most CLI flag combinations and edge cases lack tests
- **No fuzz testing** — frontmatter parsing and content analysis would benefit from `go test -fuzz`
- **No property-based testing** — token counting and contamination scoring have mathematical properties that could be verified
- **Link checking tests** likely use mocked HTTP (good) but the SSRF risk path is untested

### Recommendations

1. Add `testing.F` fuzz targets for `skill.splitFrontmatter()` and `skill.Load()`
2. Add fuzz targets for `links.ExtractLinks()` to catch regex edge cases
3. Increase `cmd/` coverage with table-driven tests for flag combinations
4. Add a negative test for symlink traversal behavior

---

## 7. Prioritized Recommendations

### HIGH (address before production/CI use in untrusted environments)

1. **Add `io.LimitReader` to LLM response reads** — prevents OOM from malicious endpoints
2. **Pass context to `checkHTTPLink`** — enables proper cancellation
3. **Document symlink-following behavior** — users should know about the trust boundary

### MEDIUM (address for production quality)

4. **Add file size checks before `os.ReadFile`** — defense in depth against large files
5. **Truncate API error response bodies** — avoid leaking upstream details
6. **Tighten cache file permissions** — `0o700`/`0o600`
7. **Document SSRF risk for CI link validation** — or add RFC 1918 blocking

### LOW (nice to have)

8. **Add fuzz testing** for frontmatter parsing and link extraction
9. **Increase `cmd/` test coverage** to at least 60%
10. **Remove unnecessary mutex** in link checker (indexed slice writes)
11. **Make `useMaxCompletionTokens` more robust** — explicit model list

---

## Overall Assessment

**This is a well-engineered Go project.** The codebase demonstrates strong fundamentals:

- Clean package boundaries with single responsibilities
- Excellent test coverage (94%+ across core packages)
- Minimal, high-quality dependencies
- Proper concurrent programming with goroutines and sync primitives
- Good error handling patterns

The main areas for improvement are **defensive input validation** (file sizes, response sizes, symlinks) and **CI/untrusted-environment hardening** (SSRF, context propagation). These are relevant if the tool is used to validate skill packages from untrusted sources.
