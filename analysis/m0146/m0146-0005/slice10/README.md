# M0146-0005 slice 10 (M0146-0005h): join keys deparse through a set operation

Witness: TPC-DS Q14's `cross_items` join (`q14-hash-cond.txt`). After slice
8 the join matched PG's, but its Hash Cond printed the INTERSECT
subquery's own column names (`brand_id`). PG prints the first branch's scan
column (`iss.i_brand_id`). ruleutils.c's `resolve_special_varno` follows
an OUTER/INNER Var into the child plan's target list, and
`set_deparse_plan` makes a SetOp's or Append's outer plan its first child.
The first-divergence census compares Cond text, so Q14 still counted as a
`join-order` divergence at depth 2.

## Change

`explainNames.setOpResolvedColumn` (explain\_names.go) walks a join key's
output column down to a named scan. It passes through set operations
(first input), identity Project columns, Filter/Sort/Gather/GatherMerge,
and joins whose output is the concatenation of their inputs. It answers
only when the walk crossed a set operation, so every other key keeps its
rendering. `formatJoinKeyCond` uses it for each key side.

## Results

- `census-diff-*.txt`: Q14's first divergence moves from depth 2 to
  depth 7 (SF0.25, join-order inside a branch) and depth 6 (SF1,
  parallelism). Nothing else moves.
- `sf025-sweep.txt`: 96/96. `tpcds-fireset.txt`: Q14 (and Q5 at SF1) fire
  with no timeouts. TPC-H: 5/22, spotcheck PASS, the acceptance arm has 24
  MATCH.

## Not ported (ledgered)

The same deparse rule for set-op columns in Filter, Sort Key, Group Key and
projection expressions. Only join keys resolve through a set operation.
