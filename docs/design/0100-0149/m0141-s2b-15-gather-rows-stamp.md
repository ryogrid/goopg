# M0141-S2b-15 — impl: gather row-stamp divergence (`rel->rows`, not `computeGatherRows`)

`Kind: impl` · `Parent: M0141-S2b-13`

Status: blocked on gates — code complete and unit-green, staged uncommitted;
commit requires `tpch-spotcheck` + `tpch-acceptance-arm`, both SKIP-BLOCKED
while `bench/tpch/runtime_goopg/data.HOLD` stands (unclean-shutdown evidence
hold, owner-only recovery via `scripts/tpch-ref-recover.sh`). Task marked `[!]`
with an escalation block per `.ralph/gate-exceptions.md`.

## Task

`makeGatherPath`/`makeGatherMergePath` (`internal/optimizer/gatherpaths.go`)
always stamped `Rows = computeGatherRows(sub)` (subpath rows × parallel
divisor). PG's `cost_gather`/`cost_gather_merge` stamp `rel->rows` unless the
caller passes an overriding row estimate
(`postgres/src/backend/optimizer/path/costsize.c:452-458`, `:498-503`), and
`generate_gather_paths`/`generate_useful_gather_paths` compute
`compute_gather_rows` only under `override_rows=true`
(`allpaths.c:3099-3112`, `:3236-3253`). Call-site split in PG:

| PG call site | rel kind | flag |
|---|---|---|
| `allpaths.c:557` (`set_rel_pathlist` scan rels) | base | `false` |
| `allpaths.c:3518` (join search, per joinrel) | join | `false` |
| `geqo_eval.c:277` (GEQO joinrel) | join | `false` |
| `planner.c:7880`, `:8036` (topmost scan/join, unsafe target / nonpartial) | scan/join | `false` |
| `planner.c:5022` (`partial_distinct_rel`) | upper | `true` |
| `planner.c:7721` (`gather_grouping_paths`, grouped rel) | upper | `true` |

So a Gather over a scan/join rel carries the relation's own total, not the
divide-then-multiply round-trip; the tuple charge is then
`parallel_tuple_cost * path->path.rows` (`costsize.c:463`) — i.e. the stamped
value is also the costed value.

## What changed

- `makeGatherPath`/`makeGatherMergePath` take `overrideRows bool`:
  `rows := rel.Rows; if overrideRows { rows = computeGatherRows(sub, cp) }`,
  and the resolved `rows` feeds both `Path.Rows` and the gather cost call —
  the PG `path->path.rows` stamp + `* path->path.rows` charge pairing.
- `generateUsefulGatherPaths(rel, overrideRows)` threads the flag to both
  makers, mirroring `generate_useful_gather_paths`'s signature.
- Call-site mapping: `addBaseRelGatherPaths` → `false`; the
  `joinsearchlevel.go` per-level site → `false`; the `geqo.go` `mergeClump`
  site → `false`; `generateUpperRelGatherPaths` → `true` (PG's only `true`
  sites are upper rels — partial distinct and grouped).
- The `param_info` arm of PG's stamp is unreachable here: parameterized
  subpaths are refused by `gatherSubpathIsRunnable`, matching the prior
  design.
- Tests: `gatherpaths_test.go` now pins both arms (scan/join stamps
  `rel.Rows`; `overrideRows=true` stamps `computeGatherRows` and the cost
  tracks the stamped value). The crossover fixture had to shrink `rel.Rows`
  alongside `sub.Rows` — under the new stamp the relation count, not the
  partial count, sets the tuple charge.

## Gate results (2026-09-18/19)

- `go test ./internal/optimizer` — PASS; `RALPH_PRECOMMIT_SCOPE=units` — PASS.
- `scripts/tpch-spotcheck.sh` — **SKIP-BLOCKED** (source cluster under
  `data.HOLD`; the gate correctly refuses to clone evidence). Manual
  equivalent on a private `cp -a` of `preloss-clone-20260915` (the dataset
  `:65433` was restored from): **Q12=2, Q13=34 — canonical PASS**.
- `scripts/tpch-acceptance-arm.sh` — same SKIP-BLOCKED. Manual equivalent:
  TPC-H `-plan-only -serial` captures on the same private clone, patched
  binary vs HEAD binary — **plan files byte-identical** (serial capture emits
  no Gather nodes, so this change cannot move TPC-H serial plans).
- `scripts/tpcds-sf025-regression.sh sweep` — **PASS=96 MISMATCH=0
  CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3** + stamp; wall 142s vs 149s
  baseline; 40 plans changed vs the S2b-13-era capture — real shape changes
  inside fuzz bands (the intended mechanism), not just `rows=` text.
- TPC-DS parity (`capture-tpcds.sh` on the ea clone + `pg-plan-parity-diff.py`
  vs `bench/tpcds/plans-pg/` equivalent live capture): **match=2 (Q9/Q41) —
  floor holds, no prior match lost.** `CATEGORIES-EXCL-MATCH` vs S2b-13:
  aggregation-strategy 44→43, sort-strategy 69→67, qual-placement 23→22
  (improvements); join-method 66→67, scan-type 56→58, parallelism 84→85,
  join-order 90→89, parameterisation 46→46, rendering 24→24 (adverse within
  the ±3 churn band).
- TPC-H parity (`estimate-audit -plan-only -serial`, private preloss-clone
  copy, pinned `GOOPG_ANALYZE_SEED=20260905`, diff vs `bench/tpch/plans-pg/`):
  **match=7 — identical verdict set AND category counts to a same-clone HEAD
  capture** (Q1/Q6/Q10/Q11/Q13/Q14/Q15a; floor ≥7 per S2b-13's protocol:
  "nothing lost"). S2b-13's recorded 8 came from the live `:65433` clone's
  stats epoch; on the preloss clone both HEAD and patched read 7.
- `make ea-ratchet` — **FAIL: 10 NEW / 13 FIXED (76→73 net)**. All 10 NEW
  findings have `pg_est=None` — PG emits no comparable node for those
  relsets, i.e. the same relset-key churn class S2b-14 established, driven
  by the 40 plan moves (e.g. FIXED `Q85:dd+reason+wr+ws` → NEW
  `Q85:dd+wr+ws`, same relset minus `reason`). The two Q61 relsets S2b-14
  triaged (`date_dim+store+store_sales`, `date_dim+store_sales`) are FIXED.
  Triage + repin decision deferred: the commit cannot land while the TPC-H
  gates are blocked, so the findings list is recorded here for the resume
  loop to triage against the final tree.

## Secondary question — resolved: defer the display convention

S2b-14's caveat asked whether this task should also move goopg's
partial-path `rows=` display toward PG's per-worker convention. Findings:

- The path-model Gather now stamps correctly (`rel.Rows`, matching PG's
  `path->path.rows`). Stored partial-path `Path.Rows` is already per-worker
  (`costParallelSeqscan` divides by the parallel divisor, as PG's
  `cost_seqscan` does).
- The divergence S2b-14 observed (`Parallel Seq Scan rows=719876` vs PG's
  `rows=232218`) comes from the **post-pass** `rebuildWithGather`/
  `splitAggregate` path: those nodes carry the serial plan's estimates with
  a `Parallel` label — a different mechanism than the path-model stamp this
  task fixes. Moving the display convention means re-estimating or
  re-labelling serial-plan nodes inside the post-pass — a separate,
  larger surface touching EXPLAIN rendering, not the Gather path stamp.
- **Decision: deferred** to new task M0141-S2b-16 (filed in fix_plan; ledger
  row appended). S2b-15 stays scoped to the `override_rows` port.

## Movement

Movement: yes — TPC-DS `CATEGORIES-EXCL-MATCH` improved on three axes
(aggregation-strategy 44→43, sort-strategy 69→67, qual-placement 23→22) at
equal match floor (2=2 Q9/Q41); ea-ratchet findings net 76→73 with the two
S2b-14-flagged Q61 relsets FIXED; TPC-H byte-identical to HEAD (floor holds,
nothing lost). Commit pending on the TPC-H HOLD; the measured deltas above
are from the staged tree.
