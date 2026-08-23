Task: M0134-0081 (updatable_views.sql) — sizing-only, PARKED 2026-08-23. No
code fix landed; case stays `failed` (CSV unchanged, 1823-line diff at
ci/logs/20260822-001356/testport/regress-diffs/updatable_views_*.txt).

Files: .ralph/fix_plan.md (M0134-0081 PARKED note, 6-bucket summary),
.ralph/deferral_ledger.md (2026-08-23 M0134-0081 row, full bucket detail +
resume points + PG oracle pointers). No source files touched.

Key symbols: internal/optimizer/view_dml.go (viewAutoUpdatableChain,
viewColumnMap, viewProxyTable — existing root-0025 machinery, confirmed
NOT wired into planMerge at internal/optimizer/planner.go:11529).

Findings (live-repro'd against a throwaway server, /tmp/goopg_uv on 5533):
(A) information_schema.tables/views/columns omit view rows entirely for
is_updatable/is_insertable_into (largest bucket, ~40% of diff) — separate
info_schema virtual-catalog subsystem gap. (B) MERGE INTO a view silently
affects 0 rows + wrong command tag (`SELECT 0` instead of `MERGE 1`) —
planMerge never checks tbl.View, unlike planInsert/planUpdate/planDelete;
same silent-no-op corruption class root-0025 fixed for the other 3 DML
forms, never extended to MERGE. (C) MERGE ... RETURNING old.*/new.* — PG18
feature, same 0%-implemented gap as the already-parked M0134-0063
(returning.sql) Bucket A. (D) ctid (system column) in a view's target list
wrongly rejects the view as non-updatable — viewColumnMap only searches
physical b.Columns. (E) a repeated column ref to the same base ordinal
(`a, b, a AS aa`) breaks resolution of the FIRST alias — viewProxyTable's
one-Name-per-ordinal design lets a later viewOrd overwrite an earlier
alias's Name (confirmed: `UPDATE rw_view16 SET b='x' WHERE a=-2` errors
"column a does not exist"). (F) INSERT ... DEFAULT VALUES via a view
returns 0 rows instead of PG's expected 2 — not yet root-caused.

Next step: per banner, next unparked M0134 task by ID ascending =
M0134-0082 (explain.sql). Alternatively (D) or (E) above are the smallest
CONTAINED buckets and best candidates for a dedicated M0134-0081 resume
slice later, though neither alone flips the case (A/B/C/F remain).

Gates run: make ralph-state-guard PASS (self-repaired a stale
running/completed status mismatch, same pattern as prior loops' clean-exit
marker). No source changes this loop, so no unit/spotcheck/pgbench gates
were run (nothing to regress-test).

Nightly triage: both 20260823-011911 AI items already filed in fix_plan.md
M-NIGHTLY section (verified this loop, no new run posted since) — no
filing action needed.

Delegation: none active.

In-flight: none.
