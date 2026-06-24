(idle — nothing in flight)

Loop #25: **M0118-0009 partial reconciliation** (no engine change). Resolved the
documented-PASS-but-CSV-lagging drift flagged by loop #24's working_set: the
savepoint/abort row-lock-restore family — `delete-abort-savept`,
`delete-abort-savept-2`, `aborted-keyrevoke` — were all green (designs
0118-0013/0014/0015) but two CSV rows were still `failed` and all three tests used
soft `runIsoSpec`. Promoted the three `TestPort_Isolation{DeleteAbortSavept,
DeleteAbortSavept2,AbortedKeyrevoke}` to `runIsoSpecStrict`, flipped the two
lagging rows `failed`→`pass` in `postgres-oracle-target-inventory.csv` (rationale =
test func name), regenerated `upstream-isolation-coverage.md` +
`postgres-oracle-target-inventory.md`. Isolation tally 101→103 pass / 20→18 failed.
Verified all three strict PASS (10.2 s). fix_plan NOT edited (driver-churn rule).

Remaining M0118-0009 specs (each a distinct unbuilt subsystem): async-notify
(LISTEN/NOTIFY), horizons (dollar-quote lexer + EXPLAIN JSON), intra-grant-inplace
{,-db} (catalog-row lock on GRANT tuple xmax), stats (pg_stat_* infra),
temp-schema-cleanup (pg_my_temp_schema + temp cleanup), prepared-transactions{,-cic}
(2PC). Other open milestones: M0118-0002 (predicate-locks: GIN/GiST/hash AMs),
M0118-0004 (deadlock-parallel: lock-group abstraction), M0118-0005 (fk-deadlock,
ri-trigger, fk-partitioned-1/2), M0118-0007 (eval-plan-qual: EPQ-over-join).
