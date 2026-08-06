(idle — nothing in flight)

Last loop: **M0125-0053 CLOSED and committed** — the rest of a statement now
sees the PRE-IMAGE of a row its own data-modifying CTE removed.

1. `ctx.CTEXmaxReveal` is the write fence's mirror image on the same
   `CTEFencePtr{Rel, Ptr}` key, filled by `cteFenceDelete` at both `deleteOp`
   paths, MERGE's matched-DELETE, and `cteFenceUpdate`'s old key. Both filed
   witnesses now match live PG 18.3.
2. **Only READ scans consult it** — PG's own structure, not a shortcut:
   `ExecDelete`/`ExecUpdate` return NULL for a tuple whose cmax equals
   `es_output_cid`, so a DML target scan that never finds the row gives the
   same row count and heap state. Every write path passes a nil reveal.
3. The reveal lives INSIDE the HOT-chain walk. The pre-image sits ahead of the
   CTE's new version, so testing only the walk's result returns the new
   version, which the fence then drops — losing the row entirely.
4. **The correction the isolation suite forced:** membership is not a licence
   to show the tuple. Forcing visibility broke
   `TestPort_IsolationInsertConflictDoUpdate3` with a duplicate row — `INSERT …
   ON CONFLICT DO UPDATE`'s documented MVCC violation lets it stamp a version
   not visible to the command's snapshot. `cteRevealHeader` clears only `Xmax`
   and re-runs the ordinary test, so the xmin snapshot check still applies.

Files: `internal/executor/{context,operators_cte_dml,operators_storage,
operators_index,operators_indexonly,operators_merge,operators_upsert}.go`,
`internal/server/dispatch.go`, new
`internal/executor/cte_dml_preimage_reveal_test.go` (6 tests, each pinned to a
value captured from live PG 18.3 on port 65432),
`docs/design/0125-0053-dml-cte-preimage-reveal.md` + README index, fix_plan
(-0053 ticked, -0054 filed), 3 ledger rows.

Gates run: executor/server/planner/analyzer/mvcc/storage PASS; units gate PASS;
full `TestPort_Isolation*` PASS except the pre-existing
`TestPort_IsolationEvalPlanQual` (nightly `AI-20260806-011323-001`, confirmed
failing at HEAD without this change); `scripts/tpch-spotcheck.sh` Q12=2/Q13=35
canonical PASS; `scripts/tpcds-sf05-regression.sh plans` → `queries=99 same=99
changed=0` (capture `plans-20260806-165200.txt`, baseline NOT re-pinned);
pgbench smoke via the commit hook.

NEXT LOOP (banner: M0124 closed → M0125 → M0127 → M-NIGHTLY → M0123).
`ci/logs/action-items.md` was still run `20260806-011323` this loop (all 18
filed, nothing new) — re-check first; a NEW nightly at `status: pass` makes
M0127-P6.1 selectable. Otherwise the live M0125 choices are **`M0125-0054`**
(this loop's successor: PG runs the main plan FIRST and the DML CTEs after, in
`ExecPostprocessPlan` — four witnesses recorded, and
`TestCTEPreImageWriteWriteDivergesFromPG` exists to flip) or **`M0125-0048`**
(the faithful `AGG_MIXED` grouping-sets aggregate — fidelity, large, retires
the `__gs_src_N` hoist). -0031/-0032/-0033/-0041 stay `[→ M0127: absorbed]`.

In-flight: none. The PG reference cluster on 65432 was started this loop for
the oracle capture and stopped again (`bench/tpch/setup_pg.sh` to bring it
back; credentials come from `source bench/tpch/env.sh`).
