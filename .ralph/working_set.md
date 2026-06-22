Task: M0118-0009 `delete-abort-savept` — LANDED + committed this loop.

Last loop (2026-06-22, design 0118-0013): `delete-abort-savept` PROMOTED to pass
(all 7 perms byte-identical vs PG 18.3). Subxact-scoped DELETE/UPDATE xmax:
- NEW `effectiveWriterXID(ctx)` (operators_storage.go) = session.EffectiveWriterXID()
  (current sub-XID in a savepoint) else ctx.Tx.XID. Strict no-op outside savepoints.
  Wired at all 24 old-tuple-xmax stamp + paired-WAL sites (storage/merge/upsert).
- producer `stampUpdaterXmaxPreservingLockers` now KEEPS an outer-level self locker
  (TopLevelXid==ourTop, ≠ourXID) → {outer-keyshare + subxid-updater}; ROLLBACK TO
  drops the subxid member, KEY SHARE survives.
- `lockRowsOp.stampLockInner` abort test → `HasAbortedXID(xmax) || IsAborted(xmax)`
  (subxact rollback lives in pg_subtrans, not the top-level aborted set).
Gates green: -race mvcc/multixact/executor + storage/wal; full row-lock+deadlock+
merge+insert-conflict isolation batch PASS; pgbench smoke 0-failed.

Also verified + ticked: M0118-0003 row-locking COMPLETE (all 20 specs pass).

NEXT loop picks a fresh fix_plan item. Closest unblocked candidates (M0118-0009
savepoint/multixact siblings, ledger 2026-06-22):
- `delete-abort-savept-2`: FOR NO KEY UPDATE pure-lock upgrade under savepoint →
  rides lockRowsOp NOT the producer; needs the row-lock path to preserve+restore an
  outer-level self lock-only member across ROLLBACK TO (mirror of the producer fix).
- `aborted-keyrevoke`: rolled-back UPDATE's NEW-tuple xmin must ALSO be sub-XID-scoped
  (NewHeapTuple/insert still uses ctx.Tx.XID); this loop only did old-tuple xmax.
- `multixact-no-forget`: whole-txn ROLLBACK of an updater member; surviving locker
  must be retained while the aborted updater is forgotten on the read path.
Otherwise advance M0118-0005 (FK concurrency) or another open milestone.

GOTCHAS: CSV rationale MUST be comma-free [[serena_replace_content_dotall_eats_file]];
never gofmt -w (go1.25 vs local 1.26 reformats unrelated lines — 3 storage files
already gofmt-dirty pre-existing) [[goopg_gofmt_version_mismatch_no_w]]; isolation
specs run goopg as a SUBPROCESS so debug must go to a file not stderr; CWD persists
between Bash calls — always cd /home/ryo/work/goopg/goopg first
[[worktree_cwd_path_consistency_hazard]]. tpch-spotcheck INFRA-BLOCKED (SLRU
backfill >60s); pgbench smoke is the live guard.
