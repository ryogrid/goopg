# 0118-0098 — `intra-grant-inplace-db.spec` PROMOTED: VACUUM waits on a concurrent `GRANT … ON DATABASE` (M0118-0009)

Status: accepted
Milestone: M0118 (Upstream Isolation Spec Suite Pass-Through), task M0118-0009
Spec: `postgres/src/test/isolation/specs/intra-grant-inplace-db.spec`
Test: `TestPort_IsolationIntraGrantInplaceDb` (strict, `runIsoSpecStrict`)

## Summary

`intra-grant-inplace-db.spec` `failed`→`pass`, the single permutation byte-identical
to PostgreSQL 18.3. Builds directly on the catalog-surface enabler 0118-0096
(`pg_database.datfrozenxid` column) — that loop surfaced the column `snap3` reads
but explicitly deferred the one remaining behavioural gap: the `<waiting ...>`
serialization between an ACL change and a concurrent in-place datfrozenxid update.
This loop closes that gap.

## The spec

```
permutation snap3 b1 grant1 vac2(c1) snap3 c1 cmp3
```

- `s3 snap3`  — records the current `datfrozenxid` into a temp witness table.
- `s1 b1`     — `BEGIN;`
- `s1 grant1` — `GRANT TEMP ON DATABASE isolation_regression TO regress_temp_grantee;`
- `s2 vac2`   — `VACUUM (FREEZE);` — **must block** (`<waiting ...>`); the `(c1)`
  annotation declares the blocker is released by step `c1`.
- `s3 snap3`  — records `datfrozenxid` again (while `vac2` is blocked).
- `s1 c1`     — `COMMIT;` — unblocks `vac2`, which then completes.
- `s3 cmp3`   — asserts `datfrozenxid` did not retreat → `(0 rows)`.

The spec's header states the invariant being tested:

> GRANT's lock is the catalog tuple xmax. GRANT doesn't acquire a heavyweight
> lock on the object undergoing an ACL change. In-place updates, namely
> datfrozenxid, need special code to cope.

## Upstream mechanism

In PostgreSQL `GRANT … ON DATABASE` performs a normal `heap_update` of the
`pg_database` row, setting that tuple's `xmax` to the granting transaction's XID
but taking **no** heavyweight lock on the database object. A database-wide
`VACUUM` ends with `vac_update_datfrozenxid`, which advances `datfrozenxid` on the
`pg_database` row using `heap_inplace_update_scan` →
`systable_inplace_update_begin`. That path locks the tuple and, finding a
still-in-progress `xmax`, waits via `XactLockTableWait` for the ACL-change
transaction to commit or abort before rewriting the row in place. Hence `VACUUM`
blocks behind the uncommitted `GRANT`, and resumes the instant it commits.

## goopg has no `pg_database` heap tuple

goopg serves `pg_database` from a virtual builder; `datfrozenxid` is *computed*
(`InMemory.DatFrozenXID()` = `min(relfrozenxid)` across user heaps, design
0118-0096), never stored as an MVCC heap tuple. There is therefore no real
`xmax`, no `heap_inplace_update_scan`, and no tuple lock to wait on. Rather than
build a runtime shared-catalog MVCC-tuple subsystem (the Effort-L path the prior
loop deferred), this change **replays the observable wait directly** using
goopg's existing transaction-wait primitive. The isolation runner decides
`<waiting ...>` purely by a 300 ms timeout (see the `iso-runner-blocking-is-timing-only`
note), so a faithful block on the ACL-change XID reproduces the exact output.

## Implementation

Four small, narrowly-gated pieces:

1. **Parser** (`internal/parser/parser.go`, `ast.go`) — the GRANT/REVOKE no-op arm
   now scans the consumed tokens for an `ON DATABASE` object class and sets a new
   `CompatNoopStmt.DatabaseACL` flag. Pure additive; ordinary GRANT/REVOKE
   parsing is unchanged.

2. **Catalog marker** (`internal/catalog/catalog.go`) — new
   `InMemory.dbACLChangeXID atomic.Uint32` plus `SetDatabaseACLChangeXID(xid)` /
   `DatabaseACLChangeXID()`. This is the in-memory stand-in for the `pg_database`
   tuple `xmax`: the XID of the most recent `GRANT/REVOKE … ON DATABASE`. Atomic
   so the VACUUM reader never contends on `c.mu`.

3. **Executor** (`internal/executor/operators_ddl.go`, `execCompatNoop`) — when a
   `CompatNoopStmt.DatabaseACL` statement runs, it materializes this
   transaction's writer XID (`MaterializeWriterXID`) and records it as the
   database ACL-change xmax. Inside an explicit transaction (the spec's `b1;
   grant1; … c1`) that XID stays in-progress until `COMMIT`. (In autocommit the
   server short-circuits a `GRANT` before the executor, and the transaction
   commits immediately, so no observable wait arises either way.)

4. **VACUUM** (`internal/executor/operators_vacuum.go`, `vacuumOp.Next` +
   `waitForDatabaseACLChange`) — a *database-wide* VACUUM (`len(vs.Targets) == 0`,
   the only case PG runs `vac_update_datfrozenxid`) first calls
   `mvcc.WaitForXID` on the recorded ACL-change XID. `WaitForXID` returns
   immediately when that XID has already committed/aborted (or none was recorded),
   so this is a no-op in the overwhelmingly common case; it blocks only while an
   ACL change is genuinely uncommitted.

## Why the blast radius is nil

- A targeted `VACUUM table` (`len(vs.Targets) != 0`) never consults the marker —
  it matches PG, where the per-relation path does no `pg_database` update.
- With no concurrent ACL change the marker is `0` (`InvalidTransactionID`) and
  the wait returns instantly.
- After any `GRANT … ON DATABASE` commits, the marker holds a finished XID;
  `WaitForXID` on a committed/aborted XID returns instantly, so a later
  database-wide VACUUM never spuriously blocks. XIDs are monotonic in goopg, so
  the marker is never confused with a future transaction.
- No MVCC/storage/WAL/heap surface is touched; `datfrozenxid` remains computed.

## datfrozenxid does not retreat

`cmp3` returns `(0 rows)` — `datfrozenxid` never goes backwards. goopg's
`DatFrozenXID()` is a wraparound-safe `min(relfrozenxid)` and `VACUUM (FREEZE)`
only ever advances `relfrozenxid`, so the witnessed horizon is monotonic by
construction; the result matches PG with no extra work.

## Gates

- `TestPort_IsolationIntraGrantInplaceDb` strict PASS (byte-identical).
- Sibling maintenance/ACL isolation specs PASS, no regression:
  `VacuumConflict`, `VacuumNoCleanupLock`, `ClusterConflict`, `TruncateConflict`,
  `CreateTrigger`. (`VacuumConcurrentDrop` fails identically on clean HEAD — a
  pre-existing timing-sensitive `<waiting>` flake on this WSL2 host, unrelated:
  it uses only targeted `VACUUM/ANALYZE`, which this change does not touch.)
- `internal/parser` + `internal/catalog` units PASS; executor
  `Vacuum|Grant|CompatNoop|Freeze` units PASS.
- `go build ./...`, `go vet`, `gofmt -l` clean.
- pgbench smoke = pre-commit hook.

## Remaining in M0118-0009

`intra-grant-inplace` (the `pg_class` sibling — `ALTER TABLE … ADD PRIMARY KEY`
`<waiting ...>` behind a `FOR KEY SHARE` on the `pg_class` row), `horizons`
(JSON `->` operator + `EXPLAIN (FORMAT json)` heap-fetch plan parity), `stats`
(`pg_stat_force_next_flush` + cumulative-stats infrastructure),
`prepared-transactions{,-cic}` (two-phase commit) remain `failed`. Group stays
open.
