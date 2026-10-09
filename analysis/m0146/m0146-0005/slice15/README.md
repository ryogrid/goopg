# M0146-0005 slice 15 (M0146-0005o): INTERSECT / EXCEPT row estimates

Witness: TPC-DS Q8 (SF0.25), first divergence `join-method` at depth 3.
PG's zip INTERSECT estimates 200 rows, and goopg estimated 535 (half the
smaller input). PG therefore sized `store ⋈ INTERSECT` at 12 rows and
nested-looped it under a nested loop at the top. goopg's 32 rows picked
hash joins.

## PG's rule (generate_nonunion_paths / build_setop_child_paths, prepunion.c)

Each arm contributes a group count:
- the arm's rows, when its own query level has GROUP BY, aggregates,
  HAVING or DISTINCT, or when the arm is itself a set operation;
- otherwise `estimate_num_groups` over its output expressions.

INTERSECT returns the smaller arm's groups and EXCEPT the left arm's. The
ALL forms return rows (the smaller arm's for INTERSECT ALL, the left
arm's for EXCEPT ALL).

On the private PG (`q8-left.sql`, `q8-right.sql`, `q8-intersect.sql`, at
512MB work_mem), Q8's left arm `substr(ca_zip,1,5)` estimates 3185 groups.
The right arm `select ca_zip from A1` is not grouped at its own level, and
`a1.ca_zip` has no statistics, so it gets the default 200. min = 200.

## Change

`estimateSetOp` (cardinality.go) applies the rule for INTERSECT and
EXCEPT. `setOpArmGroups` decides "grouped" from the arm's top node
(Aggregate, Distinct, DistinctOn or SetOp, looking through
Filter/Sort/Gather); a Project on top marks a new query level. UNION's
non-ALL `/2` is unchanged (ledgered).

## Results

- SF0.25 Q8 now plans PG's shape: the INTERSECT is 200 rows, with nested
  loops over `store_pkey` × INTERSECT and at the top. The record moves
  from `join-method` to `join-order` at the same node; PG's `Materialize`
  nodes remain.
- SF1 Q8 moves the other way (PG hash-joins, goopg now nested-loops).
  PG's SF1 reference estimates `store` at 40 rows with cost 10.40
  (`pg-sf1-store-default-estimate.txt`), the default for a table with no
  statistics. goopg sees the real 12 rows. This is a reference-cluster
  statistics artefact, not an engine gap; it is noted on the
  measurement-data task.
- Gates: units PASS (one parallel-hash-spill test flaked under the nightly
  load and passed 3/3 alone and on re-run); fire set PASS (Q8, Q14, Q38,
  Q87; no timeouts); TPC-H 5/22 identical; spotcheck PASS; the acceptance
  arm has 24 MATCH. The sweep and arm ran FORCE=1 (values-only) during the
  nightly batch, so their timings are void.
