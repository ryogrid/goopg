# fix-04 — COPY FROM multi-insert batching (P0 for bulk load)

## Problem (evidence)

`pgbench -i -s 100`: goopg COPY ingest **529 s** vs PG **9.4 s** (56×;
18.9 k vs 1.06 M rows/s), with 4–8 s stalls every ~400 k rows; total init
539 s vs 15.8 s (34×). Diagnostic profile during COPY (`copydiag/`,
`-s 20`): **~60 % CPU is the fix-01 `runtime.Stack` storm** (cum; COPY
appends WAL per row), 16.6 % `syscall.pwrite` (many small writes), ~11 %
`memmove`/`memclr`; only 7.4 flush barriers/s — **not** fsync-bound.
Structure: goopg routes COPY rows one-at-a-time through the insert path —
per row: one heap insert (buffer lookup/pin/lock), one WAL record, one
append handoff.

## PostgreSQL approach (03 §7)

- copyfrom.c buffers ≤1000 tuples / 64 KB (`CopyMultiInsertInfo`), then
  `heap_multi_insert()` fills each page in an inner loop and emits **one
  `XLOG_HEAP2_MULTI_INSERT` record per page batch**; freshly initialized
  pages use `XLOG_HEAP_INIT_PAGE`/`REGBUF_WILL_INIT` (no per-tuple offsets
  logged).
- `BulkInsertState` (`BAS_BULKWRITE` 16 MB ring) keeps the current target
  buffer pinned and bulk-extends the relation — no per-tuple buffer-mapping
  lookups, no shared-pool pollution.
- pgbench uses `COPY … WITH (FREEZE ON)` (pgbench.c:5040): tuples are born
  frozen, pages born all-visible+all-frozen in the VM — which also makes
  the post-load vacuum near-free (03 §10) and the PK build read a compact
  heap (03 §8).

## Design

Stage the port of PG's three layers; each stage is independently landable.

### Stage 1 — tuple batching + heap multi-insert

1. In the server-side COPY FROM executor path (the COPY ingest operator in
   `internal/executor` — the row loop that currently calls the single-row
   insert), accumulate decoded rows into a batch
   (`copyMultiInsertBuffer`: cap 1000 tuples / 64 KB, PG's constants).
2. New heap API `internal/storage` / `internal/access/heap`:
   `HeapMultiInsert(rel, tuples []…, bulk *BulkState)`:
   - acquire one target page; fill with as many tuples as fit
     (`RelationPutHeapTuple` loop equivalent);
   - emit **one canonical `XLOG_HEAP2_MULTI_INSERT`** record per filled
     page (the canonical-record builders in `internal/catalog`
     (`buildCanonicalPayload`/FPI siblings) gain a multi-insert variant —
     format per PG `heapam_xlog.h` `xl_heap_multi_insert` +
     `xl_multi_insert_tuple`, so `pg_waldump` and the PG-standby feed stay
     byte-compatible);
   - mark dirty once per page (`MarkDirtyWithLSN`).
3. **Recovery siblings** (hard requirement, sibling-paths rule): implement
   `XLOG_HEAP2_MULTI_INSERT` redo in `internal/wal/recovery.go`
   (`ApplyRecord` RmgrHeap2 dispatch) *and* `stream_replayer.go`.
4. Triggers/constraints: pgbench tables have none at COPY time, but the
   batch path must fall back to row-at-a-time when the table has
   BEFORE/INSTEAD triggers, volatile defaults, or FKs needing per-row
   checks — mirror copyfrom.c's `insertMethod` decision
   (CIM_SINGLE/CIM_MULTI).

### Stage 2 — BulkInsertState analogue

`BulkState{curBuf PinnedPage, extendBy int}` in the heap layer: keep the
current target page pinned across batches; extend the relation several
blocks at a time (PG's `already_extended_by` bookkeeping); use a small
private ring so a big COPY does not evict the shared pool (goopg's pool has
per-slot state — a ring strategy is a pin-policy, not a new pool).

### Stage 3 — FREEZE support (unlocks vacuum + VM wins)

Honor `COPY … WITH (FREEZE)` semantics (allowed only when the table was
created/truncated in the same transaction — parser already accepts the
option?; verify, else add): write tuples with frozen xmin markers, set
page-level all-visible, and set the visibility map bits during load
(equivalent of `visibilitymap_set(ALL_VISIBLE|ALL_FROZEN)`), so the
post-load vacuum skips the heap (fix for the 4.5× vacuum gap without
touching vacuum itself).

Out of scope (record in deferral ledger when landing): sort-based btree
build for the PK phase (03 §8) — separate follow-up; COPY parse-side
(protocol) optimizations.

## Expected lift (arithmetic)

Per-row fixed costs (WAL framing + append handoff + buffer lookup) divide
by the page-batch size (~200 rows/page for pgbench_accounts): Stage 1 alone
should move COPY from ~19 k to >150 k rows/s (fix-01 contributes its 25 %
independently). Stage 2 removes buffer churn and the periodic stall
pattern; Stage 3 removes the vacuum re-scan. Target: `pgbench -i -s 100`
generate phase ≤ 30 s (≥17×), total init ≤ 45 s.

## Risks

- WAL-format addition: multi-insert record must be byte-PG-accurate
  (pg_waldump + standby-attach gates); FPI interaction on first-touch pages.
- Crash mid-batch: redo must be idempotent (page-level records make this
  natural; test kill-9 mid-COPY).
- Executor sibling paths: COPY has simple- and extended-protocol entries
  (memory: wire BOTH), plus `COPY FROM` vs `COPY FROM STDIN` framing.
- TPC-H loader and regress COPY tests exercise this path — full
  regress-port re-run mandatory (M0106 rule).

## Verification plan

1. Unit: multi-insert page-fill + redo round-trip (write → crash → replay →
   row set equal); FREEZE VM-bit test.
2. `scripts/pg-regress-runner.sh copy copy2 insert` + full regress sweep.
3. `pg_waldump` diff of a multi-insert WAL stretch vs PG's for the same
   COPY input (oracle-diff style).
4. Kill-9 mid-`pgbench -i` crash-recovery e2e.
5. `scripts/tpch-spotcheck.sh` (loader + row counts) and pgbench smoke.
6. Perf acceptance: `pgbench -i -s 100` timing vs this run's 539 s
   baseline; re-check the c=50 simple-update headline is unchanged
   (INSERT path in pgbench_history shares code).
