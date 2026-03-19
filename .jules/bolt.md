
## 2024-05-20 - Pre-allocating Background Loop Buffers
**Learning:** In hot loops, particularly periodic background tasks like latency monitors (e.g. `monitor.go`), dynamically allocating slices on every iteration generates unnecessary garbage collection pressure and CPU overhead. In this codebase, allocating a 4096-element `float64` slice every 100ms created a ~32KB allocation per tick.
**Action:** When a loop needs to gather variable amounts of data per iteration, pre-allocate the required buffer outside the loop and reuse it using `buffer = buffer[:0]` inside the loop, eliminating allocations and drastically reducing CPU overhead.

## 2025-02-12 - Replaced Atomic Counter with PRNG for Uncontended Sharding
**Learning:** Using a single `atomic.Uint64` combined with a modulo operation to round-robin incoming tasks into shards introduces a single point of memory contention on multi-core systems. During high throughput, thousands of tasks writing to the same atomic counter concurrently will cause cache-line ping-ponging across CPU sockets, generating overhead that dwarfs the actual operation being performed.
**Action:** Replace single-variable atomic round-robin sharding with thread-local PRNG sampling (like `rand.Uint32N(numShards)` in Go 1.22+). This achieves mathematically uniform distribution while providing perfect O(1) lock-free scaling because no cross-core atomic coordination is required.

## 2025-02-13 - Hashing Cache-Line Aligned Memory Pointers
**Learning:** When using memory pointer addresses to hash and distribute load into shards (e.g., via `modulo`), cache-line padding (like forcing structs to exactly 128 bytes) creates memory addresses where the lowest 7 bits are always zero. If the hash algorithm relies on these lowest bits (like `(addr ^ (addr >> 16)) % 8`), the uniform distribution is completely destroyed, clumping allocations into a few shards and causing severe lock contention.
**Action:** When hashing pointers of naturally large or deliberately padded structs, always right-shift the address by the alignment boundary (e.g., `addr >> 7` for 128 bytes) before applying any XOR or modulo operations to discard the "dead" zero bits and restore a uniform distribution.
