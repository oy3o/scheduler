## 2024-03-19 - [Fix Stack Trace Leakage in Future API]
**Vulnerability:** The `closureTask[T].Execute` method in `future.go` captured panic payloads, wrapped them in an error along with a full debug stack trace (`debug.Stack()`), and exposed this directly via the `Future.Get()` API.
**Learning:** This implementation leaked sensitive internal application architecture details (stack traces) to upstream callers/APIs. The intention was to feed the stack trace to Gatekeeper's `safeOnError` telemetry hook, but storing it directly in the `Future.err` value exposed it to the public API contract as well.
**Prevention:** Always separate public-facing error states from internal telemetry. Sanitized errors without system details should be presented to callers (e.g., `c.future.err.CompareAndSwap(nil, publicErr)`), while fully contextualized errors (with stack traces) should be routed separately to internal logging/telemetry hooks.

## 2026-03-27 - [Fix DoS via Nil Dereference in Future API]
**Vulnerability:** The `SubmitFunc`, `SubmitVoid`, and `Join` methods in `future.go` accepted `nil` values (nil functions or nil futures) without validation. Submitting a nil function would cause a runtime panic (`nil pointer dereference`) when executed by the Gatekeeper loop.
**Learning:** Even if the core subsystem (`Gatekeeper.Submit`) has nil validations, wrapper APIs or higher-level abstractions must also validate their inputs independently. Assuming downstream will handle it causes the wrapper itself to trigger a panic (e.g., executing `nil` closure).
**Prevention:** Always implement defensive programming at every public API boundary. Check for nil callbacks or objects before allocating resources or wrapping them into tasks to fail securely and gracefully.## 2025-05-15 - Exposure of Debug Stack Trace in Future API
**Vulnerability:** Information disclosure via multiline panic payloads in the Future API.
**Learning:** Even when separating internal and public errors, the raw panic payload 'p' can contain a stack trace if an error with a trace is re-panicked.
**Prevention:** Always sanitize panic payloads for public-facing errors by truncating them at the first newline or stripping known stack trace keywords.

## 2024-04-27 - [Fix DoS via Log Spoofing and Stack Trace Leakage via Alternate Whitespace]
**Vulnerability:** The panic sanitization logic in `future.go` only checked for `\n` to truncate stack traces. Attackers or edge cases could bypass this and leak stack traces or overwrite logs using alternate vertical whitespace characters like `\r`, `\v`, or `\f`.
**Learning:** Checking for standard newline `\n` is insufficient for sanitization against multi-line attacks (like log spoofing or stack trace leakage). Alternate whitespace characters can also be interpreted by terminals or logs to create new lines or overwrite content.
**Prevention:** When sanitizing strings to prevent multi-line execution or leakage, always check against all standard line-breaking characters (e.g., using `strings.IndexAny(s, "\n\r\f\v")`).
