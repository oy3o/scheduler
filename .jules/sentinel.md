
## 2024-05-20 - Stack Trace Leakage in Future API
**Vulnerability:** The public `Future.Get()` API returned raw panic errors containing complete internal stack traces.
**Learning:** `closureTask.Execute` was capturing panic stack traces using `debug.Stack()` and storing this sensitive internal layout directly into the `Future` result `val` atomic, exposing it to external callers.
**Prevention:** Sanitized errors without stack traces must be stored for public retrieval (`Future.Get`), while detailed panics with stack traces are passed internally to `Gatekeeper` for telemetry and logging.
