(idle — nothing in flight)

M0132-S11 (perf acceptance) MEASURED + recorded + committed. Verdict:
- Criterion 3 (fsync) MET — 0.32 fsync/txn (≈3 txn/group commit); the 2-fsync bug
  is gone, commit is on END.
- Criteria 1-2 NOT MET — same-day same-HEAD A/B (m0132s11_{prep,simple}_317fb002):
  prepared `-N` 8,781 vs simple 10,158 (−13.6%); prepared `-S` 72,857 vs simple
  93,738 (−22.3%). Root cause (O-XP-1 profile): NO prepared-statement cache —
  re-parse per Execute (`dispatch_extended.go:40`) + re-parse/re-plan per Describe
  (`extended.go:686` describeViaPlanner). NOT an M0132 regression.
- Recorded: `analysis/perf-optimize3/runs/m0132s11_prep_317fb002/S11-RESULTS.md`,
  ledger row 2026-08-13 M0132-S11, doc-09 §S11 "Measured" note, S13 note in
  fix_plan (its "prepared > simple" gate blocked on the cache).

Next per banner: M0132-S12 (PREPARE/COMMIT PREPARED/ROLLBACK PREPARED +
LISTEN/NOTIFY/UNLISTEN over the extended protocol). S13's A/B half is blocked on
the statement-cache follow-up; its verification half (prepared path correctness)
is unblocked.
