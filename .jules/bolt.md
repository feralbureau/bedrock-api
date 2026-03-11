## 2025-05-15 - [stringSimilarity Optimization]
**Learning:** Traditional Levenshtein distance implementations using a full `[][]int` matrix can cause significant GC pressure due to $O(N)$ separate allocations and $O(N \times M)$ memory usage. An iterative implementation with a single row buffer reduces space to $O(\min(N, M))$ and allocations to $O(1)$.
**Action:** Always prefer the space-optimized iterative Levenshtein algorithm for string similarity functions that are called frequently in a hot path.

## 2025-05-16 - [Proxy URL Rewriting Optimization]
**Learning:** Performing string manipulation (`strings.ToLower`, `strings.TrimPrefix`) and `fmt.Sprintf` on every item in a large result set (e.g., 300 tracks) creates significant allocation churn and CPU overhead. Using a pre-calculated map for enum-to-string conversion and direct string concatenation reduces overhead by ~75% and allocations from 5 to 1 per item.
**Action:** Use static maps for protobuf enum name resolution and prefer direct string concatenation over `fmt.Sprintf` in high-frequency loops.
