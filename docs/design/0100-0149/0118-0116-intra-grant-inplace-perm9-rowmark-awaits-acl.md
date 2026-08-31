# 0118-0116 — intra-grant-inplace enabler: perm 9 (plpgsql REVOKE + rowmark awaits ACL xmax)

Status: accepted
Milestone: M0118-0009 (upstream isolation spec suite pass-through)
Spec: `postgres/src/test/isolation/specs/intra-grant-inplace.spec`
Predecessors: 0118-0109 (GRANT-`xmax` half), 0118-0113 (`pg_class` rowmark half),
0118-0114 (reverse-direction wait + deadlock detection), 0118-0115 (perm-8 victim
XID release)

## Summary (Enabler, NOT a promotion)

0118-0115 made permutations 1–8 byte-identical; the first divergence sat at L206,
the start of **permutation 9**:

```
b1 grant1 b3 sfu3 revoke4 c1 r3
```

This loop makes permutation 9 byte-identical, advancing the first divergence
**L206 → L235** (now permutation 10). The spec stays `defer` because permutation
10 (`b1 drop1 b3 sfu3 revoke4 c1 r3`, where `drop1` is `DELETE FROM pg_class`)
still needs virtual-catalog tuple-delete semantics — a distinct unbuilt subsystem.

## Two gaps closed for permutation 9

Permutation 9 timeline (expected):

| step | session | behaviour |
|------|---------|-----------|
| `b1` | s1 | `BEGIN` |
| `grant1` | s1 | `GRANT SELECT ON intra_grant_inplace TO PUBLIC` — records the table's ACL-change `xmax` |
| `b3` | s3 | `BEGIN ISOLATION LEVEL READ COMMITTED` |
| `sfu3` | s3 | `SELECT relhasindex FROM pg_class WHERE oid='…'::regclass FOR UPDATE` — **`<waiting ...>`** behind grant1's tuple `xmax` |
| `revoke4` | s4 | `DO $$ … REVOKE SELECT … FROM PUBLIC … $$` — **`<waiting ...>`** behind sfu3's `pg_class` rowmark (`LockTuple`) |
| `c1` | s1 | `COMMIT` — unblocks sfu3, which returns `relhasindex = f` (1 row) |
| `r3` | s3 | `ROLLBACK` — unblocks revoke4 (the REVOKE succeeds; no warning) |

Two things were missing:

### Gap 1 — plpgsql parser rejected a bare `REVOKE` in a `DO` body

`revoke4` wraps `REVOKE SELECT ON intra_grant_inplace FROM PUBLIC;` in a `DO $$
… $$` block. The plpgsql body parser's statement dispatch routed embedded SQL
(`INSERT`/`UPDATE`/`DELETE`/`SELECT`/`CREATE`/`DROP`/`ALTER`) to `parseSQLStmt`,
but `GRANT`/`REVOKE` are not reserved keywords — the main SQL lexer keeps them as
plain identifiers — so a leading `REVOKE` fell through to `parseAssign` and failed
with `expected ':=' or '=' after "revoke"`. The parse error happened *before*
execution, so the `EXCEPTION WHEN others` handler never caught it and goopg
emitted an `ERROR` (PG never errors here).

Fix (`internal/plpgsql/parser.go`, `parseStmt`): add a case for a leading
`grant`/`revoke` identifier that routes to `parseSQLStmt` like the other embedded
SQL statements (the main parser resolves them to a `CompatNoopStmt.TableACL`).
This is a general correctness fix — `GRANT`/`REVOKE` work in any plpgsql function
or `DO` body now, not just this spec. Unit: `TestParseGrantRevokeEmbeddedSQL`.

### Gap 2 — `sfu3`'s `pg_class` rowmark did not wait on the in-flight ACL change

`sfu3` (`SELECT … FROM pg_class … FOR UPDATE`) records a `pg_class` rowmark via
`lockRowsOp.maybeRecordPgClassRowMark` (0118-0113), but it completed immediately
instead of blocking behind grant1's uncommitted ACL change. In PG the FOR UPDATE
acquires the tuple lock and then waits on the tuple `xmax` held by the in-flight
GRANT.

Fix:
1. Refactor `waitForTableACLChange` (a `*ddlOp` method, 0118-0109) so its core is
   the free function `waitTableACLChange(ctx *Context, tableOID uint32) *ExecError`
   — it blocks (deadlock-aware via `waitPgClassInplaceXID`) until the
   `TableACLChangeXID(tableOID)` writer commits/aborts, skipping a same-xact /
   savepoint holder.
2. `maybeRecordPgClassRowMark` records the rowmark **first** (so a concurrent
   `GRANT`/`REVOKE` blocks behind *us* via `waitForPgClassRowMarks` — 0118-0114),
   **then** calls `waitTableACLChange(ctx, relOID)`. Ordering mirrors PG: acquire
   `LockTuple`, then await the tuple `xmax`. The function now returns `*ExecError`
   (deadlock / lock-timeout) which `drainAndStamp` propagates.

With both gaps closed: sfu3 waits behind grant1 (`<waiting ...>`); revoke4 records
its own ACL xmax and waits behind sfu3's rowmark (`<waiting ...>`); `c1` unblocks
sfu3 (returns `f`); `r3` unblocks revoke4 (succeeds, no warning) — byte-identical
to PG 18.3.

## Blast radius

- Gap 1: a new dispatch case for two identifiers at plpgsql statement start;
  every other body construct is byte-unchanged.
- Gap 2: `waitTableACLChange` is reached from the rowmark path **only** when the
  locked relation is `pg_class` (`OID == catalog.RelationRelationId`) with an
  `oid = <const>` filter — every ordinary `SELECT … FOR UPDATE` on a user table
  is byte-unchanged. With no concurrent ACL change, `TableACLChangeXID` returns
  `InvalidTransactionID` and the wait is an immediate no-op.

## Residual (spec stays `defer`)

Permutation 10 (`b1 drop1 b3 sfu3 revoke4 c1 r3`): `drop1` is `DELETE FROM
pg_class WHERE relname = 'intra_grant_inplace'` — a virtual-catalog tuple delete
(deferred drop at commit) that records a delete `xmax` sfu3 must wait on, after
which sfu3 returns `(0 rows)` and revoke4 finds the relation gone (`WARNING: got:
cache lookup failed for relation REDACTED`). That is a distinct unbuilt subsystem
(virtual `pg_class` row delete + `SearchSysCacheLocked1` find-then-none).

## Gates

- `TestParseGrantRevokeEmbeddedSQL` (new plpgsql parser unit) PASS.
- Spec probe: first divergence advanced L206 → L235 (perms 1–9 byte-identical).
- Non-regression strict specs `TestPort_IsolationIntraGrantInplaceDb`,
  `TruncateConflict`, `RiTrigger`, `DeadlockHard`,
  `TuplelockUpgradeNoDeadlock`, `LockCommittedKeyupdate` PASS.
- `go test ./internal/plpgsql/` + `./internal/executor/` PASS;
  `go test -race ./internal/catalog/` green; `go build` clean.
- pgbench smoke = pre-commit hook.
