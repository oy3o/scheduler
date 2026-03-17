## 2025-02-20 - Prevent Stack Trace Leakage in Future API
**Vulnerability:** The public `Future.Get()` API leaked internal stack traces when a submitted task panicked. The stack trace was assigned directly to the user-facing error value.
**Learning:** Panics in generic task executors should be isolated. While detailed crash logs (like `debug.Stack()`) are vital for internal telemetry and system-level error hooks, exposing them across public-facing boundaries (like `Future`) leaks implementation details and poses an information disclosure risk.
**Prevention:** Always sanitize errors at the component boundary. Maintain dual error streams: a sanitized, safe error for external callers, and a detailed, rich error for internal telemetry/alerting (e.g., via the Gatekeeper's `OnError` callback).
