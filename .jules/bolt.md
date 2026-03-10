## 2025-05-15 - [stringSimilarity Optimization]
**Learning:** Traditional Levenshtein distance implementations using a full `[][]int` matrix can cause significant GC pressure due to $O(N)$ separate allocations and $O(N \times M)$ memory usage. An iterative implementation with a single row buffer reduces space to $O(\min(N, M))$ and allocations to $O(1)$.
**Action:** Always prefer the space-optimized iterative Levenshtein algorithm for string similarity functions that are called frequently in a hot path.
