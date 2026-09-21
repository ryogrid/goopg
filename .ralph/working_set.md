(idle — nothing in flight)

# Loop #67 result — setop common-type FIXED (numeric category)

Banner: items 0-9 unchanged. Item 10's PgoutputInterop task is open but NOT
selectable — its own next step is "wait for the next nightly" and the run id
is unchanged (still 20260922-004850). So the next selectable was the
"Manually discovered" subsection's first task (a `###` under M-NIGHTLY).

## Root cause, found by reading
`SetOp.Output()` returns `n.Left.Output()` AND `wrapSetOpBranchWithCasts`
coerced only the RIGHT branch to the LEFT's schema. Those two together ARE
the first-member-wins rule.

## Fix
`setOpUnifyBranches` resolves each column's common type across both branches
and coerces BOTH. `SetOp.Output()` then becomes correct with NO change to
`Output()` — coercing the left branch is exactly what was omitted.
`setOpCommonTypeName` is the single decision point for widening later.

## Why bounded to the numeric category (stated, not assumed)
`applySetOp` folds members LEFT-DEEP, so goopg resolves pairwise where
upstream resolves all-at-once. That is equivalent only where the
implicit-coercion relation is a TOTAL ORDER — true in TYPCATEGORY_NUMERIC
(`int2<int4<int8<numeric<float4<float8`, float8 preferred AND maximal).
A non-total-order category would make the fold order-dependent, i.e. the
same defect in subtler form.

## Measured on live PG 18.3 (not derived)
1/2.5 -> numeric (was bigint); int2/int8 -> int8 (was smallint);
int4/float4 -> float4; float4/float8 -> float8; REVERSED float8/int2 ->
float8. The reversed pair is the PAIRED CONTROL: it distinguishes a real
resolution from a positional rule and passes even WITHOUT the fix, which is
why it sits beside cases that do not. Values unaffected (sum still 3.5).

## Gates (all green)
units; FULL upstream regress suite (only the known `partition_aggregate`);
tpch-spotcheck Q12=2/Q13=33; tpcds-sf025 PASS; acceptance arm 24/24;
pgbench smoke. Non-vacuity: neutralising the unification fails exactly the
four order-sensitive subtests, leaving the two controls green.
**One plan delta, investigated not waved through**: TPC-DS Q5 cost moved
(14363.79 -> 14595.00) but is STRUCTURALLY IDENTICAL — 66 lines, zero diff
once costs are stripped, rows/widths unchanged, MISMATCH=0. It is the cost
of the coercion Project, the same coercion PG inserts.
Trap worth carrying: my first cost-stripped diff was EMPTY because the awk
range used `=== Q5` while the capture writes `===== Q5 =====` — a vacuous
no-diff looks exactly like a real one until you confirm the range matches.

## Deferred + ledgered
Cross-category pairs (upstream raises 42804; goopg still accepts),
all-`unknown` -> text, and domain preservation. Widening needs a type-category
table the optimizer lacks (only `pgTypeCategoryForOID`, keyed by OID).

## Next loop
Item 10: PgoutputInterop (only if a NEW nightly ran — check the run id).
Otherwise the pre-existing milestones: M0119 -> M0122 -> M0131 -> M0134 ->
M0135/M0136 -> M0095/M0110.

## Owner escalations OPEN — three
1. M0145-0018 cost-model no-go. 2. M0145-0001 lineage exhausted (blocks all
of item 3). 3. partition_aggregate's inventory row marks a never-passing
case must-pass.
