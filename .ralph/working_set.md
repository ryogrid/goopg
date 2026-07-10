(idle — nothing in flight)

## Loop summary (2026-07-11, loop #28)

**Outcome: landed year-month tokenizer quirks `1-` / `1-2.5`
(unimplemented_feat #5(d-iii-rest), the last named year-month deferral).**

- Nightly triage FIRST: `ci/logs/action-items.md` still holds only
  AI-20260710-011513-001 (`make build` fail). Re-confirmed STALE — `make build`
  PASSES at HEAD (exit 0). No new nightly work.
- Feature: `interval '1-'`→1 year (empty month tail = bare year) and
  `interval '1-2.5'`→1 year 2 mons 00:00:00.5 (fractional-seconds run trailing
  the year-month field) now parse; `1-2.5day`→+12:00:00; SIGNED `-1-2.5`/`+1-2.5`
  still error. One file: `internal/parser/interval.go`.
- Design: PG `ParseDateTime` lexes `1-2.5` as DTK_DATE `1-2` + fresh DTK_NUMBER
  `.5`; a SIGNED field is one DTK_TZ token its years-months branch rejects.
  Mirrored: new `splitYearMonthFraction` peels a `.`-led run off an UNSIGNED
  `<digits>-<digits?>` field in `expandIntervalFields`, feeding the remainder
  back through `splitAlphaNumRuns`; `parseYearMonthField` now accepts an empty
  month tail as 0 months (PG strtoint on ""=0). Verified byte-for-byte vs live
  PG 18.3 (scratch initdb on /tmp/pgverify-interval, port 5601, now stopped).
- Tests: `internal/executor/interval_subday_test.go` `TestYearMonthHyphenIntervals`
  +22 accepts / +8 rejects.
- Docs: unimplemented_feat.json #5 STILL DEFERRED clause narrowed (year-month
  quirks removed, replaced with the `1-2h` lone-unit-word deferral); design
  0003-0006 new Follow-up section; README row #73 extended (also backfilled the
  previously-unindexed d-iii/d-iii-rest follow-ups); deferral ledger row appended.
- Gates (all PASS): build/vet clean; parser + full executor suites; tpch-spotcheck
  Q12=2/Q13=33; pgbench smoke via pre-commit hook (on commit).

**Still deferred (interval)** per ledger: bare unit-word glued to a year-month
(`interval '1-2h'`→PG 1 year 2 mons, `h` a no-op — needs DecodeInterval's
right-to-left unit-then-DTK_MONTH-override); full typmod grammar (HOUR TO MINUTE
ranges, SECOND(p), Form-1 trailing-word column-alias); interval ±infinity.

In-flight: none
