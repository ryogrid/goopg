# root-0022 — TOAST chunk_id restart durability

## Problem

goopg has a real, wired-up TOAST implementation (M0046-0006,
`internal/executor/toast.go`): any column value over `ToastThreshold` (2000
bytes) is chunked into `ToastMaxChunkSize`-byte (1996) rows in a synthetic
per-table side relation (`ToastRelFor`, `RelOid = mainRel.RelOid +
100_000_000`) and replaced in the main tuple with a 12-byte pointer
(`chunk_id | total_len | num_chunks`). Both INSERT and UPDATE-new-row-version
go through it (`writeHeapRowReturning` → `ToastLargeColumnsIfNeeded`).

Each TOASTed value's `chunk_id` comes from a single package-level counter,
`toastOIDCounter` (an `atomic.Int64`), incremented once per `toastStore`
call. That counter is **process-local and always starts at 0** — it is never
persisted and, before this change, was never reseeded from existing on-disk
state at startup. The TOAST relation's rows, however, are ordinary heap
tuples and **do** survive a restart (checkpointed/flushed like any other
relation's pages).

Consequence: the first TOASTed value written after **any** restart (clean
or unclean — no crash required) reissues `chunk_id = 1`, colliding with
whatever `chunk_id` 1 already resides in the same table's TOAST relation
from before the restart. `DetoastValue`'s reassembly scans the TOAST
relation for every MVCC-visible row whose `chunk_id` matches the pointer's
`oid` and unconditionally overwrites `chunks[seq]` for each match
(`toast.go`, `DetoastValue`) — with no notion of "generation" separating a
pre-restart value from a same-numbered post-restart value, a later-scanned
(higher block number, i.e. more recently written) row's bytes silently win
for any overlapping `seq`, splicing two unrelated logical values together.

Surfaced by WordPress-on-goopg (deferral ledger, 2026-07-02 rows): after
heavy admin-dashboard traffic wrote several oversized (>8 KB) `wp_options`
transients (theme-patterns / block-CSS caches) and a clean `goopg` restart,
the *neighboring* `wp_user_roles` option — itself a ~3992-byte value, over
the 2000-byte `ToastThreshold` and therefore also TOASTed — read back with a
foreign transient's bytes, fataling WordPress
(`array_keys(): Argument #1 must be of type array`). Reproduced twice on
independent fresh installs; two narrower synthetic repros (a single TOASTed
row updated once post-restart) came back negative, because a single-value
collision-with-itself doesn't produce cross-row corruption — the bug needs
at least two *distinct* TOASTed values sharing one table's TOAST relation,
one written before and one after the restart, which is exactly what
WordPress's real option-upsert churn produces.

## Upstream semantics

PostgreSQL's TOAST OID is a real `Oid`, allocated via
`GetNewOidWithIndex(toastrel, toastidx, ...)`
(`postgres/src/backend/access/heap/toast_internals.c`, `toast_save_datum`),
which retries against the TOAST table's own unique index on
`(chunk_id, chunk_seq)` until it finds a value with no existing row —
`Oid` allocation is global and collision-checked, not a private
monotonic counter, so it is inherently restart-safe (a fresh backend
resumes from the shared `pg_class`-derived OID counter, which is itself
checkpointed).

## Design

goopg's TOAST relations have no unique index to collision-check against
(`DetoastValue` full-scans instead), so mirroring PG's retry-on-collision
approach exactly would require adding index machinery this slice doesn't
need. Instead, this follows the project's existing "sequence-style" restart
pattern used for the catalog OID counter itself
(`cat.AdvanceNextOIDPast`, `internal/initdb/open.go`, M0106-0013): **scan
the durable state once at startup and advance the in-memory counter past
whatever was found**, guaranteeing forward-only, collision-free allocation
for the remainder of the process's life without needing per-call collision
checks.

New exported surface (`internal/executor/toast.go`):

- **`AdvanceToastOIDCounterPast(used uint32)`** — CAS-loop bump of
  `toastOIDCounter`, mirroring `AdvanceNextOIDPast`'s monotonic-only-forward
  contract.
- **`MaxToastChunkIDInRel(pool, toastRel) (uint32, bool, error)`** — scans
  every *physically present* tuple (no MVCC visibility filter — even an
  invisible-but-still-resident row's `chunk_id` must never be reissued,
  since `DetoastValue` itself only filters by visibility for read
  reconstruction, not to decide what's "used") and returns the highest
  `chunk_id`. Short-circuits via `Pool.Exists` before touching `NBlocks`/
  `Pin`, so a table that never TOASTed anything doesn't get its TOAST file
  silently created as a side effect
  (see `goopg_smgr_ocreate_recreates_removed_files`).
- **`SeedToastOIDCounter(pool, mainRels []storage.RelFileNode) error`** —
  the driver: for each main-table `RelFileNode`, derive its TOAST relation
  and advance the counter past the max found in it.

**Wiring** (`internal/initdb/open.go`, right after the existing M0106-0013
OID-advance loop that follows `loadUserTablesFromHeap`): iterate
`cat.AllTables()`, compute each table's `RelFileNode` via
`cat.RelFileNode(tbl)`, and call `executor.SeedToastOIDCounter`. Runs
unconditionally — even on the M0114 catalog-cache-hit fast path — since the
counter always resets to 0 on process start regardless of how the catalog
was loaded; a reseed failure is logged and non-fatal (matches the sibling
`loadStatisticsFromHeap`/`loadUserIndexesFromHeap` non-fatal-warn style
immediately below it), leaving the pre-fix behavior rather than blocking
startup.

## Semantics and caveats

- This closes the *counter-reset* collision class. It does **not** add
  TOAST chunk garbage collection: orphaned chunks from an UPDATE/DELETE of
  the owning row are never reclaimed (a pre-existing, separate gap — same
  as upstream's own dependence on VACUUM for TOAST GC, but goopg has no
  TOAST-aware VACUUM pass at all yet). Orphans inflate the TOAST relation
  but, after this fix, can never collide with a live value's `chunk_id`.
- `MaxToastChunkIDInRel`'s full-relation scan is O(rows) per table at every
  startup. Acceptable at WordPress/typical-goopg-instance scale (mirrors
  the cost already paid by `loadStatisticsFromHeap`/`loadUserIndexesFromHeap`
  in the same startup path); a pathologically large TOAST relation would
  slow startup, not correctness — no change needed for this slice.
- Still open (deferral ledger, not addressed by this slice): TOAST chunk
  writes (`writeHeapTupleToRel`) dirty pages via plain `Pool.MarkDirty`
  rather than the change-record/FPI-per-insert discipline the main heap
  insert path uses (`markHeapInsertDirty`/`LogHeapInsert`), so a chunk
  written into an already-dirty TOAST page within the same checkpoint epoch
  as an earlier chunk on that page is not independently WAL-protected — an
  **unclean crash** before the next checkpoint could still lose it. This is
  a narrower, secondary durability gap (requires a crash, not just a
  restart) distinct from the counter-collision bug this slice fixes;
  tracked in the ledger for a follow-up loop.

## Tests

- `internal/executor/toast_test.go`:
  - `TestToastOIDCounterCollisionAcrossRestart` — end-to-end within the
    executor package: writes value A, resets `toastOIDCounter` to simulate
    a restart, calls `SeedToastOIDCounter` (exactly as `open.go` now does),
    writes value B in the same table, and asserts neither value's bytes
    leak into the other. Verified to fail (byte-exact reproduction of the
    reported corruption) with the reseed call removed.
  - `TestMaxToastChunkIDInRelNoFile` — the `Pool.Exists` short-circuit
    doesn't create a TOAST file as a side effect.
  - `TestSeedToastOIDCounterAdvancesPastExisting` — focused unit test for
    the seeding helper.
- `internal/testport/toast_oid_restart_durability_test.go`:
  `TestPort_ToastValueSurvivesRestartWithoutCollision` — real cluster
  process restart (`cluster.Stop`/`Start`, mirrors
  `serial_sequence_durability_test.go`): TOASTed value pre-restart, a
  second distinct TOASTed value post-restart in the same table, both
  verified byte-exact via `SELECT count(*) ... WHERE v = repeat(...)`.
