Task: M0122-0003 — Add GENERIC_PLAN, WAL, MEMORY EXPLAIN options to parser

Files:
  - internal/parser/ast.go: added GenericPlan, Wal, Memory fields to
    ExplainOptions + ExplainOptionsSet
  - internal/parser/parser.go: added cases for "generic_plan", "wal",
    "memory" in parseExplainOneOption
  - internal/parser/explain_options_test.go: updated AllOptions test,
    added TestParseExplainGenericPlan/Wal/Memory
  - internal/executor/operators_explain.go: GENERIC_PLAN notice when
    no generic plan is available (matches PG behavior)

Key symbols: ExplainOptions, ExplainOptionsSet, parseExplainOneOption,
explainOp.Open

Hypothesis/Findings:
  - PG 18 supports GENERIC_PLAN, WAL, and MEMORY EXPLAIN options that
    goopg rejected as "unknown EXPLAIN option"
  - Parser change is the minimum viable improvement — tools that use
    these options (e.g. EXPLAIN (GENERIC_PLAN)) no longer error
  - GENERIC_PLAN gets a NOTICE "generic plan is not available" matching
    PG's behavior when no plan cache entry exists
  - WAL and MEMORY are parsed but produce no extra output yet —
    executor tracking for WAL record counts and per-node memory is
    deferred to follow-up loops

Next step: Consider next M0122-0003 subtask (EXPLAIN WAL record
tracking or per-node MEMORY reporting), or triage next M-NIGHTLY run

Gates run:
  - go test -run TestParseExplain ./internal/parser/ PASS (18 tests)
  - go test -run TestExplain ./internal/executor/ PASS
  - go build ./... clean
  - RALPH_PRECOMMIT_SCOPE=smoke PASS (0 failed, 3 workloads)
  - make ralph-state-guard PASS (auto-repaired, consistent)

In-flight: none
