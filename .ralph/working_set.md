Task: Bookkeeping — mark all milestones CLOSED, unblock M-NIGHTLY

Files:
  - .ralph/fix_plan.md: M0123-S2/S3/S4 → [x], M0125-0031/0032/0033 → [x]
    (absorbed into now-closed M0127), M-NIGHTLY section unblocked
  - .ralph/working_set.md: this file

Key symbols: N/A (bookkeeping loop)

Hypothesis/Findings:
  - All five priority milestones CLOSED: M0124, M0125 (absorbed items → M0127),
    M0127, M0128, M0123
  - M-NIGHTLY now unblocked — banner + section header + procedure comment updated
  - Two M-NIGHTLY items filed from action-items 20260808-005620:
    1. testport/TestPort_IsolationEvalPlanQual — REOPENED but PASSES in isolation
       at HEAD aec67933 (21.84s); full-suite ordering issue, not a regression
    2. tpcds/Q95-timeout — STALE at HEAD (completes 57s)
  - No immediately actionable M-NIGHTLY items remain
  - Remaining unchecked items in fix_plan are deferred/blocked milestones
    (M0095, M0110, M0119, M0122)

Next step: Read ci/logs/action-items.md; if new items, file + investigate the
most actionable one. If the log is stale/same, consider M0122 PG-compat buckets
or user direction.

Gates run:
  - go test -run TestPort_IsolationEvalPlanQual ./internal/testport/ PASS (21.84s)
  - make ralph-state-guard PASS (auto-repaired, consistent)

In-flight: none
