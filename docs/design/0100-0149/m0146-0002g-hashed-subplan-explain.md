# M0146-0002g: EXPLAIN renders a SubPlan sublink as PG does

## PG behaviour

`get_rule_expr`'s T_SubPlan arm (postgres/src/backend/utils/adt/ruleutils.c)
prints an ANY / ALL sublink as `(ANY <testexpr>)` / `(ALL <testexpr>)`.
The test expression's PARAM_EXEC references to the subplan's output print
through `get_parameter` → `find_param_generator` as `(SubPlan N).colK`,
or `(hashed SubPlan N).colK` when the subplan uses a hash table. A NOT IN
is the boolean NOT above that. TPC-H Q16 prints
`Filter: (NOT (ANY (ps_suppkey = (hashed SubPlan 1).col1)))`.

Whether a SubPlan is hashed is decided at plan time by
`subplan_is_hashable` (subselect.c). It requires:
- an ANY sublink (never ALL);
- no correlation;
- hashable equality operators;
- an estimated result — rows × (MAXALIGN(width) +
  MAXALIGN(SizeofHeapTupleHeader)) — that fits in hash_mem.

## goopg before

`formatInExprPG` printed `(x = ANY (SubPlan N))`, a documented divergence
from before SubPlan params existed, and never said `hashed`.

## Change

- `formatSubPlanInExprPG` renders the sublink form as PG does. A row
  operand compares column by column, ANDed, as PG's testexpr does. The
  literal-list IN keeps its `x = ANY (...)` form.
- `subPlanUsesHashTable` is `subplan_is_hashable` for the shapes goopg's
  executor hashes (`evalInHashProbe`: uncorrelated, plain `=` ANY, a single
  operand) plus PG's size test against the session's hash_mem.
- `explainHashMem` supplies hash_mem (`hashsize.HashMemLimit` over work_mem
  and hash_mem_multiplier, as the hash operators use), passed into
  `walkPlan` / `walkPlanAnalyze`.

## Results

- TPC-H Q16's filter is byte-identical to PG's.
- TPC-DS Q45 (SF1) now matches PG with no structural divergence;
  `parameterisation` drops by one at both scales. Q10 and Q35 change text
  only.
- Row counts, TPC-H census and the pass-required regress cases are
  unchanged. `subselect` moves slightly closer.
- Evidence: `analysis/m0146/m0146-0002g/`.

## Not covered (ledgered)

- The executor still decides hashing at run time. It hashes an eligible
  subplan whatever its estimated size (PG would not above hash_mem), and
  never hashes a multi-column row IN (PG can).
