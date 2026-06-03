# Practice card — WAL / MVCC / replication change

**Load when** the task touches `internal/wal/`, `internal/mvcc/`, recovery,
checkpoint, replication/standby, LSN, or subscription.

**Why:** these are the highest-blast-radius subsystems — a visibility or replay
bug corrupts data silently and only shows under concurrency or after a restart.

## Must-run gate

- **Race detector** on the touched packages: `go test -race ./internal/wal/... ./internal/mvcc/...`.
  Concurrency bugs here do not reproduce single-threaded.
- **Recovery / replication path**: run the relevant recovery TAP / E2E
  (`TestE2E_PhysicalReplication`, recovery testport suites) — a unit test on the
  primary will not catch a standby-visibility regression.
- After WAL **format** changes, old data dirs may be unreadable — **re-init**
  before testing (see [[codec-storage-change]]).

## Known traps

- **Standby hot-read MVCC visibility:** replaying a commit must advance the
  visibility horizon (`ReplayXactCommit` → nextXID) or standby reads see stale /
  missing rows (M0094-0005). Verify reads on the standby, not just the primary.
- **Checkpoint durability:** don't silently rely on OS page cache; fsync the
  intended files/dirs (0089). A "passing" test on a warm cache hides this.
- **Continuous PG-compat:** PG can attach as a standby at any time, so
  on-disk/catalog/`pg_control`/`pg_internal.init` updates must hold for ongoing
  operation (DDL/checkpoint/promotion), not just at initdb time
  ([[feedback_m0106_continuous_pg_compat]]).

## Oracle

Cite the upstream file you mirrored (`postgres/src/backend/...`) in the design
doc/comment. Compare behavior against `./postgres/local_install` PG 18.3.

## If you must defer

Record a deferral-ledger line (what landed / deferred / resume point / why);
never close with a silent forward reference.
