# M0146-0009e — isunique + leaf-tuples propagation for FROM-clause derived leaves

Impl task filed by M0146-0009c (the Q83 arm of the 5-finding EA recon).
Design doc: `docs/design/0100-0149/m0146-0009e-derived-leaf-isunique.md`.

## Defect

`examine_variable` assigns `vardata->rel = find_base_rel(varno)`
UNCONDITIONALLY for a Var operand (selfuncs.c:5331), so a Var over a
FROM-clause derived leaf — `*CTEScan`, `*SubqueryScan`, set-op, VALUES —
keeps the leaf's own `rel->tuples`/`rows` as its estimate inputs, and the
RTE_SUBQUERY/RTE_CTE arm adds `isunique` when the probed output column is
the body top query's lone GROUP BY or DISTINCT(ON) key (:5865-5883).

goopg did neither for prefix derived leaves:

- `resolveJoinVarColumn` resolves through the leaf binding's synthetic
  `catalog.Table` (planner.go:4564 stamps `ce.table` name+columns with no
  stats), so examination took the `ok` path with `v.tuples = baseRows = 0`
  → `getVariableNumDistinct` hit `tuples <= 0` → `DEFAULT_NUM_DISTINCT=200`
  regardless of the leaf's real size.
- `subqueryUniqueOutput` — the only isunique channel — is set solely for
  pulled ANY-subquery leaves (`joinsearchseam.go` pulled band), never for
  FROM-clause `*CTEScan`/`*SubqueryScan` leaves, and it is leaf-wide, not
  per-column.

Q83 witness (SF0.25): `sr_items ⋈ cr_items` over lone `GROUP BY i_item_id`
outputs — leaf ests 20 and 10, join est `20*10/200 = 1`. PG marks the key
isunique → `nd = leaf tuples` → `20*10/max(20,10) = 10`.

## Change

- `cte_stats_synthesis.go`: `cteOutputColStats.unique` + shared classifier
  `loneKeyPositions(body, width)` — Aggregate lone `GroupExprs` (group keys
  occupy output positions `[0, len(GroupExprs))`, plan.go:1391), lone-key
  `DistinctOn`, single-column `Distinct`; peels Filter/Sort/Limit, the
  *Project remap, and pushed-qual *SubqueryScan labels; punts on set ops /
  grouping sets / multi-key bodies — upstream's own early exits.
- `cardinality.go`: `baseRelInfo.{leafTuples,leafRows,uniqueOutCols}`.
- `joinsearchseam.go`: prefix-leaf init fills them when
  `isSubplanLeaf(scans[i])` — tuples = `EstimateRows(scans[i])`, rows =
  `EstimateRows(leaves[i])` (leaf-local Filter applied).
- `joinselectivity.go`: `examineJoinVar` overrides tuples/rows with the
  leaf's estimates on the `ok` path when `leafTuples > 0`, and marks
  `isUnique` per probed column via `derivedLeafUniqueCols`; the `!ok` arm
  generalizes the same way (pulled/synthetic leaves already carry
  `EstimateRows` in baseRows — `table == nil` now also yields tuples, the
  same `rel = leaf` fact upstream gives).

Not implemented (ledger row filed): the arm's last clause recurses
`examine_simple_variable(subroot, var)` for passthrough output vars to
recover the underlying BASE column's real statistics — Q95's
`ws_wh.ws_order_number` class. The existing `cteColPassthrough` kind is the
natural carrier.

## Evidence

- `tmp/m0146-0009e/probe83-goopg-after.txt` — standalone
  `sr_items ⋈ cr_items` probe: `Hash Join rows=10` (was `rows=1`; PG's own
  plan for the same relset estimates 10 — `tmp/m0146-0009c/probe83-pg.txt`).
- `tmp/m0146-0009e/q83-goopg-after.txt` — real Q83: the three-way merge
  chain now estimates 5 and 2 (was 1), the same shape class PG reports.
- `tmp/m0146-0009e/q95-{goopg-after,pg}.txt` — Q95's flagged
  `ws1.ws_order_number = ws_wh.ws_order_number` semijoin stays `rows=1` on
  BOTH engines: `ws_wh` is a self-JOIN body (no group key) so neither marks
  the output unique, and the driving filtered chain estimates ~1-10 on
  both sides — a PG-shared underestimate, not a defect of this class.
- `make ea-ratchet` (fresh capture, this tree): **PASS — 52 findings
  fixed**, including all four flagged Q83 relsets (`cte:sr_items`,
  `cte:cr_items`, `cte:wr_items`, `sr_items+wr_items`), `Q95:cte:ws_wh+…`,
  and the Q78/Q85 composite relsets whose leaf vars now carry real tuples.

## Gates

| gate | result |
|---|---|
| `go test ./internal/optimizer/` | PASS (incl. new derivedleaf_unique_test) |
| `RALPH_PRECOMMIT_SCOPE=units` | PASS |
| `scripts/tpch-spotcheck.sh` | PASS (Q12=2, Q13=33) + stamp |
| `scripts/tpcds-sf025-regression.sh sweep` | PASS=96 MISMATCH=0 ERROR=0 TIMEOUT=0 + stamp; plan-diff moved 6 (Q2 merge+1, Q54, Q58, Q65, Q77, Q83) — all row-count correct |
| `ACCEPT_BASELINE=… tpch-acceptance-arm.sh on` | PASS — 24/24 digest MATCH + stamp |
| `scripts/tpcds-fireset-gate.sh` | PASS — fires sf025{Q2,Q54,Q58,Q65,Q77,Q83} sf1{Q2,Q4,Q11,Q54,Q64,Q65,Q83}, no introduced timeouts + stamp |
| `make ea-ratchet` | PASS (52 fixed) |

## Incidents during the loop

- `tmp/c20a/data-sf025` carries a torn WAL tail from earlier SIGKILLs; the
  clone starts only with `GOOPG_WAL_ALLOW_EARLY_END=1` (throwaway clone —
  table data is checkpointed, only the un-checkpointed tail is dropped).
- `make ea-ratchet-repin` on the default `EA_PORT=5534` reproduced
  M0146-0009d live: a foreign `postgres` (pid 1748354, leftover
  `tmp/pg-probe-data` probe cluster) was squatting on the port, so
  `pg_isready` short-circuited startup and the capture wrote 0 scored nodes
  + a 0-entry baseline — caught, baseline reverted from HEAD, repin rerun
  successfully on `EA_PORT=5541` with the WAL override. This is a second
  in-the-wild demonstration of the M0146-0009d defect.
