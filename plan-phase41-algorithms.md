# Phase 41 — Algorithm Improvements

Research source: https://github.com/thepranaygupta/Data-Structures-and-Algorithms

## Selected Algorithms for CCattler

### 1. Trie for Fact Store Prefix Scan
Where: `store/memory.go` — `Scan()` iterates ALL keys with `strings.HasPrefix`, then sorts.
Improvement: O(k + m) scan where k=prefix length, m=matches, vs current O(n) where n=total facts. At 10K facts with 50-key results, ~200x faster.
Complexity: **Medium**. Replace `map[string]*Fact` with a trie. Watch broadcast stays map-based.

### 2. Min-Heap for Scheduler Node Selection
Where: `scheduler/scheduler.go` — `selectLeastLoadedNode()` linear-scans all candidates per unplaced instance.
Improvement: Build heap once O(n), extract-min O(log n) per placement. Drops from O(n*m) to O(n + m*log n).
Complexity: **Low**. Go's `container/heap` maps directly. Score = load + resource-fit penalty.

### 3. Binary Search for Sorted Fact Extraction
Where: `scheduler/scheduler.go` — all `extractXxxFromFacts()` functions linear-scan the entire facts slice with `strings.HasPrefix`. The slice is already sorted by key from `Scan()`.
Improvement: Binary search to find prefix bounds, then iterate only matching range. O(log n + m) vs O(n).
Complexity: **Low**. `sort.SearchStrings` to find first match, walk forward while prefix holds.

### 4. Topological Sort for Controller Ordering
Where: Controller runner (`controllers/` package). Controllers have implicit dependencies via Watch/output prefixes.
Improvement: Order controller reconciliation by dependency graph. Reduces wasted ticks where a downstream controller runs before its upstream has written.
Complexity: **Medium**. Build DAG from Watch() vs output prefixes, topologically sort at runner startup.

### 5. BFS for Tenant Garbage Collection
Where: `tenant/lifecycle.go` — tenant deletion triggers resource cleanup. Shared service exports create cross-tenant dependency graphs.
Improvement: BFS traversal ensures no orphaned resources when deleting tenants with shared dependencies.
Complexity: **Low**. Adjacency list from export/import facts, BFS from deletion root.

### 6. Heap-Based Weighted Load Balancing
Where: `network/userspace_proxy.go` — `forwardToBackend()` uses simple round-robin.
Improvement: Min-heap keyed by active-connection count enables least-connections balancing. Better distribution when backends have unequal capacity.
Complexity: **Medium**. Track per-backend active connections, heap-select on each request, decrement on response.

### 7. AVL Tree / B-Tree for Ordered Store
Where: `store/memory.go` — the entire fact map. Unordered `map[string]*Fact` means every Scan requires collecting all prefix matches then sorting.
Improvement: A balanced BST or B-tree keeps keys ordered. Scan becomes a range iterator. Subsumes improvement #1.
Complexity: **High**. Replaces the core storage data structure. Consider Google's btree package for Go.

### 8. Cycle Detection for Network Policy Validation
Where: `security/network_policy.go` — `Evaluate()` checks allow/deny rules but does not validate the policy graph for circular or contradictory rules.
Improvement: Detect circular allow/deny chains at policy-write time. Fail-fast on contradictions.
Complexity: **Low**. Build directed graph from rules, run DFS cycle detection on AddRule.

## Implementation Order

Priority by impact/effort ratio:
1. Min-Heap for scheduler (#2) — largest measurable perf gain, low complexity
2. Binary Search for fact extraction (#3) — quick win, low complexity
3. Trie for prefix scan (#1) — major store improvement
4. Topological sort for controllers (#4) — correctness improvement
5. Heap-based load balancing (#6) — performance
6. BFS for tenant GC (#5) — correctness
7. Cycle detection (#8) — safety
8. B-Tree ordered store (#7) — long-term, subsumes #1

## Estimated Effort

Items 1-3: ~1 week. Items 4-6: ~1 week. Items 7-8: ~1 week. Full phase: ~3 weeks.
