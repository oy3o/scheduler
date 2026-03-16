## 2025-02-27 - Security Policy: Avoid stack trace leakage in Future APIs
**Vulnerability:** Future.Get() leaked full stack traces directly to callers when a scheduled task panicked.
**Learning:** Returning detailed execution errors directly in user-facing libraries can inadvertently expose sensitive internal paths and execution context to API consumers or end-users.
**Prevention:** Always maintain an isolation boundary when returning panics. Provide a sanitized public error to the caller, while preserving the detailed stack trace (internal error) for backend logging or monitoring components like the Gatekeeper's OnError hook.
