(idle — nothing in flight)

Loop #32 COMPLETE + COMMITTED: M0118-0007 `eval-plan-qual-trigger.spec` PROMOTED
soft→strict (design 0118-0095). Probe found it already matches PG 18.3 byte-for-byte
across all 38 active permutations with NO engine change — the harder half of the EPQ
output-parity pair (BEFORE/AFTER row triggers + READ COMMITTED EvalPlanQual rechecks +
key-update CTID-chain following + ON CONFLICT DO UPDATE upserts + REPEATABLE READ 40001,
all via RETURNING + noisy_oper() NOTICE WHERE quals). Switched
`TestPort_IsolationEvalPlanQualTrigger` to `runIsoSpecStrict`; CSV D-002 rationale +
md regenerated (inventory already had `pass`). Strict PASS, stable.

M0118-0007 STILL OPEN: sibling `eval-plan-qual` defers (cross-table EPQ recheck returns
`(0 rows)` where PG re-projects updated row + EXPLAIN/column-format diffs ~L1171).

Probe results this loop (throwaway zz_probe_test.go, since removed):
- eval-plan-qual-trigger = PASS (promoted this loop)
- eval-plan-qual = defer (EXPLAIN/col-format diffs ~L1172)
- ri-trigger = defer (separator-line/format diffs)
- horizons = defer (jsonb ops + IOS prune horizon)
- fk-partitioned-1/2 = defer (ALTER TABLE ATTACH PARTITION + partitioned-FK)

Next loop options (all genuinely large remaining M0118 subsystems):
- M0118-0007: eval-plan-qual (EPQ-over-join re-projection + EXPLAIN format).
- M0118-0005: ri-trigger, fk-partitioned-1/2.
- M0118-0009 misc (each a full subsystem): horizons (jsonb ->/->> + IOS prune horizon),
  intra-grant-inplace{,-db} (pg_class MVCC tuple — virtual today), stats (pg_stat_*
  cumulative infra), prepared-transactions{,-cic} (2PC + SSI).
- Probe new candidates with throwaway zz_probe_test.go before committing.
