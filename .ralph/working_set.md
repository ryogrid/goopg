(idle — nothing in flight)

## Loop summary (2026-07-11, loop #27)

**Outcome: landed ISO 8601 duration interval bodies
(unimplemented_feat #5(d-iii-rest) ISO half).**

- Nightly triage FIRST: `ci/logs/action-items.md` still holds only
  AI-20260710-011513-001 (`make build` fail). Re-confirmed STALE — `make build`
  PASSES at HEAD (exit 0). No new nightly work.
- Feature: `interval 'P1Y2M3DT4H5M6S'`/`'PT1H30M'`/`'P1.5D'`/`'P1W'`/`'P-1Y'`,
  alternative format extended `'P0002-06-07T01:30:00'` + basic packed
  `'P00020607T013000'`, and fall-throughs (`'PT'`→00:00:00, `'P1'`→1 year,
  `'P1DT'`→1 day) now parse; malformed bodies (`P`, `PX`, `P1Q`, `PT1X`, `P1Y2`,
  `P1YY`) error. One file: `internal/parser/interval.go`.
- Design: PG `interval_in` tries free-form `DecodeInterval` first, ISO only on
  `DTERR_BAD_FORMAT`. Mirrored exactly: old `ParseIntervalBody` body renamed
  `decodeIntervalFields`; `ParseIntervalBody` calls it first, falls back to new
  `decodeISO8601Interval`. ISO body passed UNTRIMMED (PG `str[0]!='P'` leading-
  space rejection). `decodeISO8601Interval` line-ports `DecodeISO8601Interval`:
  datepart/havefield state machine, Y/M/W/D before T + H/M/S after (M=months
  before T, minutes after). Every designator + extended-alt field routes through
  the SHARED `IntervalUnitToParts` (no sibling drift); only 8-digit/6-digit basic
  packed formats use inline math. New `scanISONumberPrefix`/`parseISO8601Number`/
  `iso8601IntegerWidth`/`clampISOInterval` mirror PG helpers.
- Tests: `internal/executor/interval_subday_test.go` new
  `TestISO8601IntervalLiterals` (30 accepts + 6 rejects, byte-for-byte vs live
  PG 18.3 port 5599).
- Docs: unimplemented_feat.json #5 STILL DEFERRED clause narrowed (ISO removed);
  design 0003-0006 new Follow-up section; deferral ledger row appended.
- Gates (all PASS): build/vet clean; parser + executor interval suites;
  tpch-spotcheck Q12=2/Q13=33; pgbench smoke via pre-commit hook (on commit).

**Still deferred (interval)** per ledger: (d-iii-rest) full typmod grammar
(HOUR TO MINUTE ranges, SECOND(p), Form-1 trailing-word column-alias); interval
±infinity (needs DTK_LATE/DTK_EARLY + INTERVAL_NOBEGIN/NOEND carrier); year-month
tokenizer quirks `1-2.5` (PG→1y 2mon 0.5s) and `1-` (PG→1 year) in the free-form
decoder — need a finer hyphen-field lexer that risks the `1-2` year-month path.

In-flight: none
