
## 2024-05-20 - Pre-allocating Background Loop Buffers
**Learning:** In hot loops, particularly periodic background tasks like latency monitors (e.g. `monitor.go`), dynamically allocating slices on every iteration generates unnecessary garbage collection pressure and CPU overhead. In this codebase, allocating a 4096-element `float64` slice every 100ms created a ~32KB allocation per tick.
**Action:** When a loop needs to gather variable amounts of data per iteration, pre-allocate the required buffer outside the loop and reuse it using `buffer = buffer[:0]` inside the loop, eliminating allocations and drastically reducing CPU overhead.
