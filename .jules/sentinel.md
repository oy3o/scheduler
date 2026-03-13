## 2024-03-13 - Initial
**Vulnerability:** None yet
**Learning:** Initializing
**Prevention:** N/A
## 2024-03-13 - Prevent Stack Trace Leakage in Public APIs
**Vulnerability:** The `closureTask.Execute` in `future.go` panics and wraps the error with the full stack trace, which is then exposed to the caller via `future.Get`.
**Learning:** Returning a detailed internal error to the user creates an observability leak, violating the security policy: "Prevent stack trace leakage in public APIs without breaking internal observability. When recovering from panics in a library, return a sanitized public error to callers, but preserve the detailed internal error (with stack trace) for downstream telemetry/alerting."
**Prevention:** Catch the panic, wrap the error securely (hide details from user), and let the internal handler logic route the detailed error with stack trace for observability.
