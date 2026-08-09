(idle — nothing in flight)

Last loop: M-NIGHTLY AI-20260810-011258-003 blocker #3 (`pg_attrdef`
catalog completeness) FIXED and committed. The fix_plan item STAYS OPEN —
a NEW blocker #4 now gates Phase D.

Landed (3 causes, one commit):
1. Indexes 2656 (adrelid,adnum) / 2657 (oid) were never materialized —
   added to `nailedLocalRels` (initdb/relcache_init.go),
   `pgIndexInitialEntries` (initdb/initdb.go) and all three critical-index
   placeholder lists (base/1, base/5, global).
2. `pgAttrdefAttrs()` had 3 attrs while the heap writer wrote 4 — added
   `adbin` (pg_node_tree/194), relnatts 3→4. pg_attrdef is NOT formrdesc'd,
   so PG rebuilds its TupleDesc from the streamed pg_attribute rows for 2604.
3. `mirrorTouchedCatalogsToPostgresDB` did not mirror 2604/2656/2657 into
   base/5 — a `dbname=postgres` standby read an empty heap and downgraded to
   `WARNING: 1 pg_attrdef record(s) missing`. THIS was the last 20% and is
   the non-obvious one: goopg writes user catalog rows to base/1 and mirrors
   a FIXED LIST of rels to base/5.
Runtime entries: `writeAttrdefRow` → insertPgAttrdefAdrelidAdnumIndexEntry /
insertPgAttrdefOidIndexEntry (key shapes identical to 2659 / 2662).

Result: TestE2E_PGStandbyFullCycle Phases A, B and C now PASS. Phase C
(failover + promote) had never executed in any prior loop.

NEXT (blocker #4, fix_plan item 4): Phase D fails because the PROMOTED PG
rejects `SELECT pg_create_physical_replication_slot('s10_reverse')` —
`function ...(unknown) does not exist`. PG 18's signature is
(slot_name name, immediately_reserve bool DEFAULT false, temporary bool
DEFAULT false); goopg's pg_proc seed for OID 3779/3780 has no
pronargdefaults/proargdefaults, so PG cannot resolve the 1-arg form.
May need an internal/pgnodes List IR node first (same gap as stxexprs).

Gates run: internal/initdb PASS (56 s), internal/executor PASS,
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS,
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35), commit-hook pgbench smoke,
`make ralph-state-guard` OK (auto-repaired the stale completed marker).

Ledger: 1 row, 3 deferrals (pg_proc defaults; stale 2656/2657 leaf entries on
ALTER re-sync; pg_inherits 2611/2680 has the SAME base/5 mirror gap).

In-flight: none.
