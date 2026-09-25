# R83 SCOPE: LIMIT above DISTINCT (Q41 nearest miss)

Lineage: R82 DS baseline selected Q41
(`[join-order,parameterisation,sort-strategy]`, no
parallelism/agg) as the cleanest next target.

## Step-0 finding (measured, no code changed)

Q41 (`SELECT DISTINCT i_product_name ... ORDER BY
i_product_name LIMIT 100`):

- goopg: `Sort → Unique → Limit → Sort → SeqScan`
- PG: `Limit → Unique → Sort → SeqScan`

Assembly order in `planSelect`
(`internal/optimizer/planner.go`): ORDER BY sort (:1940s)
→ LIMIT wrap (`:2063-64`, guard `:2028`) → Project
(`:2144`) → DISTINCT spec (`:2334-41`) → M0097-0046 outer
Sort (`:2404`). So LIMIT lands BELOW Unique (via an
intervening Project). PG applies LIMIT above Unique,
always: pushing it below is not a valid optimization
(counterexample: 150×`'a'`+50×`'b'`,
`SELECT DISTINCT v … ORDER BY v LIMIT 100` — PG returns
`{a,b}`; goopg's shape truncates to the first 100
pre-distinct rows and returns `{a}`). PG-side proof:
`bench/tpcds/plans-pg/Q41.txt` shows Limit above Unique;
no Limit-below-Unique pushdown pass exists; executor
`ExecSetTupleBound` (`execProcnode.c:848-990`) descends
only through Sort/IncrementalSort/Append/MergeAppend/
Result/SubqueryScan/Gather/GatherMerge, stopping at any
node that can combine rows — Unique is such a node. The
bug is values-masked on current data only because
78 distinct < 100.

No later pass reorders Limit/Distinct (only
`liftLimitAboveLockRows`, LockRows+SKIP LOCKED only).

Out of scope (review correction): the ORDER BY sort's
limit bound (`limitTuplesForOrderedSort`) is COST-ONLY in
goopg (Sort has no bound field; executor sorts fully) —
PG likewise bounds in costing but never in execution, so
goopg already matches PG exactly there. No change.

Blast radius: Q41 ONLY. Same-statement DISTINCT+LIMIT
audit: Q6 (DISTINCT in scalar subquery, outer LIMIT
distinct-free), Q38 (DISTINCTs inside INTERSECT branches,
outer `count(*)` LIMIT plans at the set-op site), Q54
(outer GROUP BY+LIMIT, DISTINCT only in CTE
`my_customers`) — all unaffected by construction; any
shape move there is a STOP trigger. `query_0.sql` is a
4900-line bundle outside the sweep (`query${q}.sql`
only). TPC-H: verify via A/B (no DISTINCT+LIMIT
expected).

## Change (single half)

Limit node above Distinct: reorder the `:2063-64` LIMIT
wrap after the `:2334-41` DISTINCT adoption (resolve
`s.Limit`/`s.Offset` against the same pre-projection
`ctx`; only re-parent the built node — constant/param
LIMIT is schema-transparent). Decline preconditions (else
keep today's order): (a) `s.WithTies` (`TiesKeys` are
pre-projection coordinates); (b) Limit/Offset exprs
containing `ColumnRef`/outer-vars. All corpus cases are
constant LIMITs. Safe to keep: HAVING inside `node`;
B-01c keep touches only `orderSort` (`enclosingtree.go`
already has a Limit arm; compute/assert-only);
`terminatesPartial` treats Limit≡Distinct; minmax
early-return and LockRows lift don't intersect.

The top M0097-0046 outer Sort stays (separate change with
its own risk — named follow-up, not this round). The `$0`
vs `i1.i_manufact` correlated display stays (named
follow-up — PARAM_EXEC lowering vs outer-var rendering).

## Predictions (recorded before implementing)

- P1: Q41 becomes
  `Limit → Sort → Unique → Sort → SeqScan` —
  Limit above Unique. Remaining gaps: top Sort extra vs
  PG (follow-up), `$0` (follow-up), join-order alignment
  artifact (expected gone). Net 3 → 1–2 cats.
- P2 (gated by a NEW synthetic test, since the sweep
  passes with the bug present at 78<100): duplicates-
  exceed-limit fixture (150×'a'+50×'b', DISTINCT+LIMIT
  100) returns both values — fails before, passes after.
- P3: corpus A/B — ONLY Q41 may move, toward PG;
  Q6/Q38/Q54 movement is a STOP trigger (over-broad
  change); ZERO EXTRA otherwise.

## STOP rules

- Reorder-contract break (HAVING aliasing, B-01c keep,
  plan-cache mutation, WithTies/non-constant miss):
  stop, record, narrower slice.
- Values MISMATCH on sweep beyond the predicted
  duplicates case: stop, scope the repair (do NOT revert
  to wrong-but-matching).
- Q41 doesn't move as P1: re-probe, don't force.

## Explicitly out

M0097-0046 outer-Sort elimination; `$0` correlated
display; sort-bound costing changes; join-method costing;
K100; TPC-H (verify-only).

## Gates

Units, suites, synthetic duplicates test (P2 criterion),
TPC-H spotcheck, SF0.25 sweep (`MISMATCH=0`-class),
byte-guard A/B both corpora + per-query census with ZERO
EXTRA flips, shape-delta alongside. `match` count is NOT
a criterion.
