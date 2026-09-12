# R95 REPORT — lateral-probe workers land; Q96 goes parallel by cost

R95 follows scope commit `49e1f75` (reviewed APPROVE-WITH-NOTES, all
notes reflected). Implementation ships in `2f3febf`. No defaults, PG
sources, worker sizing, join order, cost formulas, join-method choice,
partial-path dominance, EXPLAIN text, or R94 ordinary-shape rules changed.

## What shipped

- Planner predicate `lateralProbeJoinIsPartialCapable` + probe check
  `lateralProbeIsPartialProbe` (`parallel.go`): lateral INNER NL over a
  bare equality index probe (Key/Keys, no SAOP — the `pidx` leaf filter
  serves single-range only; no range bounds; `Index` required). CROSS/
  SEMI/ANTI/LEFT/RIGHT/FULL refused as scope-minimization (CROSS has no
  R60 producer; Q96's commas plan as INNER).
- Walks (`drivingScan`, `stampParallelScan`, `drivingScanCrossesSort`)
  descend the outer literally under the new predicate; `unstamp` needed
  no change (both-side descent covers it — pinned). `HasShareableHashJoin`
  descends approved-NL outers (ordinary AND lateral): without this, the
  collection below promises a prebuild it never performs and workers
  build partial hash tables with missing rows — the same latent hole
  existed for R94's ordinary shape and is closed here for both (the
  agreement test names both predicates so narrowing either reopens it
  loudly).
- Path classifier (`gatherpaths.go`): parameterized probe inner admitted
  only for INNER jointype, `PathIndexScan` with index clauses, partitioned
  relsets, and a re-checked V8 subset test (`req ⊆ outer`); Memoize-loop
  output, clauseless/seq/unsatisfiable/unpartitioned inners refused.
- Executor: `lateralProbeJoinPartial` (plan shape + built-op check —
  unwrapped `*indexScanOp`/`*indexOnlyScanOp`, never `lateralBindable`,
  which pins the no-wrappers rule at the operator level), literal-left
  arms on all three claim walkers with the inner-bitmap guard,
  NL-left descent in `collectShareableJoins`. Drive-by hardening the
  review's matrix demanded: nil-plan refusal (not panic) in the bitmap
  and index Join arms.
- R60 filing untouched (INNER-only since R94; parameterized probes were
  always filed — only the classifier refused them).

## Q96 result (the round's gate)

Q96 now plans `Finalize Aggregate → Gather (3 workers) → Partial
Aggregate → Nested Loop` with parallel hash joins below and the lateral
time_dim probe inside — `agg-upper verdict=split`, chosen by cost, not
forced. Value **266** serial, natural-parallel, and opt-in alike
(byte-identical values across all three). Census verdict moves to
`SHAPE-DIFF [join-order,scan-type,qual-placement]`:
aggregation-strategy, join-method, sort-strategy and parallelism now
match PG; the remaining distance is join order (+ scan-type/qual
placement downstream of it) — the next round's subject, not this one's.

## Gates and evidence (all artefacts under `/tmp/pp2/r95/`)

Binary `goopg-r95` built from HEAD `2f3febf`.

- Local suites: `go test ./internal/optimizer ./internal/executor
  ./internal/testutil/estimateaudit` all pass; `go vet` clean;
  `git diff --check` clean. New tests: planner predicate/walk/
  classifier/prebuild-agreement/selection pins
  (`partial_lateral_test.go`); executor identity (hand-built
  decomposed-probe trees faithful to the measured shape — 186 rows,
  matching SQL-driven LATERAL), walker refusals incl. the fake
  bindable, prebuild collection agreement, subqCache-depth pin
  (`parallel_lateral_probe_test.go`).
- TPC-H digest (private clone `:5562`): **24/24 MATCH**, verdict PASS.
- TPC-DS SF0.25 foreground sweep (private clone): **PASS=96,
  MISMATCH=0, CKMISMATCH=0, ERROR=0, TIMEOUT=0, SKIP=3**
  (`sf025-results/sweep-20260912-145823.txt`) — Q35/Q69 green this run
  (2s/1s), confirming R94's two failures were sweep-tail environment,
  not code.
- Controls Q9/Q41/Q91: natural vs opt-in EXPLAIN byte-identical
  (hashes unchanged from R94); Q96 differs by design (shape above,
  costs 24630 natural / 16367 opt-in — search-side partial paths price
  per-worker under opt-in).
- Fresh live-PG TPC-DS census: **match=2, shapediff=69, unparsed=0,
  missingnode=25, error=3, timeout=0** — totals identical to R94 with
  Q96's categories strictly fewer.

## Next boundary

Q96's remaining `[join-order,scan-type,qual-placement]` distance —
relation order under the lateral probe — needs its own evidenced scope.
Do not widen the lateral predicate (CROSS/SEMI/general subtrees) to
chase it without one.
