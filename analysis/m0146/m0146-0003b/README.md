# M0146-0003b — partial-agg row transport (M0141-S3/S4 of M0146-0003)

Slice: transition-state serialization + row-emitting Partial/Finalize
pair (`Aggregate.PartialEmit`). No producer emits the flag yet — the
node pair is reachable only in directly-constructed plans, so the
expected plan-movement delta is zero at both TPC-DS scales.

Design: docs/design/0100-0149/m0146-0003b-partial-agg-row-transport.md

## Files

- internal/optimizer/plan.go — `Aggregate.PartialEmit` flag, both-node
  pairing contract.
- internal/executor/agg_state_serial.go — name-keyed
  serializeAggRuntime/deserializeAggRuntime bounded by the
  aggregateIsDecomposable whitelist; aggStateFieldBelt refuses
  DISTINCT/string/array/WITHIN GROUP/user state.
- internal/executor/operators_join_agg.go — `emitPartialStateRows`
  (Partial arm: `[keys | passthrough | serialized state per agg]` rows,
  group-key sorted) + `absorbPartialStateRow` (transport-Final arm:
  width/kind check → key lookup → deserialize → same combineAggRuntime
  rules as the accumulator transport).
- internal/executor/parallel_agg_transport_test.go — round-trip pins,
  belt refusal pins, Gather/GatherMerge identity at workers 1/2/4,
  pairing-error pins.

## Gates

- go test ./internal/executor — PartialEmit identity + serialization
  suites PASS; `-race` on the identity test PASS.
- RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh — PASS.
- scripts/tpch-spotcheck.sh — PASS (Q12=2, Q13=33).
- scripts/tpcds-sf025-regression.sh sweep — PASS, 96/96 verdicts;
  plan shapes 99/99 identical to 9fcb9c952 (zero moves, expected:
  dead-in-production).
- ACCEPT_BASELINE=tmp/m0145-0008m/arm-on.txt
  scripts/tpch-acceptance-arm.sh on tmp/arm-on-m0146-0003b.txt — PASS,
  24/24 value-identical.
- scripts/tpcds-fireset-gate.sh m0146-0003b tmp/fireset-m0146-0003b —
  PASS, fires=none at sf025 and sf1; CATEGORIES-EXCL-MATCH identical
  baseline-vs-candidate.

## Deferred inside M0146-0003

- S5: GatherMerge-fed Finalize-Sorted streaming merge-combine
  (current transport-final hash-combines unordered rows — same result,
  not PG's node shape).
- S6: planner producer setting PartialEmit + EXPLAIN rendering of
  serialized-state columns.
