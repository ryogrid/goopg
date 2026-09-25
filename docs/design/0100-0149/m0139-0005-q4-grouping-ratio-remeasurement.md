# M0139-0005 — re-measure Q4's grouping-election ratio

Status: accepted
Milestone: M0139 — Executor-side narrowing / projection pushdown
Type: recon (measurement only; no production diff, per the plan-parity
harness's recon-task carve-out in `AGENT.md` §"Plan-parity harness")

## Goal

R81 (`docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/02-open-problems.md`
§B1) located TPC-H Q4's plan-shape divergence one rel above the grouping
contest, in `electOrderedGrouping` (`upperorderedgrouping.go:148`): goopg's
two ORDERED-rel candidates (a HashAggregate needing an explicit top `Sort`,
and a group-keys `Sort`-then-`GroupAggregate` that already delivers the
`ORDER BY` for free) come out **fuzzy-equal** on startup cost (ratio 1.0086,
inside `stdFuzzFactor = 1.01`) so goopg's `M0129-S1` tie-break picks the
actual-cheaper (hashed); PG's own startup ratio for the same contest is
1.0118, **outside** fuzz, so PG picks the sorted candidate directly. R81's
named unblock condition (i) was "DatumBytes / projection pushdown — neither
exists." M0139-S1/S2 built exactly that mechanism since R81 was written.
This task's job: re-measure whether it moved Q4's ratio.

## Method

A throwaway probe (`internal/testutil/tpch/zz_probe_m0139_0005_test.go`,
deleted before commit, same pattern as M0139-S3) built a private, disposable
cluster from HEAD via `internal/testutil/cluster` + the existing
`scaleLoader` (20,000 orders / ~68,000 lineitems, real TPC-H DDL and
vocabularies), never touching the shared, peer-owned `:65433` bench server.
It set `GOOPG_DEBUG_M0139_0005=1` in the test process's environment before
`cluster.Start()` (the server subprocess inherits it), ran `EXPLAIN
(VERBOSE, COSTS ON)` on the canonical Q4 text (`tpch.Queries()[4]`), and read
the server's own log file (`cluster.LogPath()`) for temporary,
env-gated debug lines added to `electOrderedGrouping` (printing every
`ordered.Pathlist` candidate's Startup/Total cost and pathkey count) and to
`narrowJoinLeg` (printing every call, its decline reason, and which
keep-set tier fired). **Both instrumented files were reverted with `git
checkout` before commit** — `git diff --stat` on both is empty at HEAD;
nothing here landed as production code, matching the S1/S2/S3 precedent for
a temporary measurement probe.

## What HEAD actually does (measured, not assumed)

The default-settings Q4 plan at HEAD:

```
Sort  (cost=1288.80..1288.81 rows=5 width=72)
  Sort Key: orders.o_orderpriority
  Output: o_orderpriority, count
  ->  HashAggregate  (cost=1287.04..1287.09 rows=5 width=72)
        Group Key: orders.o_orderpriority
        Output: o_orderpriority, count
        ->  Hash Semi Join  (cost=0.00..1278.84 rows=656 width=448)
              Hash Cond: (orders.o_orderkey = l_orderkey)
              Output: o_orderdate, o_orderkey, o_custkey, o_orderpriority,
                      o_shippriority, o_clerk, o_orderstatus, o_totalprice,
                      o_comment
              ->  Seq Scan on public.orders  (cost=0.00..207.39 rows=739 width=448)
                    Filter: (o_orderdate range)
                    Output: <all 9 orders columns>
              ->  Seq Scan on public.lineitem  (cost=0.00..1064.89 rows=26622 width=550)
                    Filter: (l_commitdate < l_receiptdate)
                    Output: <all 16 lineitem columns>
```

Two independent, mutually-corroborating findings:

**1. Zero narrowing anywhere in this subtree, at any leg.** Both `Seq Scan`
nodes emit every column of their table — including the `Hash Semi Join`
itself, whose `Output:` is the full 9-column `orders` row, not the single
`o_orderpriority` column the `HashAggregate` above it actually needs (the
join key `o_orderkey` is not needed either, once semi-join matching is
done). The live trace makes this unambiguous rather than inferred from
width numbers: **`narrowJoinLeg` was never called at all** for this query —
zero debug lines, not even a decline line, in the entire server log for the
run. Contrast M0139-S3's Q12 probe, where the same instrumentation style
showed the hook firing and narrowing a hash join's build side; here it does
not fire because it is never reached.

**2. `electOrderedGrouping` only ever sees ONE candidate.** The debug dump
of `ordered.Pathlist` printed exactly one line
(`startup=1288.800548 total=1288.813048 pathkeys=1`), not two — meaning
whichever ORDERED-rel candidate is cheaper dominates the other outright in
`addPath` at this scale (not a close, fuzzy contest at all). The resulting
plan (`HashAggregate` then a top `Sort`) is consistent with the sorted
candidate having been eliminated well outside the fuzz band here, not with
a near-tie the way R81 describes at SF=1.

## Why narrowing cannot reach Q4's join (root cause, not just an observation)

Tracing why `narrowJoinLeg` is never called: Q4's `EXISTS (SELECT * FROM
lineitem WHERE l_orderkey = o_orderkey AND l_commitdate < l_receiptdate)` is
handled by `unnestExistsExpr` (`internal/optimizer/unnest.go:4078`), goopg's
correlated-EXISTS decorrelation rewrite. That function builds the physical
`&Join{Type: JoinTypeSemi, Algo: JoinAlgoHash, Left: outerChild, Right:
innerPlan, ..., schema: append(Schema(nil), outerChild.Output()...)}` node
**directly, as a rewrite step on the raw parse tree, before any search
joinrel exists** — it does not go through `createHashJoinPlan` /
`joinInputsFor`, the ONE call site `narrowJoinLeg` is wired into
(`createplanjoin.go:392-394`). `outerChild` is `Output()`-full by
construction (line 4367's schema assignment names it explicitly), and
nothing downstream ever re-visits it to narrow.

This is not a new, isolated bug — it is the same fork
`docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/01-what-we-learned.md`
F11/K63 (R73-R77) already named for a different symptom: **"No SEMI path is
ever filed — zero `addPath` calls with `Jointype == JoinSemi`" ... "R74
traced the fork (`unnestExistsExpr` consumes the EXISTS subquery into a
legacy `Join{SEMI, Hash}` before any search joinrel exists, so no sjinfo →
no joinrel → no path → no price)."** F11 tracked the *costing* consequence
(a display-seam price with no real path behind it, later given a two-site
splice by R77's `stampPlanCost`/`legacyDisplayCostOf` patch, further
root-caused for the `Filter`-embed gap by `.ralph/deferral_ledger.md`'s
2026-09-15 `m0137-0011-filter-node-missing-plancost-embed` row). **This task
establishes the corollary for narrowing**: the same "before any search
joinrel exists" bypass that stops a real Path/price from ever being filed
for Q4's semi join *also* stops `joinInputsFor`'s narrowing call chain from
ever being invoked on it, because that chain is wired to the Path-based
`createHashJoinPlan`, not to `unnestExistsExpr`'s direct node construction.
Building `narrowJoinLeg` (M0139-S1/S2) assumed the missing attachment point
was *only* "no call site wraps this leg" (true for every leg S1/S2's own
corpus census covered); Q4's leg is a stronger case — no call to
`joinInputsFor` happens for this join **at all**, so there is no plan-time
moment where any Path-keyed mechanism (pricing per F11/K63, or narrowing per
this task) can reach it.

## Verdict

**The ratio has not moved, and cannot move under the current mechanism.**
S1/S2's narrowing is real and load-bearing for every join the Path-based
search actually builds (confirmed on Q12 by M0139-S3), but Q4's semi join is
not one of them — it is produced by `unnestExistsExpr`'s pre-search rewrite,
which S1/S2 never touched and which structurally cannot be reached by a
hook wired into `joinInputsFor`. The width feeding
`electOrderedGrouping`'s startup-cost comparison for Q4 is therefore
byte-identical to what it was before M0139-S1/S2 landed, so **no
re-measurement of the 1.0086-vs-1.01 ratio at real SF=1 scale is
informative until this bypass is addressed** — the number cannot have
changed by construction, independent of scale. (The small-scale probe run
here shows a further, secondary fact — at 20,000 orders the two ORDERED-rel
candidates are not even fuzzy-tied, only one survives `addPath` outright —
but that is scale sensitivity in the underlying cost comparison, not the
question this task was scoped to answer, and is not investigated further
here.)

## What this does and does not decide

This is a measurement task, not a fix. It answers M0139-0005's question
("does the narrowed width move Q4's ratio?") with a mechanism-level "no, and
here is exactly why" rather than a numeric near-miss — a stronger and more
actionable result than re-running the same election at a bigger scale would
have been, since the width genuinely never changes for this query
regardless of scale. It does not implement the fix (wiring `narrowJoinLeg`,
or a real Path, into `unnestExistsExpr`'s output) — that is a materially
different, larger task (touching the un-costed-SEMI-path mechanism F11/K63
and the `m0137-0011` `Filter`-embed line both already track) than a
narrowing-only patch, and is out of this recon task's scope. `M0139-0006`
("put the packed-retention decision to the owner") should carry this
finding alongside S3's residue measurement: two of M0139's three
`minimize_datum`-adjacent recon items (Q4's ratio, Q12's residue) both now
point at "the join-leg narrowing hook doesn't reach every join the search
can produce," from opposite ends (Q12: the hook fires but retains more than
hoped; Q4: the hook cannot fire at all).

## Gates

Recon task: no production diff (`git diff --stat` on the two temporarily
instrumented files, `internal/optimizer/upperorderedgrouping.go` and
`internal/optimizer/joinleghook.go`, is empty at HEAD). `go build ./...`
clean after the revert. No ledger row for the narrowing-bypass finding
itself: it is a corollary of the already-tracked F11/K63/`m0137-0011`
lineage (goopg-internal mechanism gap, not a newly discovered
PG-incompatibility on its own), consistent with the M0139-S1/S2/S3
precedent of recording purely-internal findings in the design doc rather
than the ledger. The underlying F11/K63 gap ("goopg's EXISTS/IN
decorrelation bypasses the cost-based join search entirely, unlike PG's
`convert_EXISTS_sublink_to_join`") remains open and already tracked; this
task adds the narrowing-specific resume pointer for whoever picks it up.
