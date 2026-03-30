## 2023-10-27 - [Panic Isolation vs Observability]
**Vulnerability:** Attempted to prevent stack trace leaks by either removing debug.Stack() or re-throwing panics in future.go.
**Learning:** The existing code intentionally uses two separate errors: a publicErr without stack trace, and an internalErr with debug.Stack() for OnError telemetry. Re-throwing the panic breaks the application's Panic Isolation Boundary leading to DoS crashes, while removing debug.Stack() degrades critical internal observability without fixing an actual leak.
**Prevention:** Do not remove debug.Stack() from internal errors or re-throw caught panics when the application relies on isolating failures and returning internal telemetry as errors. Focus on preventing panics at API boundaries (like nil checks).
