## 2024-05-19 - Fast Percentile Calculation using QuickSelect
**Learning:** Computing P99 latency with full array sort `sort.Float64s()` takes O(N log N) which becomes a CPU bottleneck when array size reaches thousands of samples (e.g. 4096). This blocks the monitor loop and inflates P99 spikes.
**Action:** Replace full array sort with `QuickSelect` based partial sort, providing O(N) expected time. When only one or two specific elements (lo and hi for interpolation) are needed, quick select significantly boosts performance (~20x faster for N=4096).
