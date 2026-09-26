# M0146-0003c — GatherMerge-fed Finalize-Sorted (M0141-S5 of M0146-0003)

Slice: the sorted-armed row-transport finalize. `Strategy ==
AggStrategySorted` on a `Final + PartialEmit` node consumes a
merge-ordered serialized-state stream — PG's `Finalize GroupAggregate`
over `Gather Merge -> Sort -> Partial HashAggregate` — folding same-key
runs into ONE live group via `combineAggRuntime`, no group map. An
out-of-order key trips a belt error rather than emitting a group twice.

Design: docs/design/0100-0149/m0146-0003c-finalize-sorted-transport.md

## Files

- internal/executor/operators_join_agg.go — `decodePartialStateRow`
  (shared validator extracted from `absorbPartialStateRow`),
  `openSortedPartialTransport` (streaming fold + order belt), Open gate.
- internal/executor/parallel_agg_transport_test.go — `sortedFinal` arm
  through runTransportSplit/checkTransportIdentity;
  `TestPartialEmitSortedIdentity` (GatherMerge identity, positional
  compare, workers 1/2/4); `TestPartialEmitSortedRejectsUnsorted`
  (belt pin).
- internal/optimizer/plan.go — PartialEmit comment extended for the
  sorted arm.

## Gates

- go test ./internal/executor ./internal/optimizer — PASS.
- go test -race -run 'TestPartialEmit(Sorted)?' — PASS.
- RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh — PASS.
- scripts/tpch-spotcheck.sh — PASS (Q12=2, Q13=33).
- FORCE=1 scripts/tpcds-sf025-regression.sh sweep — PASS, 96/96
  verdicts, 99/99 plan shapes identical. NOTE: ran during the nightly
  CI batch — timings void (non-blocking status-delta +18.3% is
  contention noise), verdicts/checksums real.
- FORCE=1 ACCEPT_BASELINE=tmp/m0145-0008m/arm-on.txt
  scripts/tpch-acceptance-arm.sh on tmp/arm-on-m0146-0003c.txt — PASS,
  24/24 value-identical.
- scripts/tpcds-fireset-gate.sh m0146-0003c tmp/fireset-m0146-0003c —
  PASS, fires=none at sf025 and sf1; CATEGORIES-EXCL-MATCH identical
  baseline-vs-candidate.
- scripts/jointree-parity-capture.sh tpch (PLAN_ONLY — exempt from the
  nightly refusal): CATEGORIES-EXCL-MATCH identical to 104c2c90b;
  tpch-diff.txt / tpch-class.txt copied here.

## Deferred inside M0146-0003

- S6: planner producer setting PartialEmit (+Strategy on the finalize)
  and EXPLAIN rendering of serialized-state columns / strategy labels.
- Partial-GroupAggregate (AGG_SORTED partial — no Sort under the
  merge): the partial arm still hashes internally and sorts its
  output; only the finalize gained a sorted mode.
