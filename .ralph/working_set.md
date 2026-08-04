Task: M0127-P5.6-g — `eqjoinsel_semi`'s MCV arm + `(1 - nullfrac1)`. Code is
DONE, documented and committed; ONE gate is carried (see In-flight).

Files: internal/planner/cardinality.go (`semiPairMatchFraction`,
`clampProbability`, `keyColumnStats`, `rightExprStats`,
`columnStatsForChildBase`); joinkeyproof.go (`baseColumnRef.stats`);
cardinality_semimcv_test.go (13 tests, new).

Three things the next loop should NOT re-derive:

1. **PG 18.3 estimates `rows=1` for Q21's anti-join too**, against the same
   actual 4003 — measured on the reference cluster, not inferred.
   `neqjoinsel` returns `1 - nullfrac` for SEMI/ANTI by design. Q21 is an
   AUDIT-OVERRIDE task (P5.6-g-iii), not an estimator one. And PG's Q18
   estimate is 1674× over, which trips this audit's own 1000× tripwire — the
   absolute factor is the wrong bar for a PG-parity milestone.
2. **Q18's SEMI est=2 998 620 is the 0.5 punt** (exactly half the outer). Cause:
   `resolveBaseColumn` has no `*HashAggregate` arm → nd2 unknown, and the
   inner's 1 210 559-row estimate is far above `defaultNumDistinct` so the
   clamp never fires. The arm ALONE measures worse (4.84 M); it needs Q18's
   dedup-to-INNER shape with it (P5.6-g-ii).
3. **The audit's noise floor is ±5 %** — two runs re-ANALYZE and resample, so
   INNER joins this change cannot reach moved by up to 5 % in both directions.
   A SEMI/ANTI delta under that is not evidence.

Evidence: `analysis/leftdeep-joins/2026-08-05-p56g.txt` + `-README.md` (§2 has
the two real-data probes that separate "no-op" from "broken wire": 20 → 5 010
vs actual 5 010 with an inner MCV list; 1 000 → 750 vs actual 750 at 25 %
nulls).

Next step: per the banner, the next M0127 item. **M0127-P5.6-g-i (the carried
DS05 run) should go first** — it is one command and it gates nothing else.
P5.6-f-iii is still open and still needs two sweeps, not a code fix.

Gates run: build + vet + gofmt-clean; planner `go test` PASS (13 new);
UNITS PASS (`/tmp/units_p56g.log`); SPOT PASS Q12=2 Q13=35
(`/tmp/spot_p56g.log`); estimate audit run (violations 2 → 2, bit-identical —
the intended reading, see above); PG 18.3 oracle cross-check on Q18/Q21;
pgbench smoke via the commit hook.

Nightly triage: the same 17 `AI-20260804-005028-*` subjects, all already filed
under M-NIGHTLY. Nothing new to file. Live observation worth keeping: the
2026-08-05 batch wedged in `testport` (hash_index timeout → cluster restart →
no log output for 100+ min) with two orphaned `goopg` processes at 335 % and
80 % CPU — that is AI-...-017 `suite-wedge` reproducing, and it is what blocked
DS05 below.

In-flight: `scripts/tpcds-sf05-regression.sh sweep` NEVER STARTED — it exits
rc=1 immediately with `FATAL: the nightly CI batch is running (ci/batch) — its
TPC-H stage saturates CPU/RAM for hours and would contaminate these timings
(FORCE=1 to override)`. No PID, no partial log. Re-run it (filed as
M0127-P5.6-g-i) once `pgrep -f ci/batch/run-nightly` is empty; expected
PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1 SKIP=4. It matters for THIS
change because TPC-DS has nullable join keys where TPC-H has none.
