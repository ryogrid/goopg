# Working Set (carried from loop killed by usage limit, 2026-06-11 06:33)

Task: M0100-0011 — EvalPlanQual trigger / BEFORE-trigger inline firing
  (design doc draft exists untracked: docs/design/0100-0011-evalplanqual-trigger-before-trigger-inline-firing.md)

Files (UNCOMMITTED edits in tree — review `git diff` before continuing):
- internal/executor/operators_upsert.go — added AFTER INSERT + BEFORE/AFTER UPDATE
  trigger firing in upsertOp ON CONFLICT paths
- internal/executor/operators_storage.go — Phase 1 inline EPQ changes (large diff)
- internal/server/dispatch.go — bpchar output fix: PG `bpcharout` trims trailing
  spaces (bcTruelen); goopg must NOT re-pad on output. A previously-failing char(1)
  test on this branch now passes.
- internal/executor/plpgsql_runtime.go, internal/plpgsql/parser.go — minor

Key symbols: upsertOp, applyUpdate, tryApplyHOTUpdate, normalizeIsoOutput,
  IsolationRunner output formatting

Hypothesis/Findings:
- Isolation test for eval-plan-qual-trigger improved from 15 → 5 remaining diff lines.
- Remaining 2 issues:
  1. NOTICE ordering — all 4 NOTICEs (key-a t, upk t, key-b f, BEFORE trigger) emit
     PRE-WAIT; Phase 1 inline EPQ runs through without blocking, so NOTICEs appear
     before the step delimiter instead of after s2 wakes.
  2. Serialization error bug — when s1 ROLLBACKs, s2 incorrectly gets "could not
     serialize access" instead of completing the update. Was reading
     tryApplyHOTUpdate Phase 2 concurrent-xmax handling when cut off.

Next step: read tryApplyHOTUpdate / Phase 1 inline EPQ in operators_storage.go to make
EPQ block (or re-check) on in-progress xmax so the s1-ROLLBACK case retries instead of
raising a serialization error under READ COMMITTED.

Gates run: go build OK; modified-package unit tests PASS; isolation test diff at 5
lines (not yet green). Pre-commit suite NOT yet run; Q12/Q13 spot-check NOT run.
