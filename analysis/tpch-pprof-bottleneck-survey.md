# TPC-H pprof Bottleneck Survey (M0054-0004)

**Date:** 2026-05-05
**goopg commit:** `735990a` (perf-analysis HEAD when the survey ran)
**Build artefact:** `tmp/goopg-bench-bin`
**Profile collection script:** `pprof-all.sh` (committed alongside this report)

## 1. Environment

| Parameter | Value |
|-----------|-------|
| Scale factor | SF=1 (full HammerDB build, identical to run-011) |
| `shared_buffers` | 2048 MB (262144 slots) |
| `GOMEMLIMIT` | 20 GiB |
| `GOOPG_MUTEX_PROFILE_RATE` | 1 (every contention event sampled) |
| `GOOPG_BLOCK_PROFILE_RATE` | 1 (every blocking event sampled) |
| AIO method | worker (3 workers) |
| pprof endpoint | 127.0.0.1:6060 |
| Build host | Linux x86_64 (WSL2), 16 logical CPUs |

The mutex/block profiling overhead added ~60 % wall-time vs run-011
(Q9 1809 s → 3444 s in this run). For M0054-0004's purpose this is
acceptable — the goal is **proportional** hotspot identification, not
absolute timing. M0054-0007 will re-run without these profiles
enabled.

## 2. Capture windows

| Tag | Phase | CPU window | Wall clock | What was running |
|-----|-------|-----------|-----------|------------------|
| `load` | HammerDB load | 30 s | 16:59:39 | ORDERS/LINEITEM steady-state load |
| `idx`  | CREATE INDEX | 30 s | 17:00:38 | LINEITEM bulk-build phase |
| `q9`   | Q9 steady state | 60 s | 17:06:06 | Multi-way join + 6-table aggregation |
| `q20`  | Q20 steady state | 60 s | 18:03:28 | Correlated EXISTS subquery |
| `end`  | Pre-shutdown | 10 s | 18:05    | Q20 still mid-execution; snapshot state |

All artefacts under `bench/tpch/pprof/`:

```
allocs_<tag>.prof   block_<tag>.prof   cpu_<tag>.prof
goroutine_<tag>.txt heap_<tag>.prof    mutex_<tag>.prof
```

The `.prof` files are gitignored (binary, large); only this report
and the script are committed.

## 3. Per-window summaries

### 3.1 `load` — ORDERS/LINEITEM bulk insert

**CPU (30 s, 16.34 s samples ≈ 0.54 cores effective):**
- `storage.(*FSM).GetPageWithFreeSpace` **62.36 % flat / 62.42 % cum** ← single dominant CPU consumer for the INSERT path
- `executor.writeHeapRow` 70.32 % cum
- `executor.(*insertOp).Next` 75.83 % cum
- `parser.Parse` 9.73 % cum (HammerDB's batched VALUES)
- `runtime.mallocgc` 8.14 % cum, `runtime.growslice` 5.32 %

**Heap (in-use 2.08 GB):** 96 % is `storage.newArena` (the
shared_buffers arena, expected — `2048 MB` GUC).

**Mutex / block:** healthy. Bgwriter `Pool.WriteDirtyPages` accounts
for 96 % of the 0.79 s mutex delay (single sampling window). Block
profile is dominated by AIO/checkpointer/bgwriter idle channel
waits (>4000 s cum, expected: idle goroutines).

**Top finding:** `FSM.GetPageWithFreeSpace` is a real CPU hotspot
during inserts. With ~6 M LINEITEM rows × ~1.5 M ORDERS rows, every
INSERT looks up a target page through the FSM. Worth investigation
for M0054-0005, but this is **load-only** — it does not affect
power-test runtime.

---

### 3.2 `idx` — CREATE INDEX phase

**CPU (30 s, 36.54 s samples ≈ 1.21 cores effective):**
- `executor.(*ddlOp).bulkBuildBTree` 80.43 % cum
- `executor.(*ddlOp).collectBTreeEntries` 60.26 % cum
- **`executor.DecodeRow` 39.19 % cum** ← per-row tuple decode to
  extract one column (the index key) for every heap row
- `executor.DecodeRowInto` 34.43 % cum
- `executor.decodeValue` 30.93 % cum

**Allocs (cumulative since process start, 189 GB):**
- `parser.Lex` 92 GB (48.58 %) ← HammerDB's giant VALUES batches
- `executor.(*valuesOp).Next` 14 GB
- `executor.(*insertOp).Next` 14 GB
- `executor.DecodeRow` 12 GB
- `runtime.encodeRecord` (WAL) 3.8 GB

**Top finding:** the index-build path is **tuple-decode-bound**.
For each LINEITEM heap row read during the bulk-build scan,
`DecodeRow` materialises ALL columns even though only the index
key column is needed. M0054-0005 candidate: a column-projection
fast path for `collectBTreeEntries` that decodes only the indexed
column.

---

### 3.3 `q9` — six-table join + aggregation

**CPU (60 s, 352.63 s samples ≈ 5.86 cores effective):**

This is the **single most important finding of M0054-0004**:

| flat % | cum % | function |
|--------|-------|----------|
| 0.06 | **82.35** | `runtime.systemstack` ← practically all GC |
| 16.93 | 76.75 | `runtime.scanobject` |
| **29.30** | 30.57 | `runtime.findObject` ← single flat hotspot |
| 9.03 | — | `runtime.(*gcWork).putObjFast` |
| 6.79 | — | `runtime.(*gcBits).bitp` |
| 4.85 | — | `runtime.memclrNoHeapPointers` |
| 0 | 18.37 | `executor.(*aggregateOp).Open` ← actual query work |
| 0 | 18.37 | `executor.(*sortOp).Open`, `executor.(*projectOp).Open` |

The actual query path consumes **18.37 % CPU**; the remaining
**~78 % is GC overhead** scanning live heap objects.

**Heap (in-use 5.14 GB):**
- `storage.newArena` 2.0 GB (shared_buffers, expected)
- **`executor.(*spillReader).ReadRow` 1.65 GB live** ← M0037
  spill-to-disk hash join read-back path retains every spilled row
  as a separate `Row` allocation
- `executor.drainRowsBounded` 0.49 GB
- `executor.(*joinOp).openLazyHashJoin` 2.80 GB cum (the hash
  table itself + materialised inputs)
- `executor.concatRows` 0.16 GB

**Allocs (cumulative since process start, 398 GB):**
- `parser.Lex` 92 GB (load-phase carry-over)
- `executor.DecodeRow` 70 GB (17.6 %)
- `executor.concatRows` 56 GB (14.1 %)
- `executor.nullRow` 33 GB (8.3 %)

**Top finding:** Q9 is **GC-bound**, not compute-bound. The 5 GB
working set keeps the GC mark-sweep cycle hot, and the per-row
allocation churn in `concatRows` / `nullRow` keeps recreating
short-lived objects that the GC must rescan. Reducing Row-buffer
allocation will directly translate to less `findObject` /
`scanobject` time.

**Mutex / block:** still healthy. `Bgwriter.WriteDirtyPages`
accounts for 95 % of the 1.72 s mutex delay; block profile is the
expected idle-channel waits.

---

### 3.4 `q20` — correlated EXISTS subquery

**CPU (60 s, 77.58 s samples ≈ 1.29 cores effective):**

| flat % | cum % | function |
|--------|-------|----------|
| 1.04 | **85.10** | `executor.(*filterOp).Next` ← entire outer-side filter walk |
| 1.16 | 85.10 | `executor.evalExpr` |
| — | 85.10 | `executor.evalInExpr`, `executor.collectInValues` |
| 3.30 | **53.04** | `executor.DecodeRowInto` |
| 9.38 | — | `runtime.duffcopy` ← row-buffer copies |
| 6.87 | — | `runtime.memmove` ← byte-level copies |
| 5.10 | — | `runtime.nextFreeFast` ← small-object allocation |
| 3.74 | — | `math/big.nat.scan` ← NUMERIC parsing |
| 3.21 | — | `runtime.scanobject` (much lower than Q9) |

Q20 is **CPU-bound on per-outer-row subquery re-evaluation**. The
correlated EXISTS subquery is being driven row-by-row through
`filterOp.Next` → `evalInExpr` → `collectInValues` → inner-plan
`projectOp.Next`. M0040 was supposed to cache subquery results;
the current shape suggests that caching does not extend to the
deepest correlated layer.

**Heap (in-use 2.22 GB):** mostly shared_buffers (2.0 GB). The
runtime working set is small — Q20 is not heap-pressured.

**Allocs (cumulative since process start, 13,929 GB):**
- **`executor.concatRows` 7,980 GB (57.29 %)**
- **`executor.nullRow` 5,413 GB (38.86 %)**
- `executor.DecodeRow` 167 GB (1.21 %)

**13.4 TB allocated in concatRows + nullRow combined.** The
allocation flame originates at
`(*aggregateOp).Open → (*projectOp).Next → ...` (97.6 %
cumulative). This is the **same hot path** that the
`/home/ryo/work/goopg/goopg/docs/design/0054-0002-executor-tuple-copy-reduction.md`
design document targets.

**Mutex:** 11.86 s sampled delay, 99 % from runtime internals
(`mheap.allocSpan`, `gcBgMarkWorker`, `getempty`). No goopg-level
contention.

---

### 3.5 `end` — pre-shutdown snapshot

10 s CPU window taken right before `bench/tpch/stop_goopg.sh`.
Q20 was still running. Profile shape matches `q20` window (same
phase). Used to verify the runtime state immediately before stop —
no goroutine leaks visible in `goroutine_end.txt`.

## 4. Aggregate top-3 actionable items (M0054-0005 input)

Three concrete items, ranked by leverage. Each is sized for a
single M0054-0005 sub-task and has a measurable acceptance
criterion. The associated design document is
`docs/design/0054-0002-executor-tuple-copy-reduction.md`.

### Item #1 — Reduce per-row Row-slice allocation (HIGHEST leverage)

**Evidence:** Q20 alloc profile shows `concatRows` 7,980 GB +
`nullRow` 5,413 GB cumulative (96 % of all allocs). Q9 keeps
0.16 GB of these alive at any moment but their churn keeps GC
hot.

**Target functions:**
- `executor.concatRows` (`internal/executor/row.go` — exact line
  per `go tool pprof -list concatRows cpu_q20.prof`)
- `executor.nullRow` (same file)
- `executor.(*projectOp).Next` and `(*joinOp).copyOut`

**Approach (per design doc 0054-0002):** introduce a per-operator
reusable output buffer, signalled via a new `BorrowSemantics`
capability bit on `Operator`. Pipeline-internal callers see a
re-used `Row` slice; retention-needing operators (Sort,
HashJoin-build, Materialize) explicitly copy.

**Acceptance:**
- TPC-H Q9 re-run shows `runtime.scanobject` cum **≤ 30 %**
  (down from 76.75 %).
- TPC-H Q20 re-run shows `concatRows + nullRow` cumulative **≤ 1
  TB** (down from 13.4 TB).
- `go test ./...` PASS.

### Item #2 — Spill-reader buffer reuse

**Evidence:** Q9 heap profile shows
`executor.(*spillReader).ReadRow` holding **1.65 GB live**.
Combined with `drainRowsBounded` 0.49 GB, this is the largest
goopg-level live allocation under any TPC-H query.

**Target functions:**
- `executor.(*spillReader).ReadRow` (`internal/executor/spill.go`)
- `executor.drainRowsBounded` and `drainRows` (same file)

**Approach:** read spilled rows into a reusable byte buffer +
decode into a re-used `Row` slot. Today every spilled row is
individually allocated.

**Acceptance:** Q9 re-run shows `spillReader.ReadRow` flat heap
**≤ 200 MB** (down from 1.65 GB).

### Item #3 — Index-build column projection

**Evidence:** `idx` window CPU profile shows `DecodeRow` /
`DecodeRowInto` / `decodeValue` consuming 39 / 34 / 31 % cum.
Bulk-build only needs ONE column (the index key) but currently
materialises the entire row.

**Target functions:**
- `executor.(*ddlOp).collectBTreeEntries`
  (`internal/executor/operators_ddl.go`)
- `executor.DecodeRow` (`internal/executor/decode.go`)

**Approach:** introduce a column-set hint to `DecodeRow` (or a new
`DecodeColumn` for single-column probes) so the bulk-build path
decodes only the indexed column.

**Acceptance:** future `idx` window profile shows `DecodeRow`
cum **≤ 15 %** (down from 39 %).

## 5. Anti-pattern findings (no action expected)

These showed up in the profiles but are NOT actionable in M0054
scope:

- **`storage.newArena` 2 GB live** — the shared_buffers arena.
  This is a single buffer-pool allocation that lives for the
  process lifetime. Reducing it would reduce buffer pool capacity
  (a different trade-off entirely).
- **`parser.Lex` 92 GB cumulative** — HammerDB's batched
  `INSERT VALUES (...), (...)` statements allocate huge token
  streams. Optimising the lexer is its own project; M0054 mirrors
  HammerDB's workload, not its SQL shape.
- **Block profile dominated by AIO/checkpointer idle waits** —
  these are background goroutines parked on channels by design.
  Showing up in the block profile is expected behaviour, not a
  bug.

## 6. Delegation to M0054-0005

Per the M0054 anti-deferral clause, M0054-0005's task entry in
`.ralph/fix_plan.md` is amended in the same loop with the three
items above as named sub-tasks (M0054-0005a / 0005b / 0005c) and
the acceptance criteria copied verbatim. The `0054-0002-executor-
tuple-copy-reduction.md` design doc supplies the implementation
detail.

## 7. References

- `pprof-all.sh` — capture script (committed)
- `cmd/goopg/main.go:141-185` — pprof endpoint + mutex/block env
  hooks
- `docs/design/0054-0002-executor-tuple-copy-reduction.md` —
  detailed Stage M0054-0005 design
- `analysis/tpch-hammerdb-run-011.md` — baseline run report
- `analysis/tpch-explain-baseline.md` — M0054-0002 EXPLAIN audit
