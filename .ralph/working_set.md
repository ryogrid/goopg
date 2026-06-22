Task: M0118-0009 `timeouts` (table-level half) — DONE this loop. `timeouts.spec`
PROMOTED to `pass` (all 8 permutations). Design 0118-0018. COMMITTING.

WHAT LANDED:
- NEW `Context.acquireScanReadLockTxn(rel)` (executor/context.go): takes a
  txn-scoped ACCESS SHARE on `tableLockMgr` via the existing `acquireRelLockTxn`
  (keyed by TxnLockBackendID, released at txn end). No-op when
  TxnLockBackendID==0 (autocommit) or rel is a system catalog (RelOid<16384,
  new const firstNormalObjectOID). Reuses LOCK TABLE machinery.
- Wired at the 3 relation-read scan opens (alongside the existing no-op
  acquireRelLock(AccessShareLock)): operators_storage.go seqscan (~L708),
  operators_index.go (~L213), operators_indexonly.go (~L53).
- Effect: a plain SELECT in an explicit txn now holds ACCESS SHARE, so a later
  bare LOCK TABLE (ACCESS EXCLUSIVE) conflicts/parks/times out — the lock_timeout
  /statement_timeout cancel machinery from 0118-0017 already covers the wait.

KEY INSIGHT: tableLockMgr is a SINGLE-mutex manager, so the perf risk is real;
mitigated by the autocommit + catalog gates + lockmgr idempotency (re-scan in a
txn is a mask-check no-op) + ACCESS SHARE conflicting only with ACCESS EXCLUSIVE
(grants instantly absent a concurrent LOCK TABLE/DDL). pgbench -N 230 tps / -S
14.7k tps, 0 failed — no regression (-S is autocommit so hits the no-op path).

Gates (all green): TestPort_TimeoutsTableLevel 4/4; TestPort_TimeoutsRowLevel
4/4; full `TestPort_Isolation` batch 388s exit 0 (no regression); -race
executor+mvcc+lockmgr; executor unit; pgbench smoke 0-failed; gofmt clean for my
edits (operators_storage.go pre-existing go1.25/1.26 diffs at L2820/3087 are NOT
mine — never gofmt -w); ralph-state-guard OK. CSV promoted + coverage/inventory
md regenerated.

NEXT loop: pick another M0118-0009 misc spec — all remaining need substantial new
subsystems: async-notify (LISTEN/NOTIFY), stats (pg_stat_* + plpgsql), horizons/
freeze-the-dead/inplace-inval/intra-grant-inplace{,-db} (vacuum/freeze/inplace +
datfrozenxid, overlaps M0117-0008), subxid-overflow (plpgsql), prepared-
transactions{,-cic} (2PC), temp-schema-cleanup (temp schema + plpgsql + advisory).
OR a different M0118 group (0118-0005 FK, 0118-0006 MERGE, 0118-0008 DDL/VACUUM).

GOTCHAS: never gofmt -w (go1.25 repo vs local 1.26). Isolation specs run goopg as
a SUBPROCESS. CSV rationale kept comma-free. cd /home/ryo/work/goopg/goopg first.
tpch-spotcheck INFRA-BLOCKED; pgbench smoke is the live guard. Untracked postgres/
+ weekly_loc.* + requirements.txt are stray artifacts — leave them.
