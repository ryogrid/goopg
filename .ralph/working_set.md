(idle — nothing in flight)

Last loop: **M0125-0052 CLOSED and committed** — a data-modifying CTE's writes
are now invisible to the WHOLE statement, not just to an outer SELECT.

1. The filing's diagnosis ("only the outer SELECT consults the fence") was
   half right. `scanMatching` — the scan behind an outer UPDATE/DELETE —
   ALREADY consulted `ctx.CTEWriteFence`. The fence was **starved**: only
   `upsertOp` and the three UPDATE paths ever registered a write, so for the
   archetypal `WITH x AS (INSERT …)` it was EMPTY. That also broke the
   outer-SELECT half everyone believed worked (`SELECT count(*)` → 2, PG 1).
2. Registering INSERTs alone would have traded one wrong answer for another:
   the key was a bare `ItemPointer` and `{block 0, offset 1}` is the first row
   of EVERY table. Key is now `CTEFencePtr{Rel, Ptr}`; the EvalPlanQual xmin
   re-read that existed only to work around that collision is deleted.
3. Fourth site class found while verifying: the fence must not be PLAN-SHAPE
   dependent. With a PK on the target the outer SELECT went through an Index
   Only Scan and returned the CTE's row — so `indexScanOp`, `indexOnlyScanOp`
   and the index-driven UPDATE fast path consult it now too.
4. Five inline registration copies collapsed into `cteFenceInsert` /
   `cteFenceUpdate` (separate src/dest rel for cross-partition moves) /
   `cteFenced`, in `operators_cte_dml.go`.

Files: `internal/executor/{context,operators_cte_dml,operators_storage,
operators_index,operators_indexonly,operators_merge,operators_upsert}.go`, new
`internal/executor/cte_dml_outer_dml_fence_test.go` (7 shapes, each pinned to a
value captured from live PG 18.3 on port 65432),
`docs/design/0125-0052-dml-cte-write-fence-covers-whole-statement.md` + README
index, fix_plan (-0052 ticked, -0053 filed), 3 ledger rows.

Gates run: executor/planner/analyzer/parser/server packages PASS;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` Q12=2/Q13=35 canonical PASS;
`scripts/tpcds-sf05-regression.sh plans` → `queries=99 same=99 changed=0`
(capture `plans-20260806-160727.txt`, baseline NOT re-pinned — nothing moved);
pgbench smoke via the commit hook.

NEXT LOOP (banner: M0124 closed → M0125 → M0127 → M-NIGHTLY → M0123).
`ci/logs/action-items.md` is still run `20260806-011323` (all 18 filed, nothing
new) — re-check first; a NEW nightly at `status: pass` makes M0127-P6.1
selectable. Otherwise the live M0125 choices are **`M0125-0053`** (this loop's
successor: the fence hides rows a CTE ADDED but cannot show rows it REMOVED —
two witnesses recorded, but note the blast radius is the tuple VISIBILITY
predicate, not a skip list) or **`M0125-0048`** (the faithful `AGG_MIXED`
grouping-sets aggregate — fidelity, large, retires the `__gs_src_N` hoist).
-0031/-0032/-0033/-0041 stay `[→ M0127: absorbed]`.

In-flight: none. The PG reference cluster on 65432 was started this loop for
the oracle capture and stopped again (`bench/tpch/setup_pg.sh` to bring it back
— it starts in seconds from the existing pgdata).
