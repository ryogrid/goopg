Task: M0141-S0 — "scoping recon (measurement only, no production change)" for
the AGGSPLIT_INITIAL_SERIAL/FINAL_DESERIAL programme (upper-planner ordering
contest milestone). **DONE, committed and pushed** this loop (branch
`plan-parity-with-pg-take2-ralph`, commit `7750d48b4`). No production code
changed (recon-only, matching S0's mandate).

Files: `docs/design/0100-0149/m0141-s0-aggsplit-programme-scoping-recon.md`
(new design doc), `docs/design/README.md` (+index row), `.ralph/fix_plan.md`
(M0141-S0 checked off; six slices S1..S6 filed with an entry gate),
`.ralph/progress.json` (state-guard timestamp repair, incidental).

What was found (headline): K96/K97 undersold what already exists and
overstated corpus reach. goopg already ships a working Partial/Finalize
aggregate split (`AggMode`, `combineAggRuntime`'s per-aggregate combine rules,
`AggregateIsDecomposable`'s whitelist, and TWO independent plan producers — a
legacy post-cache pass at 12/22 TPC-H matches, and a newer costed-path version
at 7/22, an unresolved regression this recon surfaces but does not fix). The
genuinely missing piece is narrower: `AggStrategySorted × AggMode∈{Partial,
Final}` (PG's `Finalize GroupAggregate <- Gather Merge <- Partial
GroupAggregate`) — confirmed absent at the executor level (`openSorted` gated
to `Mode==AggModeSimple`) AND structurally incompatible with today's design:
Partial mode emits zero rows via an in-process side-channel accumulator
specifically so `Gather` stays aggregation-agnostic, but `GatherMerge` must
interleave real per-group rows by sort key, which the side channel has none
of. Decisive scoping fact: TPC-H's canonical protocol runs `-serial=true`
(zero Gather on both engines), so NONE of TPC-H's `aggregation-strategy`(10)/
`sort-strategy`(9) mismatches can involve the missing Gather-Merge machinery —
it's a serial Hashed-vs-Sorted cost/path-selection question over machinery
that already exists (`openSorted`, `groupingpaths.go`'s sorted `PathAgg`
candidate, `createplansimple.go:173,219`'s existing `Strategy` wiring — which
appears to CONTRADICT a stale comment at `plan.go:1346-1348` claiming the
planner doesn't set `Strategy` yet; unresolved, flagged for S1 to settle
first). TPC-DS's 69/76 is an unseparated mix of that same serial question and
genuinely parallel cases already gated behind M0140's own open floor (K92,
K41).

Key symbols: `optimizer.AggMode`/`Aggregate.PartialSource` (plan.go),
`combineAggRuntime` (parallel_agg_combine.go), `AggregateIsDecomposable`
(parallel_agg.go), `aggPartialAccum`/`parallel_agg_split.go` (the side-channel
being sized around), `openSorted` (operators_join_agg.go:2672, the
AggModeSimple-gated sorted executor path), `groupingpaths.go` (sorted/hashed
PathAgg candidate builder), `createplansimple.go:173,219` (Strategy wiring —
check this first in S1), `partialaggupper.go`/`partialaggpaths.go` (the
7/22-vs-12/22 regressed costed-path producer, unresolved, out of S0's scope).

Gates run: `go build ./...` clean (no production code touched). Pre-commit
hook (pgbench smoke) passed on `git commit` (not bypassed). `make
ralph-state-guard`: same recurring stale status="running"/progress="completed"
pattern as every prior loop this session; auto-repaired, confirmed consistent.
Nightly triage: all 13 open `AI-20260914-235643-*` items already filed under
M-NIGHTLY (re-verified this loop, nothing new to file).

In-flight: none.

Next step: Select **M0141-S1** — "serial Hashed-vs-Sorted audit (selectable
now)". First action: resolve the `plan.go:1346-1348` vs
`createplansimple.go:173,219` contradiction found above (read
`groupingpaths.go`'s hashed/sorted `PathAgg` candidate cost comparison and
trace one TPC-H aggregation-strategy-tagged query's plan build end to end).
Second action: a live capture (`estimate-audit -plan-only`, per `AGENT.md`
§"Plan-parity harness" measurement section — NOT `capture-tpch.sh`, which
skips ANALYZE) + `scripts/pg-plan-parity-diff.py` pass to split TPC-DS's
69/76 aggregation-strategy/sort-strategy counts into serial-shaped (in S1/S2
scope) vs already-parallel-blocked (M0140's floor, out of scope). S1 is a
recon task per the plan-parity harness — measurement first, S2 lands the fix.
Read `docs/design/0100-0149/m0141-s0-aggsplit-programme-scoping-recon.md`
before starting; do not re-derive what it already found. Do not select
M0141-S3..S6 (the parallel AGGSPLIT machinery) until S1/S2 quantify a
parallel-shaped residual — per the entry gate, K96/K97's say-so alone is not
sufficient justification.
