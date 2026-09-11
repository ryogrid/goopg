# R73 SCOPE: NL-SEMI price audit (Q4 570.66 < outer 15570.66)

Lineage: R72 STEP0 ruling (c), follow-up program item 3 (NL-semi
rescan/startup, R69 sister arm) + a display-seam suspect found while
grounding this scope. R69 scoped SEMI/ANTI out (non-owners); this
slice picks exactly that up. Program items 1 (selectivity) and 2
(widths, R70-blocked) are explicitly OUT — no estimation, no width
work in this slice.

## The anomaly (anchors)

Q4 goopg (`/tmp/pp2/r72/q4-plan1.txt`):

- `Nested Loop Semi Join (cost=0.00..570.66 rows=57066)`
- outer child `Seq Scan on orders (cost=0.00..15570.66 rows=57066)`

Join total < outer total. Arithmetically impossible through every
known site:

- `nestloopCost` (`internal/optimizer/cost_funcs.go:723`, run
  assembly `:750-754`): Total = outer.Total + inner.Total +
  per-tuple + rescans ≥ outer.Total.
- `DeriveLegacyDisplayCost` default arm
  (`internal/optimizer/plancost.go:117`, arm `:154-159`): Total =
  childTotal + perRow ≥ outer display cost.

So the winner either never passed through those functions or carries
a cost written elsewhere. P0 finds out which — before any fix talk.

## P0 — attribution (temp-instrumented, foreground, Gate-0)

Binary: clean-HEAD build + TEMP env-gated stderr ONLY (revert before
any slice commit). Cluster: private TPC-H SF=1 clone + 55xx port
(memory: shared-bench-collision rule; `GOOPG_CG_UNIT` cap).
Query: `/tmp/pp2/r71/q4.sql` EXPLAIN ×2, byte-identity required.

1. Log the PATH-level cost: at `addPath`/`setCheapest` for the SEMI
   joinrel (relids of orders⋈lineitem), print winner Kind/Jointype,
   Cost, Children costs + which producer filed it (NLI
   `joinpathsnli.go:341` with `rsStart` vs plain `addNestLoopPath`
   `pathgen.go:157` with startup literal 0 vs other).
2. Log the DISPLAY-level cost: what the EXPLAINed join node carries
   (`PlanCostCarrier` set? `legacyDisplayCostOf` fallback?) and the
   outer child's carried cost.
3. Verdict table: path-cost vs display-cost, named producing site.

PG cites (read, not implemented): `initial_cost_nestloop`
(`postgres/src/backend/optimizer/path/costsize.c:3267` — SEMI
defers inner run at `:3307-3324` to `final_cost_nestloop :3349`,
early-stop fraction `2.0/(match_count+1)` at `:3412`) — PG SEMI ≥
outer always. goopg must satisfy the same inequality whichever site
produces the number.

## P1 — fix iff mis-transcribed (R69 discipline)

- Iff P0 names a planning site whose terms mis-transcribe PG
  (missing rescan startup/run, dropped outer cost, wrong early-stop
  handling): minimal fix at that site, PG-term-keyed comment.
- Iff P0 names the display seam (election priced correctly, EXPLAIN
  prints otherwise): NO cost fix. File the seam evidence, re-scope —
  the R72 P2 table then needs a path-cost column (display numbers
  are not planner numbers).
- Either way: temp reverted, `go build ./internal/optimizer/`,
  byte-identity re-verified post-revert.

## Gates (slice commit bar)

- values 24/24 + TPC-DS SF0.25 sweep PASS=96 all-zero (unchanged).
- pp: Q4 (+Q22 read-only) verdict deltas listed; Q13 untouched.
- No rows/widths movement claimed (items 1–2 own them).

## Explicitly out

Widths, selectivity/rows, election rules, fuzz tiebreak, Q13-inner
admission, parallelism. Q22 ANTI is read-only verification (same
family per R72 P3) — recorded, not bundled.
