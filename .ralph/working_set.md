(idle — nothing in flight)

Last loop: **M0125-0054 CLOSED and committed** — data-modifying CTEs are no
longer a prefix. The main plan runs first; a CTE the body reads is driven on
demand by the scan that reads it; the rest are swept after the body.

1. PG has TWO routes, not one. The filing named only `ExecPostprocessPlan`
   (route 2). Route 1 — the `CteScan` pulling from the `ModifyTable` — is not
   optional: a CTE the body READS cannot wait for the post-body phase.
2. **The sweep is REVERSE declaration order.** `ExecInitModifyTable` files with
   `lcons`, not `lappend`. Confirmed live: three unreferenced INSERT CTEs
   a,b,c land at ctid (0,1)=c, (0,2)=b, (0,3)=a.
3. **The filed plan-time referenced-ness flag was the wrong shape** and was not
   built. A body-plan walk that misses a subtree undercounts references and
   silently returns no rows; demand-driving (`materializedCTEScanOp.Open` →
   `ctx.pendingDMLCTEs.ensureCTE`) cannot misjudge in that direction. Zero
   planner files touched.
4. The reorder's obligation: the fence/reveal gate on the fence's EXISTENCE,
   not `ctx.InDMLCTE`, so the outer statement's own writes are registered and a
   deferred CTE cannot see them. `InDMLCTE` survives only for the
   `CTENewToOld`/`CTESelfModifiedErrors` bookkeeping.

Files: `internal/executor/{operators_cte_dml,context}.go`,
`internal/executor/cte_dml_preimage_reveal_test.go` (divergence test inverted +
4 new tests, every value from live PG 18.3 on 65432),
`docs/design/0125-0054-dml-cte-execution-order.md` + README index, fix_plan
(-0054 ticked), 3 ledger rows.

Gates run: executor/server/planner/analyzer PASS; units gate PASS;
`TestPort_RegressSuite` PASS; full `TestPort_Isolation*` PASS except the
pre-existing `TestPort_IsolationEvalPlanQual` (nightly
`AI-20260806-011323-001`) — proved byte-identical at HEAD via a stash/re-run,
so untouched by this change; `scripts/tpch-spotcheck.sh` Q12=2/Q13=35 canonical
PASS; `scripts/tpcds-sf05-regression.sh plans` → `queries=99 same=99 changed=0`
(capture `plans-20260806-172836.txt`, baseline NOT re-pinned); pgbench smoke via
the commit hook.

NEXT LOOP (banner: M0124 closed → M0125 → M0127 → M-NIGHTLY → M0123).
`ci/logs/action-items.md` was still run `20260806-011323` this loop (all 18
filed, nothing new) — re-check first; a NEW nightly at `status: pass` makes
M0127-P6.1 selectable. Otherwise the live M0125 choice is **`M0125-0048`** (the
faithful `AGG_MIXED` grouping-sets aggregate — fidelity, large, retires the
`__gs_src_N` hoist). -0031/-0032/-0033/-0041 stay `[→ M0127: absorbed]`.

In-flight: none. The PG reference cluster on 65432 was started twice this loop
for oracle captures and stopped again (`bench/tpch/setup_pg.sh` to bring it
back; credentials come from `source bench/tpch/env.sh` — psql needs PGPASSWORD,
a bare `-U postgres` is rejected).
