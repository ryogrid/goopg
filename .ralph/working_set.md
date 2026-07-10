(idle — nothing in flight)

## Loop summary (2026-07-11, loop #25)

**Outcome: landed single-letter interval unit forms (unimplemented_feat
#5(d-ii)). `interval '1 y/c/w/d/h/m/s'` now parse end-to-end.**

- Nightly triage FIRST: `ci/logs/action-items.md` still holds only
  AI-20260710-011513-001 (`make build` fail, run 20260710-011513). Already
  triaged stale last loop (fixed by abbf7de1, checked off fix_plan.md:6123);
  re-confirmed `make build` PASSES at HEAD. No new nightly work.
- Feature: seven `deltatktbl` single-letter keys (`y c w d h m s`) appended to
  `canonicalIntervalUnit` (internal/parser/interval.go) — the shared choke point
  for ParseIntervalBody, so both sibling paths (parser Form-2 + executor
  `::interval`/CAST) gained them with no select.go/executor edit.
- KEY PG-fidelity discovery (refuted prior loops' "positional m" deferral note):
  read `DecodeInterval` — `m` is UNAMBIGUOUSLY MINUTE in an interval literal
  (decoded via DecodeUnits→deltatktbl, not the absolute-date datetktbl). So
  `interval '1 m'`=00:01:00, `interval '1 y 2 m'`=1 year 00:02:00 (2 minutes).
  `quarter`/`qtr`/`tz`/`timezone` ARE in deltatktbl but have no case in
  DecodeInterval's per-unit switch → PG 22007; goopg rejects them by not adding
  them. All verified byte-for-byte vs live PG 18.3 (existing instance port 5599,
  postgres@postgres, read-only SELECTs).
- Tests: 3 blocks in TestWeekDecadeCenturyIntervals (accepts + m=minute beside
  YEAR + cast siblings) + 4 reject rows in TestIntervalCastFromStringInvalidSyntax.
- Docs: unimplemented_feat.json #5 code_audit surgical-edited (d-ii landed);
  design 0003-0006 new follow-up section; README index row extended; deferral
  ledger row appended.
- Gates (all PASS): build/vet clean; executor + parser suites; tpch-spotcheck
  Q12=2/Q13=33; pgbench smoke via pre-commit hook (on commit).

**Still open (interval)** per ledger: (d-iii) full interval typmod grammar
(HOUR TO MINUTE ranges, SECOND(p) precision, Form-1 trailing-word column-alias
fall-through); field-mask collision cases (`1-2 3 mons`/`1 mon 2 mons` repeated
MONTH bit summed) + tokenizer quirks (glued `1m`, `1-2.5`, `1-`, lone-type-hint
`1-2 days`) — needs a combined DecodeInterval tokenizer+fmask port. interval
±infinity is a larger engine-wide feature.

In-flight: none
