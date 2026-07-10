(idle — nothing in flight)

## Loop summary (2026-07-11, loop #26)

**Outcome: landed glued `<magnitude><unit>` interval forms + field-mask
collision rejection (unimplemented_feat #5(d-iii) glued/collision half).**

- Nightly triage FIRST: `ci/logs/action-items.md` still holds only
  AI-20260710-011513-001 (`make build` fail). Re-confirmed STALE — `make build`
  PASSES at HEAD (abbf7de1 already fixed it). No new nightly work.
- Feature: `interval '2h30m'`/`'1.5h'`/`'1y2mon3d'`/`'-5h'`/`'1day'` now parse;
  collisions now error (`1h2h`, `1 mon 2 mons`, `1-2 3 mons`, `1h 05:00:00`,
  `1.5 sec 200 ms`). Two changes in `internal/parser/interval.go`, both feeding
  the shared `ParseIntervalBody` (parser Form-2 + executor `::interval`/CAST):
  1. `expandIntervalFields`/`splitAlphaNumRuns` reproduce PG `ParseDateTime`'s
     digit↔letter field split, incl. the `datetktbl` gobble rule (`inDatetktbl`):
     a multi-letter unit glued before a digit parses only if it's an absolute
     datetktbl token (d/h/m/s/y/mon/dec) → `1d2h` OK, `1day2h`/`1w2d` error.
  2. `intervalFieldMask` replaces `secondsOccupied bool`, mirroring
     DecodeInterval `tmask & fmask` (`intervalUnitMask`; time word = imTime;
     year-month = imMonth; fractional second widens to imAllSecs).
- KEY PG-fidelity finding: the accept/reject boundary for glued units is the
  ABSOLUTE `datetktbl` (postgres/src/backend/utils/adt/datetime.c ParseDateTime
  lines 886-917), NOT the interval `deltatktbl`. All 40 cases verified
  byte-for-byte vs live PG 18.3 (port 5599, postgres@postgres).
- Tests: `internal/executor/interval_subday_test.go` new `TestGluedIntervalLiterals`.
- Docs: unimplemented_feat.json #5 STILL DEFERRED clause narrowed; design
  0003-0006 new Follow-up section; deferral ledger row appended. (fix_plan.md NOT
  edited — driver-churn memory; state carried in ledger + this file.)
- Gates (all PASS): build/vet clean; parser + executor interval suites;
  tpch-spotcheck Q12=2/Q13=33; pgbench smoke via pre-commit hook (on commit).

**Still deferred (interval)** per ledger: (d-iii-rest) full typmod grammar
(HOUR TO MINUTE ranges, SECOND(p), Form-1 trailing-word column-alias); ISO-8601
duration bodies (`P1Y2M`); interval ±infinity; year-month tokenizer quirks
`1-2.5` (PG→1y 2mon 0.5s) and `1-` (PG→1 year) — need a finer hyphen-field lexer
that risks the `1-2` year-month path.

In-flight: none
