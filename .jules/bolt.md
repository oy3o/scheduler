
## 2024-05-20 - Pre-allocating Background Loop Buffers
**Learning:** In hot loops, particularly periodic background tasks like latency monitors (e.g. `monitor.go`), dynamically allocating slices on every iteration generates unnecessary garbage collection pressure and CPU overhead. In this codebase, allocating a 4096-element `float64` slice every 100ms created a ~32KB allocation per tick.
**Action:** When a loop needs to gather variable amounts of data per iteration, pre-allocate the required buffer outside the loop and reuse it using `buffer = buffer[:0]` inside the loop, eliminating allocations and drastically reducing CPU overhead.

## 2025-02-12 - Replaced Atomic Counter with PRNG for Uncontended Sharding
**Learning:** Using a single `atomic.Uint64` combined with a modulo operation to round-robin incoming tasks into shards introduces a single point of memory contention on multi-core systems. During high throughput, thousands of tasks writing to the same atomic counter concurrently will cause cache-line ping-ponging across CPU sockets, generating overhead that dwarfs the actual operation being performed.
**Action:** Replace single-variable atomic round-robin sharding with thread-local PRNG sampling (like `rand.Uint32N(numShards)` in Go 1.22+). This achieves mathematically uniform distribution while providing perfect O(1) lock-free scaling because no cross-core atomic coordination is required.

## 2025-02-13 - Hashing Cache-Line Aligned Memory Pointers
**Learning:** When using memory pointer addresses to hash and distribute load into shards (e.g., via `modulo`), cache-line padding (like forcing structs to exactly 128 bytes) creates memory addresses where the lowest 7 bits are always zero. If the hash algorithm relies on these lowest bits (like `(addr ^ (addr >> 16)) % 8`), the uniform distribution is completely destroyed, clumping allocations into a few shards and causing severe lock contention.
**Action:** When hashing pointers of naturally large or deliberately padded structs, always right-shift the address by the alignment boundary (e.g., `addr >> 7` for 128 bytes) before applying any XOR or modulo operations to discard the "dead" zero bits and restore a uniform distribution.

## 2024-10-24 - Single-Assignment Hole Optimization in Min-Heap
**Learning:** In highly trafficked min-heaps (like the sharded `energyHeap`), `siftUp` and `siftDown` operations represent a major CPU bottleneck due to multiple array reads and writes during swaps. A standard swap requires 3 assignments and 2 array reads per level.
**Action:** Implement the single-assignment "hole" optimization: store the element being moved in a temporary variable, move parents/children into the current "hole" with a single assignment per level, and finally drop the temporary variable into the final hole. This reduces array reads and writes significantly in the hot path.

## 2025-05-14 - Loop Fusion and Delayed Resource Allocation in Join
**Learning:** Merging redundant iterations over the same slice (Loop Fusion), such as combining validation checks and non-blocking 'fast-fail' pre-checks, reduces iteration overhead and improves instruction cache locality. Furthermore, delaying `context.WithCancel` and slice allocations until after these initial O(N) checks avoids unnecessary overhead in common early-failure scenarios.
**Action:** Always combine validation loops with Phase 1 state checks in coordination functions like `Join`. Defer heavy object creation (contexts, result slices) until the fast-fail phase has passed successfully.

## 2025-05-20 - Early Return in Linear Interpolation Percentiles
**Learning:** When calculating values via linear interpolation (like percentiles), implement early returns to skip expensive adjacent-element lookups (e.g., O(N) slice scans) if the fractional weight is exactly zero.
**Action:** Calculate the fractional index first. If it is exactly zero, return the primary element immediately and skip interpolating or looking up the secondary element.
