(idle — nothing in flight)

## Loop summary (2026-07-11, loop #51)

**Nightly triage:** action-items batch `20260711-011536` — all 3 AI items
(IsolationTimeouts, TuplelockUpgradeNoDeadlock, PgWaldumpVacuumPruneRoundtrip)
already `[x]` in fix_plan.md (co-load timing flakes). No new work.

**Task — closed the LAST deferred infinity item: `timestamp 'infinity'`
literal input + `::timestamp` cast + binary wire codec.** The ±infinity
timestamp sentinel (KindTime `Int` = math.MaxInt64/MinInt64) was previously
reachable ONLY via arithmetic short-circuits, never from typed text. New
choke-point helper `parseTimestampInfinityLiteral(s)(Datum,bool)`
(`internal/executor/copy_text.go`) recognises PG's three RESERV spellings —
`infinity`/`+infinity` (DTK_LATE), `-infinity` (DTK_EARLY), case-insensitive/
trimmed (`datetime.c` datetbl) — and returns the existing `NewTimestampInfinity`
sentinel. Wired before the ordinary parse at 5 sites:
- `evalTypedStringLit` timestamp/timestamptz case (`expr.go`, not time-cached)
- `evalCast` timestamp/timestamptz case (`expr.go`)
- codec `encodeValuePG` → PG `timestamp_send` DT_NOEND/NOBEGIN = INT64_MAX/MIN
  micros (switch bypasses UnixMicro−epoch); `decodePhysicalPGValueMctx`
  intercepts those micro values back to the sentinel before the overflow-prone
  epoch add (`codec.go`)
- `pg_input_is_valid('infinity','timestamp')` (`expr.go`)
Output (Format/AppendValueText) + ordering (compareDatum KindTime) already
correct from the carrier loop. All 14 `want` byte-for-byte vs live PG 18.3
(socket /tmp:5599, verified this loop). Tests:
`internal/executor/timestamp_infinity_literal_test.go` — `TestTimestampInfinityLiteral`
(17 cases) + `TestTimestampInfinityWireCodec` (binary round-trip).

Gates: build clean; full executor suite PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); pgbench smoke via pre-commit hook; ralph-state-guard OK.

**With this the whole #5(d-iv) interval/infinity group is COMPLETE.**

**Newly deferred (own slice, ledger row appended):** `date 'infinity'` /
`'infinity'::date` literal input — date uses a DIFFERENT sentinel domain
(DATEVAL_NOEND/NOBEGIN = INT32_MAX/MIN days, not timestamp's INT64-ns), so
`evalTypedStringLit`/`evalCast` `date` case + date codec each need an analogous
INT32 carrier. Next loop: pick that or a new milestone (e.g. M0122-0008 Auth).

In-flight: none
