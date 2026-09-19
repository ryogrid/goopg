# 0118-0117 — `intra-grant-inplace` PROMOTED: perm 10 — `DELETE FROM pg_class` virtual-tuple delete + caught `cache lookup failed` (M0118-0009)

Status: accepted
Spec: `postgres/src/test/isolation/specs/intra-grant-inplace.spec`
Test: `TestPort_IsolationIntraGrantInplace` (strict, `runIsoSpecStrict`)

## Summary

This is the **promotion** that closes `intra-grant-inplace`: all **10 permutations**
are byte-identical to PG 18.3. The nine prior enabler loops (designs 0118-0098,
0118-0109, 0118-0113, 0118-0114, 0118-0115, 0118-0116) advanced the first
divergence to L235 (perm 10); this loop closes perm 10.

Permutation 10 is `b1 drop1 b3 sfu3(c1) revoke4(sfu3) c1 r3`. In goopg's locally
adapted spec copy, `drop1` is **`DELETE FROM pg_class WHERE relname =
'intra_grant_inplace'`** (a direct virtual-catalog tuple delete), not `DROP
TABLE`. The expected behaviour:

1. `drop1` deletes the `pg_class` tuple inside `b1`'s transaction — PG's
   `heap_delete` stamps the tuple's **delete xmax** and keeps the row visible to
   other sessions until `b1` commits.
2. `sfu3` (`SELECT relhasindex FROM pg_class WHERE oid = …::regclass FOR UPDATE`)
   takes a `LockTuple` on that row and then `<waiting ...>` on its delete xmax.
3. `revoke4` (`DO $$ … REVOKE … EXCEPTION WHEN others … $$`) blocks in
   `LockTuple()` behind `sfu3`.
4. `c1` commits the delete. `sfu3` wakes, the tuple is gone → **0 rows**, and it
   holds no tuple lock.
5. `revoke4` proceeds the instant `sfu3` releases its lock (**before** `r3`),
   re-reads the now-deleted `pg_class` tuple via `SearchSysCacheLocked1`, fails
   with the internal `cache lookup failed for relation <oid>` elog, and its DO
   block `EXCEPTION WHEN others` handler re-raises a REDACTED `WARNING`.

## Changes

### 1. pg_class delete-xmax store (`internal/catalog/catalog.go`)

New `tablePendingDropXID map[oid]xid` mirroring `tableACLChangeXID`, with
`SetTablePendingDropXID` / `TablePendingDropXID` / `ClearTablePendingDropXID`.
goopg has no `pg_class` heap tuple, so a deferred delete records its writer XID
here as the tuple's delete xmax; a concurrent rowmark waits on it.

### 2. `DELETE FROM pg_class` → transaction-deferred table drop (`operators_storage.go`)

`deleteOp.Next` intercepts a delete whose target is `pg_class`
(`tryPgClassCatalogDelete`): it extracts the relation OID from a `relname =
'<name>'` or `oid = <n>` predicate (`pgClassDeleteTargetOID`), records the
delete xmax (`SetTablePendingDropXID`), and **defers** the actual catalog
removal to COMMIT via the existing `AddPendingTableDrop` machinery (the relation
stays visible to concurrent readers until then; `ApplyPendingTableDrops` removes
it at commit and clears the xmax). Gated on an explicit transaction +
`pg_class` target ⇒ nil blast radius.

### 3. Rowmark awaits the delete xmax + retracts on empty (`operators_lockrows.go`)

`maybeRecordPgClassRowMark` now also `waitTablePendingDrop(relOID)` after
`waitTableACLChange` (shared deadlock-aware core extracted as
`waitPgClassTupleXID`, `operators_ddl.go`). It records `(pgClassRowMarkOID,
pgClassRowMarkXID)`; when the post-wait scan yields **0 rows** (the relation was
deleted and committed), `drainAndStamp` retracts the mark
(`Catalog.ClearPgClassRowMark`) — PG holds no tuple lock when it locks nothing.

### 4. Poll-based rowmark-release wait (`operators_ddl.go`)

`waitForPgClassRowMarks` previously waited on the holder's **whole transaction**
(`WaitForXID`), so a peer unblocked only at the holder's commit/rollback. PG
unblocks at the **tuple-lock release**. New `waitPgClassRowMarkReleased` polls
the mark's presence (5 ms) and returns the instant it is gone — whether retracted
early (step 3) or cleared at transaction end — while keeping the wait-for-graph
deadlock check (perm 8's GRANT↔ADD-PK cycle still surfaces 40P01). This is what
makes `revoke4` complete **before** `r3`.

### 5. `SearchSysCacheLocked1` find-then-none (`operators_ddl.go`)

After the REVOKE's `waitForPgClassRowMarks` returns, `execCompatNoop` re-checks
`LookupTableByOID(tbl.OID)`; if the relation is gone it returns `XX000 cache
lookup failed for relation <oid>` — the exact elog PG raises.

### 6. plpgsql EXCEPTION handling — three correctness fixes

These were **latent bugs**: prior specs never exercised a *caught* exception
from a preceding statement.

- **TryBody wrapping** (`internal/plpgsql/parser.go`): `parseTopBlock` /
  `parseNestedBlock` appended the `ExceptionBlock` as a *sibling* of the
  protected statements and never set `ExceptionBlock.TryBody`, so the runtime ran
  an empty try and an error in the body aborted the list before any handler. Fix
  sets `excBlock.TryBody = stmts` and wraps. **EXCEPTION handlers were
  effectively dead before this.**
- **SQLERRM/SQLSTATE binding** (`plpgsql_runtime.go`): the `ExceptionBlock`
  runtime now binds `sqlerrm` (the bare message) and `sqlstate` as frame text
  variables before running the matched handler (`setPlpgsqlFrameVar`), so
  `regexp_replace(sqlerrm, '[0-9]+', 'REDACTED')` resolves.
- **RAISE WARNING severity** (`plpgsql_runtime.go`): `RAISE WARNING` now routes
  to `ctx.AddWarning` (WARNING severity) instead of `AddNotice` (NOTICE), so the
  isolation runner echoes `WARNING:` not `NOTICE:`.

## Blast radius

- The `pg_class` delete intercept fires only for `DELETE FROM pg_class` in an
  explicit transaction; ordinary DELETEs are byte-unchanged.
- The pending-drop wait + rowmark retract are reached only for a `pg_class`
  rowmark with an `oid = <const>` filter; user-table `FOR UPDATE` untouched.
- `waitPgClassRowMarkReleased` replaces `waitPgClassInplaceXID` **only** inside
  `waitForPgClassRowMarks`; the ACL-change / in-place / pending-drop single-XID
  waits still use `waitPgClassInplaceXID`. For a mark held to transaction end the
  poll returns at the same point as before (the mark clears at commit/rollback).
- The plpgsql fixes are pure corrections — dead handlers now run; happy-path
  bodies (no caught error) execute the same statements as before.

## Gates

- `TestPort_IsolationIntraGrantInplace` strict — 10/10 permutations byte-identical.
- Non-regression strict: `IntraGrantInplaceDb`, `RiTrigger`,
  `EvalPlanQualTrigger`, `DeadlockHard`, `TuplelockUpgradeNoDeadlock` PASS.
- `go test ./internal/plpgsql/ ./internal/executor/ ./internal/catalog/` PASS.
- `go build ./...` clean; pgbench smoke (pre-commit hook).

## Oracle

Mirrors PostgreSQL's `SearchSysCacheLocked1` (`src/backend/utils/cache/catcache.c`)
which locks a catalog tuple, awaits its xmax, then re-reads — finding it gone
after a concurrent committed delete and raising `cache lookup failed for relation`
(`src/backend/utils/cache/relcache.c` / elog). GRANT/REVOKE and the in-place
`relhasindex` update take no heavyweight lock on the object; their lock IS the
`pg_class` tuple xmax (`src/backend/catalog/aclchk.c`,
`heap_inplace_update_scan`).

## Update 2026-09-19 (M-NIGHTLY AI-…-003/-006/-010/-013): perm-10 regression repair — `pgClassRelDropped`

The doc's step 4 claim ("`sfu3` wakes, the tuple is gone → 0 rows") had a hole:
`drainAndStamp`'s post-wait scan **re-evaluated** `'intra_grant_inplace'::regclass`
lazily, and `regclassin`'s `LookupTable` now misses — the query died with
`42P01 relation "intra_grant_inplace" does not exist` instead of yielding 0
rows. Because `sfu3` then never produced an empty result, the rowmark was only
cleared by `r3`'s transaction abort, so `revoke4` completed *after* `r3` instead
of before it — the exact tail the nightly runs flagged since 2026-09-05
(deterministic, perm 10 only; perms 1–9 stayed byte-identical).

PG resolves the `regclass` constant **once** — into the scan key at executor
start, before the `LockTuple` wait (`regproc.c:882` `regclassin`,
`provolatile='s'`). Post-commit its scan finds the pg_class tuple deleted →
`0 rows`; the constant is never re-resolved. goopg's fix mirrors that binding
discipline at the LockRows layer rather than inside `regclassin` (a blanket
"miss → false" would wrongly silence genuine `42P01`s):

- `maybeRecordPgClassRowMark` (`operators_lockrows.go`): after
  `waitTablePendingDrop` releases, probe `im.LookupTableByOIDAllDBs(relOID)`.
  A miss means the deferred drop committed — set `o.pgClassRelDropped`. A hit
  means it aborted — drain normally and the row is found. The check runs
  unconditionally so it also covers a drop committed between filter-eval and
  the wait, and the `tablePendingDropXID` marker is deliberately NOT the signal
  (it is cleared only on the commit path, never on abort — a stale marker after
  rollback would read identically to a live one).
- `drainAndStamp`: `for !o.pgClassRelDropped` skips the child drain entirely —
  `pending` stays empty so the existing empty-pending arm retracts the rowmark
  (`ClearPgClassRowMark`), releasing `revoke4` before `r3` exactly as PG does.
- The flag is sticky by design: a committed drop cannot un-happen (OIDs are
  never recycled), matching PG's bound scan key surviving `ExecReScan`.

Verified: `TestPort_IsolationIntraGrantInplace` PASS (10/10 perms
byte-identical); siblings `IntraGrantInplaceDb`, `LockCommittedUpdate`,
`LockCommittedKeyupdate`, `DropIndexConcurrently1`, `SequenceDdl`,
`TruncateConflict` all PASS; `go test ./internal/executor/` PASS;
`go test -race ./internal/executor/` clean.
