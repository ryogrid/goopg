# M0146-0100 — a derived subquery leaf publishes its plan's ordering

Status: done 2026-10-08 (b337c0b16). Parent: M0146-0099.

## Problem

A derived-table leaf entered the join search with no pathkeys. A merge join
over it therefore always sorted the subquery's output, even when the
subquery's own plan (a GroupAggregate, a Sort) already delivered that
order.

The extra sort used to be absorbed into the merge node and stayed
invisible. M0146-0099 re-emitted merge input Sorts, and from then on it
printed — together with the `Subquery Scan` the Sort keeps below it. For
example, TPC-DS Q78 printed `Sort → Subquery Scan → GroupAggregate` on all
three inputs, where PG merges two of them presorted.

## PG behaviour

`set_subquery_pathlist` (allpaths.c) builds one SubqueryScan path per
path of the subquery's final rel. Each one gets
`convert_subquery_pathkeys(root, rel, subpath->pathkeys, …)`: the
subpath's ordering in the outer query's terms, kept only up to the first
key whose column has no equivalence class in the outer query.

## Change

- **CTE arm, generalised.** `addCTEScanPathkeys` already did this for
  CTE-scan leaves (M0146-0005b). It now covers every sub-plan leaf
  (`isSubplanLeaf`).
- **Reading the ordering.** `subqueryLeafPathkeys` reads the leaf plan's
  own ordering through `inputNodePathkeys`, in the leaf's output
  positions. That is the same sound reader the ordered upper stages use.
  A hash aggregate or plain Gather claims nothing, so it publishes nothing.
- **Translating it.** The ordering goes through the CTE arm's "useful
  column" map, which holds the merge-clause columns and the query
  pathkeys. The shared body is `convertLeafPathkeys`.

The pathkeys go on the leaf's unparameterised `PathPrebuilt` path. Merge
path generation then finds a presorted outer or inner and builds no
`PathSort` for it.

## Verification

- **Probe.** The jr_\* fixture with `enable_hashagg = off` gives the same
  EXPLAIN text on goopg and PG 18.3: `Merge Join → GroupAggregate`, with
  no Sort and `dn` stripped. `TestMergeJoinOverPresortedSubqueryLeaf`
  pins it, and disabling the subquery arm fails it.
- **TPC-DS fire set** (SF0.25 and SF1). Q65 and Q78 change, and executed
  results are identical.
  - Q65 is now PG's plan node for node at both scales. Only the bare
    column names in `Merge Cond` / `Join Filter` differ, which the
    classifier counts as qual-placement (M0146-0097's rendering ledger).
  - Q78 merges two GroupAggregates presorted, like PG. It still differs in
    the ws/cs join order and in PG's Materialize over each presorted inner
    (M0146-0101).
  - Most categories drop by one at both scales; qual-placement rises by two
    at SF0.25 and by one at SF1.
- **TPC-H.** Plans are byte-identical between HEAD and the new binary.
- **Regress A/B** (join, subselect, window, union, partition_join,
  aggregates, with, select_distinct). Output is identical, apart from
  join's index-only-scan versus bitmap-scan flap on plain `onek` scans.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25
  sweep and ea-ratchet all PASS.

## Deferred (ledgered)

- **One subquery plan only.** goopg plans a subquery once, so only the
  chosen plan's ordering is offered. PG keeps the subquery's sorted
  alternatives beside its cheapest path, so a merge can pick a costlier
  presorted GroupAggregate over a cheaper HashAggregate. That
  alternative-path offer is not built.
- **Materialize over a presorted grouped inner.** PG materializes a merge
  join's presorted grouped inner when its uniqueness proof fails. That is
  M0146-0101.
