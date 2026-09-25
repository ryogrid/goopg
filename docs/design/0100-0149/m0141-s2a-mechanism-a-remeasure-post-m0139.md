# M0141-S2a — mechanism (A) re-measure post-M0139: unchanged, and structurally cannot change

Status: accepted (landed 2026-09-15)

## Task

`.ralph/fix_plan.md` M0141-S2a: "once M0139 (executor-side narrowing) lands and
the GROUP_AGG rel's input carries only the columns the aggregate needs,
re-run S1's TPC-H capture and check whether `costAgg`'s R3 spill arm
(`cost_funcs.go:516-540`) still over-charges HASHED for Q3 (and ... Q10, Q13,
Q18)." M0139 landed in full between S2's capture and this task (six slices,
all DONE — see `.ralph/fix_plan.md` M0139 section's closing note). Per the
plan-parity harness this is measurement; **no production code is touched by
this task.**

## Method

Live re-capture against the same bench clusters S1/S2 used (`:65433` goopg
TPC-H, `:65432` PG 18.3 TPC-H reference — both up throughout, neither
restarted, neither owned by this task), via the same tool S1/S2's procedure
names (`docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md`):

```
go build -o /tmp/estimate-audit-s2a ./cmd/estimate-audit
PGPASSWORD=tpch /tmp/estimate-audit-s2a -plan-only -label m0141-s2a-tpch \
  -out /tmp/m0141-s2a -port 65433 -db tpch -user tpch -password tpch \
  -ref-port 65432 -ref-db tpch -ref-user postgres -ref-password postgres
```

**Binary-provenance check (K91):** the `:65433` server's serving binary
(`tmp/goopg-bench-bin`, built 2026-09-15 08:35, `sha256=64d18ab1...`) postdates
M0139-S2's landing commit (`62df1ee65`, 07:06 same day) and every other M0139
commit; a `go build` of HEAD at task time produces a *different* hash
(`52e5211a...`), but Go binaries differ on build-id/timestamp alone even from
identical source, so hash equality is not the right test here. The right
test — and the one actually used below — is behavioral: `EXPLAIN (VERBOSE)`
against the live `:65433` server on a **known-narrowed** two-table join (TPC-H
Q12's `orders ⋈ lineitem`) shows the Hash Join's `Output:` trimmed to
`o_orderkey, o_orderpriority, l_orderkey, l_shipmode` (4 of the two tables'
25 combined raw columns) — the exact `narrowJoinLeg` signature M0139-S2's own
report describes. The serving binary genuinely carries M0139's narrowing;
the re-measurement below is not measuring a stale artefact.

## Finding 1 — Q3, Q13, Q18 are byte-for-byte unchanged from S1/S2's capture

Re-captured cost, row and width figures for all three are **identical to
S1/S2's committed `analysis/m0141/m0141-s1-tpch.plans.txt`** down to two
decimal places — e.g. Q3's Hash Join is still
`cost=58686.91..285402.01 rows=319324 width=176`, still feeding a `Sort` then
`GroupAggregate`, still losing the contest to PG's `HashAggregate` directly
over an unsorted, narrower (`width=29`) join. Q13 and Q18 show the same
GroupAggregate-vs-HashAggregate flip on their inner aggregate node, also
unchanged. **M0139 landing moved nothing for mechanism (A).**

Live `EXPLAIN (VERBOSE)` against the *same* query (no `LIMIT`, serial forced
via `SET max_parallel_workers_per_gather = 0`, matching the tool's exact
protocol) shows something S2's capture-file-only method couldn't: the Hash
Join's own `Output:` list **is** narrowed post-M0139 —
`l_shipdate, l_orderkey, l_discount, l_extendedprice, o_orderdate, o_orderkey,
o_shippriority` (7 columns, not the ~25 raw columns across the three base
tables) — while its EXPLAIN-reported `width=176` and every cost figure above
it stay exactly what they were pre-M0139. **The Output list narrowed; the
cost did not move at all.** This decoupling is the finding, not a
measurement artefact — see Finding 2.

## Finding 2 — root cause: `applyUpperNarrowing` runs at `Plan()`'s tail, strictly after the GROUP_AGG cost contest already ran

`internal/optimizer/planner.go`:

- `Plan()` (`:95`) calls `planStmtWithSettings` at `:141`. That call recurses
  all the way down to `buildAggregateStage` / `createGroupingPaths` — the
  Hashed-vs-Sorted `PathAgg` contest (`groupingpaths.go:340`
  `addGroupingPaths`, called from `createGroupingPaths:92`, itself called
  from `planSelect`'s aggregate-stage handling at `planner.go:1760`) — and
  returns a **fully finished, already-costed, already-strategy-decided**
  plan tree as `node`.
- Only *after* that full return does `Plan()` run
  `node = applyUpperNarrowing(node)` at `:189` — M0139-0003's narrowing entry
  point, which is what produces the 7-column `Output:` list observed above.
  `narrowJoinLeg`'s `GOOPG_NARROW_LEG_HOOK` (M0139-S1/S2) is wired into
  `joinInputsFor`, called during the *same* earlier `planStmtWithSettings`
  pass that builds the join tree the aggregate consumes — so even the
  join-leg narrowing is arguably available before `createGroupingPaths` runs
  on paper, but `aggInputWidth` (`groupingpaths.go:327`) reads
  `child.Output()` off `aggNode.Child`, and `narrowPlanOutput`
  (`narrowoutput.go:708`) always returns a **new wrapping `*Project`**
  rather than mutating `child` in place — so unless `aggNode.Child` is
  reassigned to that wrapper before `createGroupingPaths` runs, the
  aggregate's own cost input is the pre-narrowing node regardless. Whichever
  of the two timing facts is doing the work, the observed behavior is
  conclusive: **the strategy choice and its cost figures are fixed before
  any narrowing pass — join-leg or upper — has a chance to touch the tree
  the aggregate was costed against.**

This means M0139, exactly as landed and sequenced, **cannot** feed back into
`costAgg`'s spill-arm currency (`cost_funcs.go:516-540`) for *any* query,
not just Q3/Q13/Q18 — by construction of the pipeline order, not by an
incidental gap in this specific case. M0139-S1's own pre-registered
prediction ("no parity movement; the pass fires N > 0 times... a slice that
predicts a match flip is mis-scoped") already said this about M0139's
category-movement potential in general; this task's contribution is
confirming the *specific* mechanism (pipeline ordering, not e.g. a missed
call site) and that it applies to mechanism (A) specifically, which S2's
Finding 2 had gated on M0139 as if landing it would eventually resolve the
re-measurement favorably. **That gating assumption is refuted — not merely
"not yet true", but structurally impossible under the current pipeline
order.**

## Finding 3 — this is not a new problem class; R124 §7 already tested the adjacent hypothesis and it failed

`docs/design/not_ralph/plan_parity_fix_take2/TODO.md`'s R120/R124 log
(cited by S2's Finding 2, "the K65/K66 ncols-narrowing family") records that
R124 §7 already tried pairing a corrected hash-agg width **currency**
(`GOOPG_HASHAGG_WIDTH_CURRENCY`, `cost_funcs.go:500-508`'s comment) with an
earlier ncols-narrowing pass and "measured identical to R120's arm alone" —
the pairing hypothesis was refuted and the flag deleted (M0137-0009). That
prior narrowing pass predates M0139 and is not proven to have the same
pipeline-ordering defect this task found, so it is not a perfect precedent —
but it is a second, independent data point that "land some form of
narrowing, then re-measure" has already failed once for this exact
cost-model gap, which raises the bar for what a future fix needs to do
beyond "narrow, then re-measure": the width feeding `costAgg` has to be the
**post-narrowing** width *at cost time*, and the currency itself
(`inAvgVarBytes` vs a true `tupleWidth`, per R120's own diagnosis) still
needs correcting too — both conditions, not either alone.

## Finding 4 — Q10 was never in scope; the cited unit test's flip does not reproduce at SF=1

`groupingpaths_test.go`'s `TestCostAggHashedNeverChargesSpill` doc comment
names Q3/Q10/Q13/Q18 as flipping under the spill arm, but S1's own committed
TPC-H `aggregation-strategy` list is `{Q2, Q3, Q4, Q5, Q8, Q12, Q13, Q18,
Q21, Q22}` — Q10 is not a member. The live SF=1 re-capture confirms why:
Q10's `HashAggregate` (`cost=277063.47..277632.27 width=684`) already
**matches** PG's own `HashAggregate` (`cost=267114.99..267788.13 width=205`)
choice — same strategy on both sides, same as before M0139, no mismatch to
re-measure. The unit test's flip is a small-scale, synthetic-data artefact
(matching M0139-0005's finding that TPC-H's Q4 tie also does not reproduce
at 20,000-row scale) — not evidence Q10 needs anything from this programme.

## What every M0137-M0143 task report must contain (per AGENT.md)

- **Category movement**: none — `aggregation-strategy` count for TPC-H is
  unchanged (still 10 tagged, still the same members mismatched).
- **shape-delta**: 0 (no production code touched; the only thing measured
  is a pre-existing, already-shipped narrowing pass observed via live
  `EXPLAIN VERBOSE`, not modified).
- **Stats epoch**: unchanged from S1/S2 (`# stats-epoch:` in
  `/tmp/m0141-s2a/m0141-s2a-tpch.txt`, matching S1's committed
  `analysis/m0141/m0141-s1-tpch.txt` header — same cluster, not re-ANALYZEd
  between S1 and this task).
- **Seam-decline census**: not applicable — no seam-declining code path is
  exercised or changed by a recon task.
- **Planning route**: newly distinguished this task — `applyUpperNarrowing`'s
  position at `Plan()`'s tail (`planner.go:189`) vs `createGroupingPaths`'s
  earlier position inside `planStmtWithSettings` (`planner.go:141` ->
  `:1760`) is the exact planning-route fact Finding 2 turns on.

## Verification

- Live re-capture completed against `:65433`/`:65432` without ERROR/TIMEOUT;
  `PLAN-PARITY` summary line unchanged from S1's (`match=6` floor held —
  re-confirmed, not re-quoted from memory).
- Binary-provenance check (see Method) confirms the serving binary carries
  M0139's narrowing behaviorally, ruling out a stale-artefact explanation for
  "unchanged" before attributing it to pipeline ordering instead.
- `go build ./...` clean (no file touched by this task; re-confirmed as
  S0/S1/S2's own practice).
- No temporary artefacts committed — captures went to `/tmp/m0141-s2a/`
  (not `analysis/m0141/`, since the content is a byte-identical
  reproduction of the already-committed S1 capture plus VERBOSE probes that
  add no new plan-shape information worth freezing).

## Resume points (filed as separate fix_plan slices, see below)

- **M0141-S2a-fix** (new, gated on this task): move (or duplicate, read-only,
  for costing purposes) narrowing *before* `createGroupingPaths` runs — or
  equivalently, make `aggInputWidth` compute what the input width *would be*
  after narrowing, at cost time — **and** pair it with a corrected
  `inAvgVarBytes` currency (R120's `hashAggTupleWidth` shape, not simply
  reinstating the deleted `GOOPG_HASHAGG_WIDTH_CURRENCY` verbatim — R124 §7
  already falsified "currency fix alone" once). This is planner-search-order
  surgery (the GROUP_AGG rel's input costing needs to see a **post-narrowing**
  child, which today does not exist until narrowing has already run at
  `Plan()`'s tail) plus a cost-model change — two coupled changes, not a
  one-line patch. Do not attempt inside a single task without first scoping
  which is cheaper: moving narrowing earlier, or computing a narrowing
  preview at cost time without moving the pass itself.
- Mechanism (B) (`M0141-S2b`, filed by S2) is untouched by this task and
  remains the larger, separately-gated item.
