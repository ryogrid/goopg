# M0142-0003b — Q9 L6 tie-break precision

Status: accepted (landed 2026-09-15)

## Task

`.ralph/fix_plan.md`'s M0142-0003b line, scoped by M0142-0003a's finding: at
TPC-H Q9's full join (L6), goopg's winning `join.hash` candidate
(`{part,supplier,lineitem,partsupp,orders}⋈{nation}`) and PG's own chain's
cheapest candidate (`nestloop.index` on
`{part,partsupp,supplier,nation,lineitem}⋈{orders}`) both rendered as
`total=80099.64` in the existing `DPPATH` trace, but the trace's `%.2f`
format could not say whether that was a genuine (if tiny) cost gap or an
exact tie decided by DP insertion order. Concrete question: does goopg's
chosen plan actually win on true cost, or does it merely tie and win by
tie-break?

## Change

`internal/optimizer/pathtrace.go`'s `formatPathLine` renders
`startup`/`total`/`inputtotal` via `%g` instead of `%.2f`. This is not a new
instrument — it brings `DPPATH` in line with the sibling `DPTRACE
cost`/`decline` channel (`joinsearchtrace.go`'s `total=%g`), which already
prints full precision for exactly this reason (Go's default `%g` prints the
shortest decimal string that round-trips to the same float64 bit pattern —
full precision when needed, the same short form as before for round numbers).
`internal/optimizer/pathtrace_test.go`'s three sentinel-format assertions
(`inputtotal=-1.00` → `inputtotal=-1`) were updated to match; no other test
depended on the two-decimal rendering. This is a production diff (the trace
code is compiled into the default build), but it is gated behind the
existing `GOOPG_PGSHAPED_DP_TRACE` env var and changes no default-path
behavior — same class of change M0142-0003a's own deferral note anticipated
("changing an existing, tested trace format's precision is instrumentation
work... not a recon-task edit").

## Method

Built `/tmp/goopg-m0142-0003b` (`go build ./cmd/goopg`, HEAD + the precision
diff above). Cloned `bench/tpch/runtime_goopg/data` to a private throwaway
dir (`/tmp/pp-m0142-0003b/data`, deleted after use), capped scratch server
(`GOOPG_CG_UNIT=m0142-0003b`, `scripts/goopg-test-run.sh`) on port `5534`,
`GOOPG_PGSHAPED_DP_TRACE=1`. Same protocol as M0142-0003a: one `psql`
session, `SET max_parallel_workers_per_gather = 0;`, `ANALYZE` all eight
HammerDB tables, plain `EXPLAIN` on Q9's exact SQL
(`internal/testutil/tpch/tpch.go`'s `Queries()[9]`). Trimmed trace evidence
committed under `analysis/m0142/m0142-0003b-q9-{plan,dptrace-l6,dppath-l6}.txt`.
Throwaway server/clone/binary stopped and removed after use; the shared
`:65432`/`:65433`/`:65437`/`:65438` bench clusters were never touched.

**Caveat, same as -0003a**: this run's own `ANALYZE` drew a fresh sample
(goopg's per-connection stats have no seed pin,
`goopg_plan_pin_three_drift_sources`), so the absolute numbers below (e.g.
`rows=120` vs -0003a's `rows=75`) are not byte-comparable across runs. What
is stable — the topology, which candidates tie, and the tie-break mechanism
— reproduces exactly (see Findings).

## Finding 1 — the tie is EXACT, not a rounding artifact

At full float64 precision, both competing L6 candidates print the identical
`total`:

| producer | partition (`outer`⋈`inner`) | startup | total | verdict |
|---|---|---|---|---|
| `join.hash` (goopg's winner) | `{part,supplier,lineitem,partsupp,orders}` ⋈ `{nation}` | `6777.825` | `108806.04332442369` | **accepted** |
| `nestloop.index` (PG's chain) | `{part,partsupp,supplier,nation,lineitem}` ⋈ `{orders}` | `6777.825` | `108806.04332442369` | dominated |

Both `startup` and `total` are bit-identical strings under `%g` (Go's
shortest-round-trip formatter — two different float64 values cannot print the
same shortest string, since that string parses back to exactly one bit
pattern). This is a genuine tie, not merely a "very close" reading that
`%.2f` happened to round the same way — M0142-0003a's "near-exact tie" is
now measured to be an **exact** one.

Notably, the two candidates reach that identical total via different
composition: their `inputtotal` (the priced cost of the input join tree
beneath the final join) differs by ~50 (`108802.83` for goopg's spine vs
`108752.90` for PG's spine), and the marginal cost of the final join differs
by a similar amount in the other direction (~3.21 marginal to add `nation`
via hash vs ~53.14 marginal to add `orders` via index-nested-loop — the same
asymmetry M0142-0003a's Finding 2 described, now at exact precision). The two
different (input, marginal) pairs summing to the identical bit pattern is
either an algebraic identity in how these two costs happen to be constructed
from the same underlying row/selectivity estimates, or a striking numerical
coincidence; this task did not chase which (out of scope — see Deferral).

## Finding 2 — the tie-break rule itself is PG-faithful, not a goopg shortcut

Cross-referencing `DPTRACE pair`: at `lev=6`, goopg's own partition
(`{lineitem+orders+part+partsupp+supplier}|{nation}`) is the **first**
pairing, `created=1` (it constructs the level's `RelOptInfo`). PG's chain's
pairing (`{lineitem+nation+part+partsupp+supplier}|{orders}`) is a later
pairing at the same level, `created=0` (its paths are offered into the
already-created rel's pathlist, never build a new one).

`addToPathlist` (`internal/optimizer/path.go:1080-1098`) rejects a newcomer
outright when `comparePaths` returns `relEqual` — an exact tie is not
"better", so the **first-registered** path (whichever pairing ran first at
that level) survives and the later-arriving tying candidate is marked
`dominated`, regardless of which partition it came from. `comparePaths`
(`internal/optimizer/path.go:894-922`) is itself PG-faithful: it dispatches
through `comparePathCostsFuzzily`/`stdFuzzFactor`, porting PG's own
`compare_path_costs_fuzzily`/`STD_FUZZ_FACTOR` dominance test from
`postgres/src/backend/optimizer/util/pathnode.c`. **The dominance rule is not
a goopg-only shortcut** — real PG's `add_path` makes the identical
first-registered-wins call on an exact or fuzzy tie.

## Verdict — answers the fork, and reframes the follow-on

M0142-0003b was filed as: "if goopg's total is genuinely lower, a
costing-term audit (B8/B10 named suspects); if PG's total is lower or
exactly equal, the DP's tie-break rule is the finding, and a different kind
of fix applies." Finding 1 settles this: **the costs are exactly equal, not
lower** — goopg's chosen plan does not win Q9's L6 step on true cost. But
Finding 2 sharpens "a different kind of fix applies" further than the
original two-way framing anticipated: the tie-break **mechanism**
(first-registered-wins under fuzzy-cost dominance) is itself correct, PG-
faithful behavior, so **fixing it would depart from PG, not converge to
it**. The load-bearing divergence is therefore **which partition gets
registered first at level 6** — i.e., a join-search **enumeration-order**
question, not a costing-term question (B8/B10 are not implicated by this
specific tie) and not a dominance-rule question. Real PG's planner produces
PG's shape as Q9's winner, which is only consistent with PG-faithful
first-registered-wins semantics if real PG's own `join_search_one_level`
(`postgres/src/backend/optimizer/path/joinrels.c`) visits/registers a
PG-chain-equivalent relset before goopg's chain's equivalent at the analogous
level — an ordering comparison this task did not make (see Deferral).

## Deferral

One ledger row: (a) an enumeration-order parity check — does goopg's
level-6 pair-generation loop visit relset pairs in the same order PG's
`join_search_one_level` does, and if not, does reordering it change which
partition wins Q9's tie; (b) the bit-exact-tie composition question (Finding
1's coincidence-or-identity open question) — lower priority, informative but
not required to resolve (a). Resume point:
`internal/optimizer/joinsearch*.go`'s level-6 relset-pair generation loop,
compared against `postgres/src/backend/optimizer/path/joinrels.c`'s
`join_search_one_level`.

## Gates

`go build ./...` clean. `go test ./internal/optimizer/...` (full package,
not just the trace tests) passes (three format-sentinel assertions in
`pathtrace_test.go` updated for `%g`, all other tests unaffected — no other
test depended on the two-decimal rendering). No values/plan-gate re-run: the
trace is gated behind `GOOPG_PGSHAPED_DP_TRACE`, which defaults off, and
`formatPathLine`/`tracePath` are only invoked from that gated call site — the
default execution path is provably unchanged by construction, not merely by
absence of a failing test.

`scripts/tpch-spotcheck.sh` (the practice card's mandatory planner-change
gate) could not run this loop: its private-clone snapshot step
(`scripts/lib/tpch-private-clone.sh`) requires the shared `:65433` cluster to
go quiet before copying its data dir, and a `tmp/goopg-bench-bin` process was
already listening on `:65433` for the gate's full 3×60s retry budget (a
pre-existing process, not started by this task — AGENT.md's "never restart
the shared cluster" rule forbids stopping it to unblock the snapshot). This
is the documented shared-cluster-contention failure mode
(`goopg_shared_bench_cluster_collisions`), not a result of this change.
Given the trace's gated-and-inert-by-construction property above, the
package's own unit suite is judged sufficient evidence for this specific
diff; the spotcheck should still be run opportunistically once the shared
cluster is free, before any *further* change lands on top of this one.
Pre-commit hook's pgbench smoke: runs as part of `git commit` per the
mandatory-on-every-commit policy (independent of the `:65433` TPC-H cluster).
Throwaway server/clone (`/tmp/pp-m0142-0003b`, `GOOPG_CG_UNIT=m0142-0003b`)
and binary stopped/removed after use; the shared
`:65432`/`:65433`/`:65437`/`:65438` bench clusters were never touched.
