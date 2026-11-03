# M0148\-0001f1 — EXPLAIN VERBOSE Output: scan targetlists, Append suppression

**Parent:** M0148\-0001 · **Status:** landed 2026\-11\-03; closed (residue in
M0148\-0001f2)

## Divergence

goopg printed every node's whole output schema as its VERBOSE `Output:`:

- **Statement projection.** `SELECT a FROM t` printed `Output: a, b, c`
  on the Seq Scan; PG prints `Output: a`.
- **Append.** goopg printed an `Output:` line under every Append; PG
  prints none.
- **Appendrel members.** The scans under an Append printed every column.
  PG prints only what the Append's consumer reads: aggregates.sql's
  `Parallel Seq Scan on public.tenk1 tenk1_1  Output: tenk1_1.unique1`
  under `variance(unique1)`.
- **Sublinks in the select list.** `ARRAY(SubPlan 1)` / `(SubPlan 1)`
  were missing from the scan's Output.

## PostgreSQL

- `create_plan` hands the final targetlist to the top plan node
  (CP\_EXACT\_TLIST). A projection over a projection\-capable scan is folded
  into the scan's tlist (`create_projection_plan` /
  `is_projection_capable_path`).
- `make_sort_input_target` computes the sort input below a Sort: the
  select list plus each sort key as a resjunk entry, evaluated by the scan.
- `create_append_plan` builds each child with CP\_EXACT\_TLIST over the
  parent rel's reltarget, so a member's tlist is the columns needed above
  the Append. A restriction pushed into a member is a scan qual, not a tlist
  entry.
- `show_plan_tlist` (explain.c): "The tlist of an Append isn't real
  helpful, so suppress it", and likewise for MergeAppend.

## Change

All in `executor/operators_explain.go`, `explainOutputNames`.
Display only; no plan changes.

- **Append suppression.** A SetOp that renders as Append or Merge Append
  prints no Output.
- **`explainScanProjectionOutput`.** A scan's Output is the targets of the
  Project above it.
  - The walk from the scan may pass through Filters, Sort (which adds its
    keys as resjunk entries) and Limit.
  - The Project must be a PG targetlist: from it up to the root, only
    Projects, Filters and pass\-through nodes (Sort, Limit, Gather).
  - goopg's internal Projects (appendrel tableoid and remap wrappers,
    narrowing) are therefore never used.
- **`explainAppendMemberOutput`.** An appendrel member scan prints the
  Append columns its consumer reads.
  - The consumer is the node above the outermost Append, through Filters
    whose columns it also reads: an Aggregate, the statement Project, or
    the plan root (all columns).
  - Each column is translated down through the member's wrapper Projects
    by substituting their targets. An identity cast added by goopg's UNION
    type unification is unwrapped. Constant targets (tableoid, narrowing
    placeholders) fall back to the schema.

## Verification

- `TestExplainVerboseScanOutputTlist` pins four shapes with PG 18.3's
  output: a scan projection, `ORDER BY b LIMIT` (resjunk key), an aggregate
  over UNION ALL, and a root UNION ALL with a member filter.
- A scratch\-PG probe matches on those shapes. The remaining differences
  are the residue below.
- **Regress runner, HEAD → change, 22 suites.** Lines, with unmatched PG
  `Output:` lines in parentheses:
  - aggregates 443 → 358 (11 → 2); the single\-suite run is down to 2
    hunks;
  - inherit 3208 → 3192 (37 → 33);
  - subselect 2531 → 2518 (146 → 144);
  - groupingsets 1613 → 1611;
  - limit 278 → 277;
  - partition\_prune 6785 → 6783;
  - the rest unchanged; no suite grew.
- `TestPort_RegressSuite`, `TestPort_PgDump*` and `TestPort_Isolation*`
  pass. The TPC gates show no change; fire\-set fires=none on all three
  corpora.

## Remaining (M0148\-0001f2)

- **Joins over subqueries.** A join's Output over a subquery or aggregate
  should deparse the referenced expressions (`s2.s2`,
  `(sum((s1.s1 + s2.s2)))`), where goopg prints the subquery's column
  names (`s2, sm`). This is aggregates hunk 1 and most of join's 361
  unmatched lines.
- **Sort\-input expressions.** PG evaluates a non\-volatile select\-list
  expression below the Sort (`Output: c, (a * 2), a` on the scan,
  `((a * 2))` above it). goopg computes it after the Sort, which is a
  plan\-shape difference.
- **Join inputs.** Physical versus projected tlists for join inputs
  (`use_physical_tlist`), and `Inner Unique: true`.
- **Older ledger items** (2026\-11\-02): implicit casts and typed literals,
  SRF RTIDs, and Result nodes above an aggregate.
