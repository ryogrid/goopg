# 04 — Execution plan

Branch `waitevent-impl`. Gates after each phase: `go build ./...`,
`go vet` on touched packages, targeted package tests, and the existing
vacuum/autovacuum/cluster isolation-adjacent suites
(`internal/testport` vacuum-related, `truncate-vacuum-cluster-conflict`,
`vacuum-no-cleanup-lock`, `insert-conflict-specconflict` smoke) must stay
green.

## Phase A — docs (this bundle)

A1 audit matrix (done, 02), A2 design (03), A3 sub-agent review + fixes,
A4 commit `-n` + push.

## Phase B — accounting layer (C2 prerequisite)

B1 pgstat relation store: add `insSinceVacuum` / `modSinceAnalyze`
atomics; increment at insert/update/delete fold sites next to deltaDead.
B2 SQL surface: `pg_stat_get_ins_since_vacuum/mod_since_analyze`;
de-zero the three columns in `pg_stat_user_tables`.
B3 Reset hooks: VACUUM resets dead+ins; ANALYZE resets mod.
Gate: unit tests for counters incl. reset semantics.

## Phase C — scan semantics

C1 VM skip + SkippedAllVisible stat + relfrozenxid guard in both callers
(executor + launcher). C2 Aggressive determination + FREEZE option
execution. C3 freeze cutoff helper (`min(max_age/2)` cap + OldestXmin
clamp) wired to session GUCs and reloption overrides; launcher drops its
hardcoded 50M.

## Phase D — autovacuum engine

D1 GUC registrations (+sample sync). D2 Launcher construction/startup in
server bootstrap gated on `autovacuum`; naptime from GUC; activity
registration as launcher backend type. D3 Trigger formula replacing
needsVacuum/needsAnalyze with reloption overrides + anti-wraparound
ordering/aggressive marking. D4 autoanalyze upgraded to sampled analyzer
via extracted core.

## Phase E — throttling + extras

E1 cost model in vacuumCore (no-op at default delay 0). E2 tail truncation
per F7 (conditional-lock; skip-on-fallback like PG). E3 relallvisible
publish.

## Phase F — verification

F1 unit: VM skip counts, guard blocks advancement when skipped&&
!aggressive, FREEZE zeroes cutoffs & forces full scan, formula thresholds
boundary tests, counter reset tests, truncation honors GUC=off.
F2 regression: vacuum/cluster isolation-adjacent testport set above;
full executor+commands+autovacuum+storage packages.
F3 live (capped throwaway :5533): insert 100k rows → delete half → manual
VACUUM logs/Stats.SkippedAllVisible>0 with warm VM and ==0 after churn;
lower autovacuum_vacuum_scale_factor/threshold via SET to force an
auto pass within seconds and confirm trigger fired from dead-tuple math
(not wall clock); ANALYZE then pg_stats shows MCV/histogram for a skewed
column; EXPLAIN row estimates plausibility unchanged.
F4 update TODO/results into this bundle; final commit `-n` + push.

## Risks

| risk | mitigation |
|---|---|
| VM.AllVisible is horizon-based (xid compare per page) — skipping could miss pages whose visibility changed post-bit-set | DML already clears bits synchronously (audited MATCH); horizon only widens safety |
| Starting autovacuum by default changes timing-sensitive tests | gate behind `autovacuum` GUC default ON but tests may SET off; run full testport vacuum set |
| dead-tuple counters reset semantics vs concurrent transactions | reset at end of our own vacuum pass under the relation lock already held |
| sample-file drift | mechanical test enforces same-commit updates |
| scope creep in analyzer extraction | keep old simplified Analyze intact; new core used only by launcher until proven |
