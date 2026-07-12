# fix-05 — Single-pass startup recovery (P2, operational)

## Problem (evidence)

Opening a scale-100 data directory takes ~28 s. The cumulative allocation
profile shows **200 GB (81 % of all allocation) in `wal.readStreamFrom` /
`os.ReadFile`**, all reached from `wal.ReadAll(walDir, 0)` — and `ReadAll`
is called by **each** of the **26** `internal/initdb/*_ddl_recovery.go`
modules (view, index, domain, matview, role-config, cast, aggregate,
tablespace, operator, foreign-server, access-method, transform, …). Every
module independently re-reads the *entire* WAL from LSN 0, slurping each
16 MB segment into memory (`os.ReadFile` cum ≈ 34 GB ≈ 20 full passes over
the 1.7 GB WAL, plus far more transient allocation from per-record
slices). This burned the benchmark
driver's readiness window and is a real operator-facing startup cost that
grows linearly with WAL retention × number of DDL recovery modules (26
today, and each new recoverable DDL type adds another full pass).

## PostgreSQL approach (03 §9)

One pass: `PerformWalRecovery()` starts at the checkpoint REDO pointer and
dispatches each record through the static resource-manager table
(`GetRmgr(record->xl_rmid).rm_redo(...)`, `rmgrlist.h`). Every subsystem
sees every record exactly once, in LSN order.

## Design

1. **One reader, many handlers.** Introduce a DDL-recovery dispatcher in
   `internal/initdb` (or `internal/wal`):
   ```go
   type ddlRecoveryHandler interface {
       // RecordTypes returns the goopg-private record types it consumes.
       RecordTypes() []wal.RecordType
       Apply(rec wal.Record) error
   }
   ```
   Each existing `*_ddl_recovery.go` module registers a handler; a single
   `runDDLRecovery(walDir, startLSN)` reads the WAL **once** and dispatches
   by record type — the goopg-private-WAL analogue of PG's rmgr table
   (goopg's two-mechanism catalog-durability rule keeps these records
   separate from heap redo; the dispatcher lives beside, not inside,
   `ReplayFromDir`).
2. **Stream, don't slurp.** Replace `ReadAll`'s whole-segment
   `os.ReadFile` + per-record slice retention with the existing streaming
   reader (`readStreamFrom` already iterates; the allocation hotspot is
   returning materialized `[]Record` for the whole WAL). The dispatcher
   consumes records via a callback, so peak memory is one record.
3. **Start from the right LSN.** Most DDL-recovery modules only need
   records since the last checkpoint marker (their state is also
   checkpointed / rebuilt from catalog heaps); audit each module and pass
   the checkpoint redo LSN instead of 0 where the module's design doc
   permits. Modules that genuinely need full history (if any) keep 0 but
   share the same single pass.
4. Startup ordering: run the unified pass at the point the *first* current
   module runs today; handlers must tolerate records for objects later
   dropped (they already do — same records, same order).

## Expected lift

Startup on the bench data dir: ~28 s → ~2–3 s (single 1.7 GB streaming pass,
no 200 GB alloc churn). Bigger WAL retention (replication slots) scales
linearly instead of ×26. Also removes the GC spike that currently greets
the first queries after startup.

## Risks

- Inter-module ordering: today each module sees the full WAL in isolation;
  with one pass, per-record handler order within the same LSN must be made
  explicit (register in dependency order; add a test with a mixed DDL WAL).
- A module whose Apply mutates state another module reads must not observe
  partial state — audit for cross-module reads (expected none; they write
  disjoint catalog domains).
- Behavior must be identical for WAL containing dropped/recreated objects.

## Verification plan

1. Unit: dispatcher test with a synthetic WAL containing interleaved DDL
   record types; assert per-module end state equals the current
   (multi-pass) implementation on the same input.
2. E2E: create views/indexes/domains/etc. → restart → catalog intact
   (existing per-module recovery tests re-run unchanged).
3. Startup-time measurement on the retained bench data dir
   (`tmp/perf-optimize2/…/goopg-data`) before deleting it: target < 5 s.
4. Units + pgbench smoke; no WAL format change ⇒ no regress sweep needed,
   but run one recovery-focused testport pass.
