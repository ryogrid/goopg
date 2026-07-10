(idle — nothing in flight)

## Loop summary (2026-07-11, loop #22)

**Outcome: completed justify_hours/justify_interval sub-day folding
(unimplemented_feat m0097-0004) — a stale sibling of the 2026-07-11 interval
carrier change. Byte-for-byte verified vs real PG 18.3. All gates green.**

- Discovery: the justify functions were implemented at M0097-0004 BEFORE the
  KindInterval carrier gained a sub-day micros field. Their doc comments/code
  asserted "the interval has no time field — always zero", so justify_hours was
  the identity and justify_interval collapsed to justify_days. That premise
  expired 2026-07-11 (Datum.IntervalMicrosValue + sub-day literals), leaving
  justify_hours/justify_interval silently wrong for the time field.
- Fix (internal/executor/expr.go): renamed evalJustifyInterval → evalJustify,
  dispatching all three over month/day/micros: justifyIntervalHours (TMODULO
  micros by usecsPerDay→days + sign-fix), justifyIntervalFull (pre-justify days
  + fold 24h→days + fold 30d→months + cross-field sign equalization), justify_days
  unchanged (justifyIntervalDays, time untouched). int32 overflow → 22008
  errIntervalRange via addDayS32 (mirrors pg_add_s32_overflow).
- Files: internal/executor/expr.go (dispatch + 3 helpers + errIntervalRange),
  interval_justify_test.go (+12 PG-verified sub-day rows), unimplemented_feat.json
  (m0097-0004 code_audit → RESOLVED), docs/design/0003-0006-* (new Update
  subsection), deferral_ledger.md (infinite-interval deferral row).
- Gates (all PASS): build/vet clean; full executor suite; tpch-spotcheck
  Q12=2/Q13=33; pgbench smoke (pre-commit hook).

**Still deferred** (ledger 2026-07-11): interval ±infinity is unmodeled
engine-wide (no INTERVAL_NOT_FINITE; carrier can't represent it) — a distinct
larger feature (literal parse + output + sentinel encoding + short-circuits
across all interval operators). Justify is now correct for all FINITE intervals
(every interval goopg can currently construct). Also open from prior loops:
interval year-month hyphen (`'1-2'`), (d-ii) single-letter units, (d-iii) full
interval typmod grammar.

In-flight: none
