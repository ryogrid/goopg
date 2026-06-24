(idle — nothing in flight)

Loop #31 COMPLETE + COMMITTED: M0118-0005 `fk-deadlock.spec` PROMOTED defer→pass
(design 0118-0094), all 14 permutations byte-identical vs PG 18.3, strict
`TestPort_IsolationFkDeadlock`.

Root cause: a child INSERT's FK check is `SELECT … FOR KEY SHARE` on the parent
row, which conflicts ONLY with a key-changing modification (key UPDATE / DELETE)
and is COMPATIBLE with a concurrent no-key UPDATE. `scanRelForFKMatch`
(internal/executor/operators_fk.go) treated ANY in-flight non-self non-lock-only
xmax as a wait, so a child INSERT blocked behind a peer's in-flight no-key parent
UPDATE where PG proceeds (sibling-paths gap vs lockRowsOp, which already keys its
wait on keysUpdated). Fix: new helpers `fkXmaxIsKeyChanging` (single-xid:
HEAP_KEYS_UPDATED OR structural-delete via self-pointing/invalid t_ctid) +
`multixactUpdaterIsKeyChanging` (updater member StatusUpdate vs StatusNoKeyUpdate);
no-key updater = clean match (no wait), key UPDATE/DELETE still waits+rescans.

Next loop options (all genuinely large remaining M0118 subsystems — none is a cheap
front-end promotion, the easy ones are harvested):
- M0118-0005 remaining: ri-trigger (user RI constraint-trigger firing),
  fk-partitioned-1/2 (ALTER TABLE ATTACH PARTITION + partitioned-FK).
- M0118-0007: eval-plan-qual (EPQ-over-join recheck re-projection).
- M0118-0009 misc (each a full subsystem): horizons (jsonb `->`/`->>` operators —
  NO opcode/eval today, plus EXPLAIN json Heap Fetches + IOS prune horizon),
  intra-grant-inplace{,-db} (real pg_class MVCC tuple + tuple-lock + catalog
  deadlock — pg_class is virtual), stats (pg_stat_* cumulative-stats infra +
  pg_stat_force_next_flush), prepared-transactions{,-cic} (2PC + SSI integration).
- Probe new candidates with a throwaway zz_probe_test.go (TestProbe → RunAndCompare,
  log .Diff) before committing to one.
