# 08-12 — Reduce per-query allocation volume (arena/pool the query state)

status: design · date: 2026-07-14 · base: `a640d2b0` · gates: G-race, G-tpch,
G-perf → [README](README.md)

## 1. Problem and numbers

goopg allocates **~14–15 KB per point-SELECT** (06-03: 136.5 GB over the ~106 s
allocs window). That volume feeds the GC tax — `mallocgc` 14.7 % cum,
`sweepone`+`gcBgMarkWorker` ~5.6 % of `-S` CPU (06-03; confirmed at scale 500,
07-02). This is the proportional half of the "Go language tax" (06-03 §
attribution): the tax shrinks as the allocation volume shrinks, so this is the
highest-leverage way to move the read-path Go tax from ~1.3–1.5× toward
~1.15–1.25×.

## 2. Current-code map (verified at `a640d2b0`)

- Per-query allocation sources identified in 06-03: `NewContext` (~2 %),
  snapshot capture (~2.4 %, doc 05), parse/plan node allocations, and the
  operator-tree build (doc 08). The `mctx` (memory-context) machinery already
  exists in goopg but is not used to pool the full per-query state.
- `runtime.newobject` is 9.25 % of `-S` CPU at scale 500 (raw pprof, `runs/
  scale500b_2159d329/profiles/goopg_S.cpu.pb.gz`; the 07-02 table records the
  allocator only as `mallocgc` cum 14.7 %), the concrete allocation cost.

## 3. PostgreSQL reference

- `src/backend/utils/mmgr/mcxt.c` / `aset.c` — PG's `MemoryContext` (palloc
  arenas): per-query allocations go into a `MessageContext`/`ExecutorState`
  context that is **reset wholesale** at end of query — no per-object free, no GC.
  goopg's `mctx` is the analog; the goal is to route the per-query hot
  allocations through a **reset-able arena** so a query's garbage is dropped by
  one arena reset, not traced by the GC.

## 4. Target design

Route the per-query hot-path allocations (context, parse/plan nodes where
feasible, row/datum scratch) through a per-query arena that is **reset**, not
GC'd, at end of query:

- Pool the `Context` object (`NewContext` → `sync.Pool` or a per-connection
  reusable context reset per query).
- Arena the row/datum scratch buffers (the `WriteDataRowReuse` path, doc 11,
  already does this for the reply; extend to the execution scratch).
- Where the operator tree is reused (doc 08), its slab is already arena-like;
  make sure per-query scratch hangs off the reused slab, not fresh allocations.

### Decision log

- **D1 — reduce volume, don't fight the GC.** 06-03's attribution: the GC share
  is proportional to the 14–15 KB/query; engines like VictoriaMetrics/Badger run
  Go hot paths at <5 % GC by amortizing allocations. Arena-per-query is the
  proven Go technique; goopg's `mctx` already exists as the vehicle.
- **D2 — measure `inuse_space`, not `alloc_space`** for retained-heap
  comparisons (project pattern `pattern_pprof_env_var_enablement`); the target is
  fewer bytes allocated per query (alloc rate) AND lower retained heap.
- **D3 — couple to docs 08/05/09.** Much of the per-query volume is the operator
  build (08), the snapshot (05), and the extended-path per-Execute setup (09);
  those three reduce the volume structurally, and this doc arenas what remains.

## 5. Invariants and failure modes

- **I1 — no use-after-reset.** An arena reset at end-of-query must not free
  memory still referenced by the reply already being flushed, or by a cursor
  held open across queries. Lifetime must be query-scoped precisely (PG's
  context-reset discipline).
- **F1 — datum aliasing across arena reset.** The known trap
  (`m0073_arena_q5_heap_drop`): arena slot reuse can alias values; cross-Kind
  equivalence (String↔StringArena) must hold at every comparison site. Any new
  arena for datums inherits this hazard — the row-count gate (G-tpch) catches
  aliasing bugs.
- **F2 — pooled Context carrying stale state.** A reset Context must clear every
  per-query field (`pattern_sibling_paths_must_agree`: the init path and the
  reset path set the same fields).

## 6. Migration slices

| # | slice | content | gates |
|---|---|---|---|
| S1 | pool the Context | `NewContext` → reusable/reset per query; measure alloc-rate drop. | G-race, G-tpch |
| S2 | arena the execution scratch | route row/datum scratch through a query-scoped arena; reset at end-of-query; guard datum aliasing (F1). | G-race, G-tpch |
| S3 | perf acceptance | `-S` alloc volume halved (target); `mallocgc`+GC CPU share drops; retained heap (`inuse_space`) lower. | G-perf |

## 7. Test-impact matrix

| test | file | slice |
|---|---|---|
| datum/arena aliasing (Q5-class) | `internal/executor/` datum tests; `scripts/tpch-spotcheck.sh` | S2 |
| context reuse correctness | `internal/executor/context_test.go` | S1 |
| row counts (silent-regression tripwire) | `scripts/tpch-spotcheck.sh` (Q12/Q13) | S1, S2 |

## 8. Performance verification

`-S` at scale 100 with `GOOPG_MUTEX_PROFILE_RATE`/`inuse_space` capture: per-query
alloc volume drops from ~14–15 KB toward the doc target; `mallocgc`+sweep+mark
CPU share (~20 %) falls; TPS rises. Cross-check no TPC-H row-count change.

## 9. Open questions

- **O-AV-1** — Which per-query allocations are safe to arena vs. which escape
  (returned to the client, held by a portal)? Escape-analysis the hot path first.
- **O-AV-2** — Order vs. docs 08/09: this doc's win overlaps theirs; sequence so
  the measurement attributes correctly (land 08/09 first, then arena the
  residual).
