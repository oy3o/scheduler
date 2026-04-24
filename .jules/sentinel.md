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

## 2024-04-24 - Alternate Vertical Whitespace Log Spoofing and Stack Trace Leakage
**Vulnerability:** The panic error sanitization in `future.go` used `strings.Index(pStr, "\n")` to strip multi-line stack traces, but this failed to account for alternate vertical whitespace characters like `\r`, `\f`, or `\v`.
**Learning:** This oversight could allow malicious user input (if included in a panic payload) to leak stack traces or cause terminal overwrite/log-spoofing attacks by bypassing the newline-only check.
**Prevention:** Always use `strings.IndexAny(pStr, "\n\r\f\v")` when sanitizing text intended for logs or public error messages to truncate at any vertical whitespace.
