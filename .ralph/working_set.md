(idle — nothing in flight)

## Loop summary (2026-07-11, loop #20)

**Outcome: landed coarse interval units week/decade/century/millennium/
microsecond (unimplemented_feat #5(c)) end-to-end. Byte-for-byte verified vs
real PG 18.3. All gates green.**

- Nightly triage: `ci/logs/action-items.md` AI-20260710-011513-001 (build fail)
  already resolved; `make build` clean. No new nightly work.
- Task: interval deferred item (c) — coarse unit names.
- Change confined to the two pure helpers in `internal/parser/interval.go` that
  already own interval-body decoding (NO new parser path, NO select.go/executor
  edit): `canonicalIntervalUnit` gained spellings (week(s), decade(s)/dec/decs,
  century/centuries/cent, millennium/millennia/mil/mils, microsecond(s)/us/usec/
  usecs/usecond/useconds); `IntervalUnitToParts` gained scale/spill cases
  mirroring PG DecodeInterval: week=AdjustDays(7)+AdjustFractDays(7);
  decade/century/millennium=AdjustYears(10/100/1000)+AdjustFractYears
  (→120/1200/12000 mo, rint→RoundToEven); microsecond=AdjustMicroseconds(1).
  Key insight: PG parses a TRAILING `WEEK`/`DECADE` token as a column ALIAS over
  bare `interval '3'` (=3s), NOT a typmod field (verified live) — so select.go
  Form-1 switch deliberately untouched; units reach helper only via embedded /
  ::interval bodies through ParseIntervalBody.
- Files: internal/parser/interval.go, interval_subday_test.go
  (+TestWeekDecadeCenturyIntervals, +coarse rows in sibling guard),
  docs/design/0003-0006-* (new Follow-up), deferral_ledger.md,
  unimplemented_feat.json (#5 code_audit follow-up note).
- Gates (all PASS): build/vet clean; parser + executor full suites;
  tpch-spotcheck Q12=2/Q13=33; pgbench smoke (pre-commit hook).

**Still deferred** (ledger 2026-07-11): (d-i) bare-number default-unit
(`interval '5'`→seconds); (d-ii) single-letter units (w/c/h/m/s/d/y, ambiguous
m); (d-iii) full interval typmod grammar (`HOUR TO MINUTE` ranges, `SECOND(p)`).
Next natural: (d-i)+(d-iii) default-unit + typmod grammar together, OR continue
unimplemented_feat survey.

In-flight: none
