# R44 step B — fold `date + interval`, and a correction to step A's report

*2026-09-10. Step B landed. **This report also corrects a wrong claim in
`REPORT-stepA.md`** — see §4, which is the most important section here.*

## 1. Result

Q14's restriction, the 11.9× error K83 measured:

| arm | `lineitem` estimate | rendered filter |
|---|---|---|
| pre-R44 | 938,645 | `l_shipdate < ('1995-09-01'::date + '1 month'::interval)` |
| step A | 938,645 | same |
| **step A+B** | **78,680** | **`l_shipdate < '1995-10-01 00:00:00'::timestamp`** |

The rendering now matches PG's own (`'1995-10-01 00:00:00'::timestamp`),
and the estimate is the one the equivalent literal form always produced.

### Parity, measured (base = pre-R44)

| corpus | category | base | A | **A+B** |
|---|---|---|---|---|
| TPC-DS | join-method | 71 | 69 | **66** |
| TPC-DS | parameterisation | 39 | 37 | **35** |
| TPC-DS | aggregation-strategy | 84 | 82 | **82** |
| TPC-DS | scan-type | 73 | 73 | 74 |
| TPC-H | parallelism | 18 | 17 | **17** |
| TPC-H | qual-placement | 6 | 5 | **5** |
| TPC-H | sort-strategy | 13 | 13 | **12** |
| TPC-H | aggregation-strategy | 10 | 11 | **10** |

**Net −10 categories on TPC-DS and −3 on TPC-H** for R44 as a whole.
Match count unmoved (0/99, 2/22), as predicted.

## 2. What landed

`tryFoldTemporalBinaryOp` folds `<date|timestamp literal> ± <interval
literal>` to a timestamp literal, ahead of `tryFoldBinaryOp`'s existing
numeric path. No executor extraction was needed after all: the pieces are
already importable — `parser.ParseIntervalBodyWithDefault` for the interval
body, `time.AddDate` for the month/day carry (the same primitive
`addTimeInterval` uses, so planner and executor cannot disagree), and the
leaf `datetime.FormatTimestamp` for PG-faithful rendering. **This retracts
`DESIGN.md` §5a.2's claim that step B required extracting `addDateTimeInt`
into a leaf package.**

The companion change widens `numericValue`'s `date` arm to accept timestamp
spellings, keeping the day-based scale. Without it the folded literal fails
to parse against a `date` column's histogram and `bucketFraction` falls back
to a flat 0.5 — the fold would land the estimate within half a bucket
rather than on it (`DESIGN.md` §5a.3).

**Volatility:** no `provolatile` index is reachable from the optimizer, so
rather than fold on a guess this handles ONE operator family whose
immutability was checked directly against the live oracle
(`date_pl_interval`, `provolatile='i'`), with both operands literals.
Broadening beyond that still requires generating a real volatility map
(§5a.1, not done).

### The infinity bug the tests caught

The first version folded `interval 'infinity'` arithmetically and produced
`119521-07-18 06:23:01.689343` for `timestamp '2020-01-01' + interval
'infinity'` — **a wrong ANSWER, not a wrong estimate**. The executor
implements ±infinity as sentinels (`addTimeInterval`'s
`IsIntervalNoBegin`/`NoEnd` arms), where the result is the same-signed
infinite timestamp and `infinity − infinity` is an error. This is exactly
the planner-vs-executor divergence K88 predicted, and it was caught by
`TestTimestampIntervalInfinity` / `TestIsFiniteInfinity` /
`TestTimestampSubInfinity` — by the suite, not by review. The fold now
declines on `parser.IntervalNoEnd*` / `IntervalNoBegin*` sentinels.

## 3. Gates

| gate | result |
|---|---|
| optimizer + executor suites | green |
| **TPC-DS SF0.5 sweep** | **PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0** |
| TPC-DS verdicts + row counts vs step A | identical for all 99 |
| TPC-DS plan shape | 12 changed, −4 further categories |
| **TPC-H values, 22 queries** | **identical to pre-R44** (verified server per arm) |
| TPC-H parity | −3 categories vs pre-R44 |

## 4. Correction to `REPORT-stepA.md` (K91) — a measurement-integrity failure

**`REPORT-stepA.md` claims "TPC-H plan text byte-identical". That is
WRONG.** Re-measured with a verified harness, step A changes TPC-H plans
(25 diff lines) and moves TPC-H parity by −1 net (parallelism −1,
qual-placement −1, aggregation-strategy +1).

Two independent harness faults produced that false claim, both mine:

1. **`launch.sh` lost its serving-binary verification.** After `/tmp` was
   cleared between sessions I rewrote the launcher from memory and dropped
   the original's inode check. `<bin> stop -D <dir>` fails when
   `postmaster.pid` is absent or the binary differs, so a **stale server
   kept serving the port** and every "A/B" ran against one binary. The
   step-A A/B also named `tmp/goopg-base`, which **never existed** — so
   both arms were literally the same server.
2. **`capture-tpch.sh` reads `/tmp/parity-r0/queries/tpch`, which was
   wiped.** Every capture since produced a 4-line stub ending
   `(capture failed)`. Diffing two stubs reports **"identical"** — a
   failure that reads exactly like a clean result.

Both faults have the same signature: *the null result and the broken
result are indistinguishable*. This is the same class as K90's
`===== Qn =====` parse failure returning `queries=0 match=0`.

**What survives unchanged:** step A's TPC-DS numbers (31 plans, −6
categories, −4.9 % runtime) came from `tpcds-sf05-regression.sh`, which
manages and fingerprints its own binary — those were never affected. And
step A's *values* claim re-verifies as correct.

Fixes applied: `launch.sh` now kills the port holder by PID and **refuses
to proceed unless `/proc/<pid>/exe` matches the requested binary's inode**;
the TPC-H query corpus was restored from `tmp/take4/` into
`/tmp/parity-r0/queries/tpch`.

**Rule for this workstream: a measurement harness must fail loudly, and any
A/B must prove which binary answered.** Where it cannot, the result is not
evidence.

## 5. What R44 still does not do

- No `provolatile` map, so the fold remains narrowly scoped (§2).
- `evalArith`'s numeric path is still float64, so `0.05 + 0.01` still folds
  to `0.060000000000000005` where PG gives `0.06` (K88). Unchanged by this
  round and still filed as its own defect — and still a live `rendering`
  divergence.
- Match count unmoved. Q14 now differs from PG only on `parallelism`
  (R43's territory), which is where its remaining gap lives.
