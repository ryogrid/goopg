Task: M-NIGHTLY triage of nightly run 20260719-094219 (5 AI items). COMPLETE — all stale.

Findings: nightly ran at sha c217c692 (predates HEAD 12969b77 pgnodes S1/S2 +
mdtablefix + f20cda39 demote). Re-ran all 5 repros at HEAD, ALL PASS:
- AI-...-001 TestPort_IsolationPreparedTransactions → PASS (57.9s); stale, fixed by f20cda39 demote.
- AI-...-002/003/004/005 regress errors/index_including/portals_p2/select → all PASS (18.8s suite).
Recorded as [x] stale entries under M-NIGHTLY in fix_plan.md. No code changed → no
design doc / no deferral row needed.

Gates run: 3 testport runs (all green above); make ralph-state-guard; pre-commit pgbench smoke on the fix_plan-only commit.

Next step (next loop — no in-flight nightly items remain): resume M0123-S2 sub-slice 2
(the risky/E2E half) per the prior working_set — (a) FuncExpr resolve/rebuild via
pgProcRetTypeByOID leaf map from cmd/gen-pg-proc-data; (b) wire writeAttrdefRow
(operators_ddl.go:13272) + sys_pg_statistic_ext.go stxexprs → pgnodes.Out when
SupportsExpr; (c) swap loadColumnDefaultsFromHeap/loadStatisticsExtFromHeap
(initdb/catalog_heap_reload.go) to pgnodes.Read→Rebuild on '{' discriminator;
(d) adversarial PG18 standby-eval E2E for DEFAULT 40+2/upper('x')/-1.

In-flight: none.
