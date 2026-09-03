# 12 — Executor gap analysis (take3 synthesis, executor bundle)

What separates goopg's executor from PostgreSQL 18.3's at HEAD `adf2d1e13`
(Sep 2026), ranked by measured TPC-H / TPC-DS leverage. Read
[10-executor-pg-design.md](10-executor-pg-design.md) for the oracle and
[11-executor-goopg-design.md](11-executor-goopg-design.md) for goopg's current
state first: this document cites them and does not repeat them. Response:
[13-executor-target-design.md](13-executor-target-design.md); execution list:
`TODO_EXECUTOR.md`.

Companion to [07-gap-analysis.md](07-gap-analysis.md) (plan-side gaps) and its
§6 executor-residual table. Where 07 §6 defers with pointers, this document
is the analysis it defers to. The plan-parity bundle (take3 01–09) is the
prerequisite context: executor work starts where plan work stops, at the
identical-plan residual.

## 0. Method and status-mark discipline

Every PG mechanism cites a 10 § section; every goopg mechanism cites an
11 § section. Measurements are carried from 11 §17 (the hot-spot table) and
the take5/take6/take7 benchmark records it points at — no new measurement
was taken for this document, and rows are quoted with their 11 §17 status:

- **LANDED** rows are closed work: recorded in §11 with their numbers, not
  re-argued.
- **OPEN** rows are the ranked gaps (§§3–10).
- **PARTIAL** (row 10, whole-row deform) is split: the narrow fix is landed,
  the general fix is open and sequenced in 13 §4.
- **STALE** (row 16, the Q14/Q3/Q10 "spill" attribution) is honored: the
  ratios hold, the attribution is corrected to plan choice, and §13 of this
  document records the correction rather than the claim.

Take2 ledger: the brief asked for take2 P7-03 rows under `take2-executor`.
Grep over `.ralph/deferral_ledger.md` on 2026-09-03 finds exactly **one**
matching row, `take2-executor-residual` (Q9 width at equal cardinality,
1098–3164 B vs 23–81 B, 8 batches vs 1 — 07 §3.2 carries the same numbers).
No `take2-P7-03*` rows exist under that key; the "11 rows" referenced by
07 §6 and 08 §1 P6 live in take2's TODO, not in this repo's ledger. This
document therefore treats 07 §6's table plus 11 §17 as the de-facto executor
deferral list, and 13 §2 (EX0) opens with ledgering the executor backlog
properly.

---

## 1. Baselines: the numbers every gap is ranked against

### 1.1 The motivating residual: identical plan, 23.6× time

TPC-H Q6 runs the node-for-node PG-identical plan (Seq Scan + filter + agg)
and still takes **23.40 s serial against PG's 0.9905 s** — 23.6× on the same
shape (11 §17 row 1; 07 §4.1, §6). Parallelism scales alike on both engines,
so the factor is per-row executor tax, not a parallelism artifact. This one
number is the whole justification for the executor bundle: plan parity is
necessary (q18+q09+q05 = 64% of the TPC-H total sits largely executor-side,
07 §4.1) and will not by itself close the time gap.

### 1.2 The Q6 optimisation chain (serial unless noted)

| step | serial | what changed | 11 §17 |
|---|---|---|---|
| oracle baseline | 23.40 s | — | row 1 |
| numeric text-decode fix | **11.51 s** (2.0×) | per-value text→numeric fast path | row 2, LANDED |
| take5 (prefilter + 6-of-16) | **6.63 s** | copy-after-filter, deform 6 of 16 cols on ~98% rows | row 3, LANDED (narrow) |
| take6 (triple-copy + boxing) | **4.49 s** (1.46×, from the 6.55 s take6 baseline, row 4) | one-copy `PageGetHeapTupleInto`, `evalPrefilter` slab-first | row 4, LANDED |
| take7 (compiled prefilter) | **3.792 s** (1.20×) | `evalFastExpr` kind-switch on the scan prefilter | row 6, LANDED (prefilter only) |
| parallel take7 | 1.009 → **0.838 s** | same prefilter win on workers | row 6 |
| PG parallel reference | **0.203 s** | — | 4.1× above goopg parallel |

Residual after everything landed: serial 3.79 s vs ~0.99 s (**3.8×**),
parallel 0.838 s vs 0.203 s (**4.1×**). The landed items each roughly halved
the gap; the open items below own the remaining factor of ~4.

### 1.3 Suite totals and measurement hygiene (carried from 07)

- TPC-H SF=1: 227.0 s vs PG 22.9 s (9.9×) is **STALE as a comparison**: the
  honest ratio at aligned `work_mem` is nearer **17.6×** (07 §2.1).
- TPC-DS SF0.5: 1173 s vs 536 s (2.2×) (07 §2.1).
- Noise band ±17% single-run; per-query gate 1.2×; suite claims on totals
  (take3 09 §6). Every executor item inherits this methodology (13 §2).
- Timing regime: TPC-H `GOGC=100 GOMEMLIMIT=12GiB` through the cgroup cap;
  TPC-DS harness `GOGC=off` default (take3 09 §6). Allocator and time are
  measured together on every item — a CPU win that doubles allocations is
  not a win (13 §1).

---

## 2. Ranked gap table

Ordered by measured leverage (bytes × rows × queries affected), not by
subsystem size. "Queries" names the known witnesses; unmeasured cells say so.

| rank | gap | § | leverage | status |
|---|---|---|---|---|
| G-EX1 | Tuple width: 48 B Datum + whole-row deform + scan-time detoast | §3 | Q6 residual 3.8×; Q9 8 batches vs 1 at equal cardinality | OPEN (reduction landed) |
| G-EX2 | Retention-boundary clones (`cloneRowOwned`/`MaterializeArena`) + row pool | §4 | 36–41% of objects post-take6; was 26.6% CPU on Q6 | OPEN |
| G-EX3 | Probe re-materialisation cascade + 2× build memory | §5 | ~18M pool round-trips, ~2×2.3 GB on Q9-class | OPEN |
| G-EX4 | Spill path: cached-handle instrumentation + encode/I/O, no run/batch discipline | §6 | Stack-walk projections (69–86% / 3.3–7.3×) superseded — elimination landed; residual TBD | OPEN (elimination landed; discipline open; rank provisional pending EX3 re-measurement) |
| G-EX5 | Expression interpreter (prefilter-only compilation) | §7 | 1.20× landed on prefilter; `filterOp` + join/agg residuals still interpret | PARTIAL |
| G-EX6 | Scan decode remainder past the numeric fast path | §8 | numeric half landed 2.0×; text/date/TOAST-pointer decode open | OPEN |
| G-EX7 | Sort: no bounded top-N, no incremental sort, no skew partitioning | §9 | q16 34% in `lessRows`; P4-04/05 planner halves blocked/waiting | OPEN |
| G-EX8 | Parallel executor: Gather fallback, worker stat loss, serial-only compiled path | §10 | every parallel query runs legacy `Build`; compiled predicate unreachable | OPEN |

Closed items (§11) are not ranked: numeric decode, copy-before-filter,
triple-copy/boxing, ToLower/atomics/CLOG — each with its landed number.

---

## 3. G-EX1 — Tuple width: 48 B Datum, whole-row deform, scan-time detoast

### PG mechanism (10 §§3, 6, 13, 15)

- `TupleTableSlot` carries `tts_values`/`tts_isnull` datum arrays with
  `tts_nvalid` tracking how many columns are currently deformed (10 §3).
- `slot_getsomeattrs` deforms only up to the highest referenced attribute;
  a scan over a 16-column TPC-H row for a 6-column query decodes 6 columns,
  never 16 (10 §3). `slot_getallattrs` is reserved for whole-row ops
  (`WHOLEROW`, hashing all keys) (10 §3).
- PG's `Datum` is 8 B pass-by-value-or-pointer; per-tuple scratch dies in
  `ecxt_per_tuple_memory` reset each row, so transient width never
  accumulates (10 §13, §15 steps 3–7).
- Hash build packs tuples contiguously into `HASH_CHUNK_SIZE` arenas via
  `dense_alloc` — one `palloc` per chunk, oversized tuples in dedicated
  chunks (10 §6).

### goopg mechanism (11 §§3, 4.1, 6, 13, 14)

- `Datum` is exactly 48 B, pinned by test (`datum.go:187`,
  `datum_arena_test.go:17-19`; M0107-0002 already shrank 64 B → 48 B).
  Every owned row is a full `[]Datum` at 48 B/column (11 §13).
- Scans deform whole projected rows up front via `decodeScanRow`
  (`operators_storage.go:1378`); take5 narrowed only the prefilter path to
  6-of-16 on ~98% of Q6 rows (11 §4.1, §17 row 10: PARTIAL, general fix =
  planner P4-01 PathTarget).
- Toasted values resolve inline for the whole row at scan time
  (`DetoastRow`, `operators_storage.go:2143-2150`); PG detoasts lazily per
  attribute (11 §14).
- Build geometry prices `EntryBytes = 48·ncols + 24 + avgVarBytes`
  (`hashsize.go:42-50`); the session-less budget is still the 512 MB default
  (11 §6).

### Measured cost

- Q6 residual: 3.79 s serial vs ~0.99 s PG after all landed items — the
  remaining 3.8× is dominated by per-column footprint on the surviving rows
  (11 §17 rows 1, 7).
- Q9 at equal cardinality (goopg 321,056 rows vs PG ~319k): widths
  **1098–3164 B vs 23–81 B (14–39× cross-ratio; level-paired
  ~39–51×: 1098/23 ≈ 47.7, 1642/32 ≈ 51.3, 2090/54 ≈ 38.7,
  3164/81 ≈ 39.0)**, 97 MB / 8 batches vs 38 MB / 1,
  63.8 s vs 6.2 s (`take2-executor-residual` ledger row; 07 §3.2).
- Take2 ruled out the two cheap explanations (pointer-chasing, GC-tracing):
  the tax is the footprint itself (11 §13).

### Why PG is faster

PG never materialises what nothing references: deform stops at the highest
touched attribute, detoast happens per attribute on first use, and hash
entries pack contiguously. goopg pays 48 B × projected columns on every
owned row plus whole-row detoast at scan time, so width multiplies through
every downstream copy, batch count, and spill decision.

### Out-of-scope notes

- The general fix (per-path projection / PathTarget) is planner work P4-01
  (07 §3.2, 08 §7); the executor half — deform-some-attrs, lazy detoast,
  narrower owned rows — is 13 §4 (EX1) and must land against the narrowed
  plan shape, not before it (13 §8: narrowing before batching math).
- Shrinking `Datum` below 48 B is explicitly NOT proposed: the 64→48 B
  reduction is landed, and further shrinkage without narrowing keeps most of
  the cost (§12 negative result).

---

## 4. G-EX2 — Retention-boundary clones and the row pool

### PG mechanism (10 §§3, 13, 15)

- Ownership transfer is explicit and rare: `ExecMaterializeSlot` /
  `ExecCopySlot` at lifetime barriers (sort input, hash insert, tuplestore
  write, gather queue); everything else borrows from the buffer pin or the
  per-tuple context (10 §3, §15 step 5).
- Per-tuple scratch is freed by `ResetExprContext` → `MemoryContextReset`
  untracked — no per-row allocator traffic for transient rows (10 §13).

### goopg mechanism (11 §§3, 13, 16)

- Every survivor crosses the ownership boundary via `cloneRowOwned`
  (`datum.go:493`) + `MaterializeArena` (`datum.go:434-478`); even
  `VirtualSlot.Materialize()` now routes through the arena step (11 §3).
- Scratch rows come from `acquireRow(width)` (`row_pool.go:42`, pooled
  widths ≤ 64) and return via `releaseRow` (11 §13).
- Take5 moved the clone to *after* the prefilter (old profile `acquireRow`
  39.2% / `MaterializeArena` 28.5%); take6 removed the triple copy (11 §16).

### Measured cost

- Pre-take5: `cloneRowOwned` 26.6% of Q6 CPU → 1.16% after the move
  (11 §17 row 3).
- Post-take6: `cloneRowOwned`/`MaterializeArena` still **36–41% of allocated
  objects** — the top allocator (11 §17 row 9, OPEN).

### Why PG is faster

PG copies at barriers and borrows everywhere else; the per-tuple reset
makes transient rows free. goopg clones every surviving row into an owned
allocation at each boundary, so the allocator — not the CPU — is the
ceiling on narrow-query throughput.

### Out-of-scope notes

- The remaining clones are correctness load-bearing (use-after-reset class,
  10 §13): elimination must prove retention safety per boundary (13 §5:
  retention-boundary audit), never delete the boundary by inspection.
- Pool sizing (widths ≤ 64, depth policy) is executor-internal; no planner
  or catalog dependency.

---

## 5. G-EX3 — Probe re-materialisation cascade and 2× build memory

### PG mechanism (10 §§5, 6)

- `HashJoin` builds once (inner `Hash` child via `MultiExecProcNode`), then
  probes bucket chains in place; outer tuples stream through without being
  copied per level (10 §5).
- Build memory is one arena set (`dense_alloc` chunks in `batchCxt`); batch
  growth repartitions stored tuples rather than duplicating them (10 §6).

### goopg mechanism (11 §§5, 6, 16)

- Joins compose inputs as `VirtualSlot` (`slot.go:146`) and re-materialise
  at each cascade seam: step 5 of the tuple journey re-materialises at every
  level (11 §16).
- The build keeps two passes plus two maps — peak build ~2× PG (11 §17
  row 14).

### Measured cost

- ~18M pool round-trips, ~2×2.3 GB of `Datum` traffic on Q9-class shapes
  (6M rows × 3 cascade levels)
  (`analysis/cost-driven-second-try-200731/02-premise-audit.md:87-155`;
  11 §17 row 11, OPEN).
- Q7's 45.4× ratio is attributed to this probe-seam class on the executor
  side (07 §4.1), shared with the P2-02b residual.

### Why PG is faster

PG's probe path borrows: the outer slot stays valid while the bucket chain
is walked. goopg's seam discipline (correct: no aliasing across `Next()`
calls) re-owns the probe row per level, so multi-join cascades multiply one
row's allocation by depth × 2.

### Out-of-scope notes

- Fixing the cascade changes join internals only; plans must not move
  (plan-shape pin, 13 §1). Cascade fixes are sequenced after EX1 narrowing
  (narrower rows shrink the cascade product first).
- Cooperative build stall (workers contending on the shared build) is an
  EX5 item (13 §7), not this gap.

---

## 6. G-EX4 — Spill path: residual encode/I/O cost, no run/batch discipline

### PG mechanism (10 §§6, 9, 11, 16)

- Hash spill routes by `ExecHashGetBucketAndBatch`; later batches spill
  symmetrically, each batch pair re-reads both sides, peak memory stays near
  `work_mem` (10 §6).
- Sort spills runs to `LogicalTapeSetCreate` tapes with polyphase merge;
  `tuplestore` pages chunks to `BufFile`s past `maxKBytes`, transparently to
  readers (10 §§9, 11).
- One `work_mem` budget per sort/hash node instance; skew keys stay resident
  via the MCV skew table (10 §§6, 16).

### goopg mechanism (11 §§6, 9, 11)

- `spillWriter`/`spillReader` (`spill.go:20,155`) stream whole encoded rows
  to per-query temp files (`tempFileRegistry`, `tempfiles.go:10-91`).
- The round5 Stack-tax elimination has **LANDED** (`1d6b1e396`, before
  11's pin): the handle is cached at construction
  (`spill.go:78-85,181`) and the per-row path uses it without any
  Stack walk (`writeFrame`, `:121-135`) — the 69–86% Stack-walk
  projections are superseded-with-evidence (11 §11, §17 row 8).
- Sort spills whole chunks (`flushChunk`, `operators.go:1119`) with no
  run-formation or tape-merge discipline; hash batching state
  (`hashBatchState`, `join_batch.go:79`) exists but both engines batch
  symmetrically — neither batches probe input row-wise (11 §17 row 16).

### Measured cost

- 69–86% of CPU on spilling Q4/Q7/Q13 shapes was attributed to the
  Stack walk, with 4.7×/3.3×/7.3× projected wins on elimination (round5
  design `01-spill-writer-stack-elimination.md:12-23`) — SUPERSEDED
  as a Stack-walk claim (elimination landed, 11 §17 row 8); the
  residual per-row cost (cached-handle instrumentation, encode,
  file I/O) is TBD.
- Costing note: these were always projections from a design doc, not A/B
  landings — 13 §6 (EX3) re-measures before claiming.

### Why PG is faster

PG's spill path does no per-row introspection and organises spilled data
into runs/batches with merge discipline, so spilling degrades into
sequential re-reads. goopg's Stack-walk introspection is eliminated
(cached handle), but it still spills whole rows with no run formation
and pays per-row encode plus cached-handle instrumentation, so the
spill path's residual cost is TBD until EX3-01 re-measures — it may
still be CPU-bound before it is I/O-bound.

### Out-of-scope notes

- **STALE attribution honored** (11 §17 row 16, §18 item 3): the Q14 24× /
  Q3 11× / Q10 4× ratios hold as numbers but were re-attributed to plan
  choice (merge over a full 6M-row index scan), not spill efficiency —
  neither engine batches those shapes. Executor spill work must not cite
  those queries as spill witnesses (§13).
- `work_mem` BootVal alignment (512 MB → 4 MB) is planner work P2-02b
  (07 §2.1); EX3 must be measured at both budgets because batch counts move
  with the budget.

---

## 7. G-EX5 — Expression eval: interpreter, prefilter-only compilation

### PG mechanism (10 §12)

- `ExecInitExpr` compiles expressions to a flat `ExprEvalStep` array;
  `ExecInterpExpr` runs a computed-goto interpreter over it, with
  `FETCHSOME`-fused deform and AND-shortcut quals; JIT substitutes the
  evaluator above cost thresholds (`JitProviderCallbacks`) (10 §12).

### goopg mechanism (11 §12, §§4.5, 5)

- Interpreter `evalExpr(e, row, ctx)` (`expr.go:352`); slot entry
  `evalExprSlot` (`expr.go:413`); compiled slab `evalFastExpr` kind-switch
  (`exprnode.go:283-456`) with `ExprAdapter` fallback to the interpreter.
- Reachability is narrow: only the scan prefilter is compiled; `filterOp`
  above re-interprets the same predicate (`operators.go:565`), and merge
  residuals evaluate per row (`mergeResidualMatch`, `join_merge_key.go:106`)
  (11 §12, §18 item 5).

### Measured cost

- take7: 4.563 → 3.792 s serial, 1.009 → 0.838 s parallel (**1.20×**) from
  the prefilter alone (11 §17 row 6, LANDED prefilter-only).
- Remainder (~0.33 pp per take7 results §7) sits in `filterOp`,
  join residuals, and agg transition evaluation — unmeasured per operator,
  which is why EX0 instruments first (13 §2).

### Why PG is faster

PG compiles every expression once (including quals above the scan and join
clauses) and fuses deform into the fetch steps; goopg compiles one site and
interprets the same predicate twice (prefilter compiled + `filterOp`
interpreted), paying interface boxing per row on the second pass.

### Out-of-scope notes

- No LLVM/JIT counterpart is proposed: the target is compiled-slab coverage
  per operator (13 §6), explicitly **no query-specific forcing** — extending
  the existing prefilter approach, not hand-optimising hot queries.
- Compiling `filterOp` on the parallel path needs a `Gather` arm in
  `buildRec`, a planner-shape change (11 §12) — sequenced as EX4-after-EX5
  coordination with a plan-shape pin, or done serial-path-first.

---

## 8. G-EX6 — Scan decode remainder

### PG mechanism (10 §§4, 15)

- `heap_getnextslot` fills a buffer slot with zero column decoding; decode
  happens lazily at first touch (§3); scans are otherwise sequential page
  streaming with prefetch (10 §§4, 15 steps 1–3).

### goopg mechanism (11 §§4.1, 14)

- `decodeScanRow` / `decodeScanRowRange` (`operators_storage.go:1378,1390`)
  with per-column type info resolved once in `Open` (`colInfo`); one-copy
  `PageGetHeapTupleInto` (take6); prefetch window `refillPrefetchWindow`
  (`:1764-1782`), disabled for parallel scans (11 §4.1).
- Codec entry `decodePhysicalPGValueLowered` (`codec.go`); AIO `ReadStream`
  exists but is **unwired** — zero callers outside its own file (11 §14,
  §18 item 2).

### Measured cost

- Numeric fast path landed: serial 23.40 → 11.51 s, 2.0× (11 §17 row 2).
- Remainder (text/date/numeric-edge decoding, TOAST-pointer handling,
  per-column branches) is unmeasured per type — EX0's per-operator harness
  (13 §2) quantifies before EX1/EX4 touch it.

### Why PG is faster

PG's scan decodes nothing until the qual touches it, and numeric/text
output conversion happens once at the receiver. goopg decodes every
projected column of every scanned row (then filters 98% away on Q6-class
shapes — now mitigated narrowly by the prefilter, generally by EX1).

### Out-of-scope notes

- Wiring AIO `ReadStream` is speculative until EX0 shows scan I/O — not
  CPU decode — as the binding constraint on some suite query. It stays a
  candidate, not a commitment (13 §7).
- The wide-text histogram catalog gap (planner P1-11) is not a scan-decode
  gap; do not conflate.

---

## 9. G-EX7 — Sort, skew, and aggregation spill behaviour

### PG mechanism (10 §§6, 8, 9, 16)

- `tuplesort`: datum fast path for single-column sorts, bounded top-N heap
  via `tuplesort_set_bound` (pushed from `ExecInitLimit`), presorted-prefix
  `IncrementalSort`, abbreviated keys, logtape merge (10 §9).
- Skew keys pinned resident by the MCV skew table (10 §6); hashed agg
  degrades into `AGG_MIXED` sorted runs past memory (10 §§8, 16).

### goopg mechanism (11 §§6, 8, 9)

- `sortOp` (`operators.go:760-1258`): memory-sized chunks, decoded-vector
  keys (`sortKeyVals`), row fallback `lessRows`, CTID tiebreak tail,
  whole-chunk spill + merge-back. No datum fast path, no bound, no
  incremental sort (11 §9).
- No skew (MCV) buckets; no parallel combined budget; skew pricing TODO
  P2-11b (11 §6, §17 row 13).
- Strategy by rule, not cost (planner TODO P4-06); no `MIXED` spill arm
  (11 §8).

### Measured cost

- `lessRows`/`sortTailWithCTIDs`: q16 shows 34% of time in sort-compare
  paths; planner bounded/incremental sorts are scoped (P4-04/05) but the
  executor compare speed is out of planner scope (11 §17 row 12, OPEN).
- Skew: unmeasured at the executor; needs Phase-1 MCV input first (planner
  P2-11b), so executor skew work is sequenced last inside EX3 (13 §6).

### Why PG is faster

PG avoids the full sort (`LIMIT` bound), avoids re-sorting presorted
prefixes, compares abbreviated keys, and never spills its hottest keys.
goopg always sorts fully, always compares full rows on fallback, and spills
hot keys with cold ones.

### Out-of-scope notes

- P4-04 (bounded sort) and P4-05 (incremental sort) are planner-path items;
  the executor half (bound-aware `sortOp`, presorted-prefix input contract)
  is 13 §6. P4-05 is BLOCKED on executor support (ledger `take2-P2-08`
  class per 11 §9) — the dependency runs planner→executor here, reversed
  from the usual direction; 13 §8 records it.
- Executor sort-compare speed (`lessRows`) without a planner shape change
  is legitimate EX3 work with a pure-timing gate (no plan movement possible).

---

## 10. G-EX8 — Parallel executor gaps

### PG mechanism (10 §10)

- `Gather`/`GatherMerge` with per-worker queues; partial paths below,
  leader finalises; parallel hash reuses structures in shared memory with
  barrier-synchronised phases; ordered exchange via `SharedTuplestore`
  (10 §10).

### goopg mechanism (11 §10)

- `gatherOp` fans out to `BuildWorker` subtrees streaming materialised rows
  back; `gatherMergeOp` preserves order; parallel hash shares one build
  (`sharedHashBuild`, `parallelBuildLazyHashTable`); workers transfer only
  `ArenaID == 0` or permanent-arena Datums (11 §10).
- Three live gaps: `buildRec` has **no `Gather` arm** (parallel queries fall
  back to legacy `Build`, compiled predicate unreachable); per-worker hash
  counters die with the worker (no EXPLAIN ANALYZE parallel stats as PG
  shows); plain `IndexScan` not a parallel partial root (planner P5-03,
  a `MISSING-NODE` entry) (11 §§1, 10).

### Measured cost

- Parallel Q6 0.838 s vs PG 0.203 s (4.1×): the serial residual scaled, not
  compounded — parallelism is not the current binding constraint on Q6, but
  every parallel query runs the legacy builder, so all EX1–EX4 wins must be
  re-proven on the parallel path (13 §7: serial control arm on every item).
- TPC-DS harness runs `GOGC=off`; parallel+GC interaction is unmeasured.

### Why PG is faster

PG's parallel path is the same compiled path with more workers; goopg's
parallel path is a different (legacy) builder, so parallel queries miss
every compiled fast path by construction.

### Out-of-scope notes

- Cost-model parallelism (`cost_gather`, partial paths, P5-01…P5-08) stays
  planner work (07 §§3.6, 08 §8). EX5 covers executor mechanics only:
  shared build/probe, Gather ordering, worker stat surfacing, slab parity
  for `Gather` (13 §7).

---

## 11. Closed: landed executor items (record, not work)

| item | effect | evidence |
|---|---|---|
| Numeric text-decode fast path | 23.40 → 11.51 s serial, 2.0× | 11 §17 row 2; `DESIGN.md:41,747`; `benchmark-results-take5.md:32` |
| Copy-after-filter + 6-of-16 deform (take5) | 11.51 → 6.63 s; `cloneRowOwned` 26.6% → 1.16% CPU | row 3; `benchmark-results-take5.md:20-32,276-277` |
| Triple-copy `PageGetHeapTuple` + `evalExpr` boxing (take6) | alloc −95.8% (18.8M → 0.80M); 6.55 → 4.49 s, 1.46× | row 4; `benchmark-results-take6.md:16-44,68-72` |
| Per-value `ToLower` removal | 4.64% → absent from profile | row 5; `perf-optimize-take6/RESULTS.md:17-34` |
| CLOG-horizon atomics + subxact fast path | 10.86% → 0.54%; Q14 1.26×, Q3 1.15×, Q10 1.17×, byte-identical | row 5; RESULTS §§3, 63–83 |
| Hint-bit follow-up: CLOG consult off the per-tuple path | differential test green | row 15; RESULTS §8 (`§§131–172`) |
| Compiled scan prefilter (take7) | 4.563 → 3.792 s serial, 1.009 → 0.838 s parallel, 1.20× | row 6; `benchmark-results-take7.md:15-60,146` |
| Datum 64 B → 48 B (M0107-0002) | reduction shipped, test-pinned | row 7; `0107-0002-datum-48b-arena-id-merge.md`; `datum_arena_test.go:17-19` |

All eight are `[x]` in `TODO_EXECUTOR.md` with these pointers. The Q14/Q3
numbers in the atomics row are byte-identical-plan wins and are NOT spill
witnesses — see §13.

---

## 12. Negative results that must be preserved

| attempt | result | preservation rule |
|---|---|---|
| Narrowing without Datum shrink (`FINDING-p401-alone-is-not-enough`) | `orders` build 128 → 64 batches, **not 1**; 48 B/column co-dominant with width | narrowing and footprint move together (13 §8: EX1 sizes the batching math EX3 implements) |
| P4-01b leaf narrowing (reverted, wrong answers) | Q2/Q5 0 rows, Q18 wrong tuples; faster-and-wrong | projection must be a path property with fixup, never a leaf swap (08 §7); executor deform-narrowing keys off the plan's target list, never its own pruning |
| Pointer-chasing / GC-tracing as the Datum explanation (take2, ruled out) | no effect; footprint itself is the tax | do not re-litigate representation snorkelling; measure bytes × rows |
| Q14/Q3 "spill efficiency" (FINDING-workmem-advantage §2b CORRECTION) | re-attributed to plan choice (merge over full 6M-row index scan) | STALE as spill claim (§13); executor spill items gate on spilling shapes, not these queries |
| P2-02b split (MEASUREMENT-p202b-width-vs-gather) | residual width ~87% / lost Gather ~13% in isolation | EX1 before EX5 recovery; parallelism does not substitute for narrowing |
| `filterOp` still interprets after take7 (~0.33 pp) | known, not a take7 defect | EX4's first item, not a regression |

---

## 13. STALE marking and ledger appendix

**Honored STALE (11 §17 row 16, §18 item 3).** The sentence "Q14 24× / Q3
11× / Q10 4× at parity, PG ~1 s each at same setting — dominant remaining
cost per FINDING-workmem §2" (07 §6 spill row) is superseded by the finding
it cites: `FINDING-workmem-advantage.md` §2b corrects "spill efficiency" to
"plan choice" with plans reproduced. The ratios hold; the mechanism does
not. No EX3 gate may use Q14/Q3/Q10 as spill witnesses; 13 §6 names the
spilling shapes (Q4/Q7/Q13-class) instead.

**Ledger appendix.** Grep `take2-executor` over `.ralph/deferral_ledger.md`
returns one row:

- `take2-executor-residual` (2026-09-03): equal-cardinality width residual,
  Q9 1098/1642/2090/3164 B vs PG 23/32/54/81 B, 97 MB / 8 batches vs
  38 MB / 1, 63.8 s vs 6.2 s; resume at planner P4-01; P4-01b revert noted.

No `take2-P7-03*` rows are present under that key. The executor backlog is
therefore ledgered by EX0-01 (`TODO_EXECUTOR.md`), which files one row per
open gap in §§3–10 with the 10 § upstream citation each requires.

---

## 14. Summary

goopg's executor is structurally complete (every PG node shape has a
counterpart, 11 §§4–10) and allocation-immature: 48 B columns, whole-row
deform, scan-time detoast, per-boundary clones, per-level probe
re-materialisation, spill encode/I/O with no run discipline
(Stack-walk elimination landed; residual TBD), one compiled expression
site, and a legacy parallel builder. The landed take5–take7 chain (23.40 →
3.79 s on Q6) proves per-item halving works; the remaining 3.8–4.1× is owned
by the eight gaps above, sequenced by [13-executor-target-design.md](13-executor-target-design.md):
instruments first, narrowing before batching, clones before cascade,
serial before parallel.

(End of file)
