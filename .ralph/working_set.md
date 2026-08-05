(idle — nothing in flight)

Last loop: **M0127-P5.9-p CLOSED** — test-only change, units gate green.

**The filed proposal was measured and does not work; that is the finding.**
`buildGeometry` sizes a hash table from `planner.EstimateRows(buildNode)`, and
for a SEARCHED scan the base restrictions ride on the scan node rather than in a
`*Filter` wrapper — `seqScanRows` (cardinality.go) returns the relation's row
count and ignores them. So "a deliberately mis-estimated selectivity on the
build side" never reaches the geometry. Measured with `b.k = b.kdup`
(column-column eq, `defaultEqSelectivity` 0.005, true for every row): the JOIN
line moved `rows=1840`→`rows=171` (the search prices it in `makeJoinRel`) while
`Batches: 4` never budged and no `(originally …)` form appeared. Two estimates
for one scan; the executor reads the one that does not change.

**The lever that works is WIDTH.** `buildGeometry` passes `avgVarBytes = 0` (no
per-column average-width statistic — ledger 2026-08-03 M0127-P3.1), so a
text-heavy build is priced as if only its Datum array were resident. That is the
one mis-estimate leaving ROW counts — and therefore the join algorithm —
untouched, which is exactly what the pinned legacy arm existed to work around.
New `spillFixtureWidth(t, probe, build, distinct, padBytes)`; the growth test
uses 400 rows × 2 kB text: prices ~19 kB, occupies ~800 kB →
`Buckets: 1024 (originally 1024)  Batches: 32 (originally 4)` on the DEFAULT
enumerator with the sizer INSTALLED. Both crutches removed
(`SetRelationSizer(nil)`, `SetPGShapedJoinSearch(false)`); the test now also
asserts the plan really is a `Hash Join`. Negative control (width back to 48 B):
`Batches: 4` up front, never moved — the assertion has teeth.

Files: `internal/executor/join_batch_explain_test.go` (only code file);
09 §3.23 + `docs/design/README.md`; ledger row P5.9-p + fix_plan tick.

Gates run: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — 0
FAIL (executor 5.9s, rest cached); pgbench smoke via the commit hook. No
tpch-spotcheck / DS05: the change touches no production code, so no plan shape
or row count can move.

NEXT LOOP (banner in `.ralph/fix_plan.md` wins — M0127 is #3 and current): no
P5.9-* item is open. Larger successors, in order of the banner's intent:
03 §4.4 `SpecialJoinInfo` inference for an outer link buried below an inner one
(Q78). Ledger P5.9-p follow-up: make a scan's base restrictions reduce
`EstimateRows` (stamp the post-qual count on the leaf, the `EstRelRows`/
`SmallDim` precedent) — corpus-wide, needs its own bar. Ledger P5.9-t
follow-up: port `reduce_outer_joins`' REDUCTIONS as a TYPE downgrade before
`planFromClause`. Ledger P5.9-u follow-up: populate `TimeSubTime`/
`TimeSubTimestampTZ` at their producers, then switch `compareDatum` off the
`Scale != 0` timetz inference.

Nightly triage: `ci/logs/action-items.md` is still run 20260806-011323, 18
items; all subjects already filed under M-NIGHTLY. Nothing new.

In-flight: none.
