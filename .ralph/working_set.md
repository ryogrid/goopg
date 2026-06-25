(idle — nothing in flight)

Last loop (#57) COMPLETE + committed: M0118-0009 intra-grant-inplace ENABLER
(design 0118-0116, NOT a promotion). Made permutation 9 byte-identical; first
divergence advanced L206 → L235 (now perm 10). Spec stays `defer`.

What landed (perm 9 = `b1 grant1 b3 sfu3 revoke4 c1 r3`):
- plpgsql parser: `parseStmt` now routes a leading `grant`/`revoke` identifier to
  `parseSQLStmt` (they're not reserved keywords, so a bare REVOKE in revoke4's
  `DO $$ … $$` block previously hit parseAssign → "expected ':=' or '=' after
  revoke" BEFORE the EXCEPTION handler). General fix: GRANT/REVOKE now parse in
  any plpgsql function/DO body. Unit: TestParseGrantRevokeEmbeddedSQL.
- executor: refactored `ddlOp.waitForTableACLChange` into free func
  `waitTableACLChange(ctx, tableOID)`; `lockRowsOp.maybeRecordPgClassRowMark`
  now records the rowmark FIRST (so peer REVOKE blocks behind it via
  waitForPgClassRowMarks) THEN awaits the table's ACL xmax (PG: acquire LockTuple,
  then await tuple xmax). Returns *ExecError, propagated by drainAndStamp.
  Gated on pg_class + oid=<const> → nil blast radius for user-table FOR UPDATE.

Remaining intra-grant-inplace gap (perm 10, distinct unbuilt subsystem):
- perm10 = `b1 drop1 b3 sfu3 revoke4 c1 r3`: drop1 = `DELETE FROM pg_class WHERE
  relname='intra_grant_inplace'` — virtual-catalog tuple delete (deferred drop at
  commit) recording a delete xmax sfu3 must wait on; sfu3 then returns (0 rows)
  and revoke4 finds the relation gone (`WARNING: got: cache lookup failed for
  relation REDACTED`). Needs virtual pg_class row delete + SearchSysCacheLocked1
  find-then-none. Likely a `SetTablePgClassDeleteXID`-style store mirroring the
  ACL-xmax store + waitTableACLChange, plus relation-gone detection in the REVOKE
  path. Resume here for perm 10.

Other remaining M0118-0009 spec: `stats` (pg_stat_* cumulative subsystem,
Effort-L). Other failing M0118 specs (distinct unbuilt subsystems):
index-only-bitmapscan (EXPLAIN DECLARE CURSOR + BitmapOr), predicate-gin/gist
(GIN/GiST AMs), deadlock-parallel (lock-group), fk-partitioned-1/2 (ATTACH
PARTITION + partitioned-FK).
