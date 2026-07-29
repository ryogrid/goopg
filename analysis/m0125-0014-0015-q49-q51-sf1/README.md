# M0125-0014 / M0125-0015 — Q49 and Q51 re-measured at SF=1

**Date:** 2026-07-30 · **HEAD:** `f3f31d87` (branch `tpcds-fix2`) ·
**Verdict: both CLOSE as *measured-and-already-fixed*.**

Both tasks were specified as "re-measure at SF=1, **then** resolve or classify"
(the M0124-0004 shape). STEP 0 of each — the re-measurement — is what this
directory records, and STEP 0 is where both of them end: goopg now matches PG on
**rows and values** at SF=1 for both queries, so the diagnoses drafted for the
"if the gap survives" branch are moot and were not pursued.

## Result

| query | goopg @ `f3f31d87` | PG 18.3 | prior SF=1 reading (`analysis/tpcds-sf1-goopg-20260728.md` §3 rows 7–8) | verdict |
|---|---|---|---|---|
| **Q49** | `OK 83 s / 34 rows`, `ck=63ace0d888e86982` | `OK <1 s / 34 rows`, `ck=63ace0d888e86982` | `OK 79 s / 30` vs PG 34 → **MISMATCH** | **PASS by value** |
| **Q51** | `OK 47 s / 100 rows`, `ck=443e242cfab22c02` | `OK 1 s / 100 rows`, `ck=443e242cfab22c02` | `OK 587 s / 0` vs PG 100 → **MISMATCH** | **PASS by value** |

`scripts/tpcds-value-diff.py` on the same two pairs:

- `=== Q49 : RENDERING-ONLY (numeric scale)` — the `0.00` vs `0.00000000000000000000`
  quotient-scale gap, not an answer difference.
- `=== Q51 : RENDERING-ONLY (bpchar/width)` — goopg's un-blank-padded `char(n)`
  (ledger row 2026-07-06, M0122-0005), not an answer difference.

Nothing survives pass 2 in either query, unsorted or sorted, and the two
independently-computed checksums are equal. That is the full acceptance
criterion both fix_plan items state ("34 / 100 rows = PG **and values equal PG at
SF=1**").

## Method

Host was quiet: no other `goopg` process, `ci/batch/run-nightly.sh` not running,
load average 1.01. Clusters started with `bench/tpcds/server.sh start sf1` / `pg`
(goopg through the cgroup cap, `GOGC=off`, `GOMEMLIMIT=12GiB`), and stopped
after.

The psql invocation is byte-identical to the one `scripts/tpcds-bench.sh` uses
for a sweep cell, so these numbers are comparable to the baseline they replace:

```
psql -h 127.0.0.1 -p 65436 -U postgres -d postgres \
     -c "SET max_parallel_workers_per_gather = 4;" -f .../query{49,51}.sql   # goopg SF=1
psql -h 127.0.0.1 -p 65438 -U ryo      -d tpcds    -f .../query{49,51}.sql   # PG 18.3 SF=1
```

**The server was restarted between Q49 and Q51.** Q51's runtime is part of its
acceptance criterion, and a `GOGC=off` server that has just run an 83 s query
sits near `GOMEMLIMIT` and thrashes — the "sweep-tail collapse" confound
(CLAUDE.md, benchmark-timing hygiene). Both timings are therefore S-cold, which
is *more* favourable than the baseline's condition (Q51's 587 s was measured
~50 queries into a sweep); see the caveat below before reading the speedup as a
pure engine win.

Budget: Q51 was given 2400 s rather than the sweep's 600 s, per the fix_plan's
instruction to raise the budget for the acceptance run rather than let a
marginal cell read as a regression. It did not need it.

## Two things this measurement changes beyond the two verdicts

1. **Q51 is no longer budget-marginal.** It held the narrowest `OK` margin in the
   SF=1 sweep — 13 s under the 600 s cut. At 47 s it now has 553 s of headroom,
   so the standing warning that "any fix that adds work can flip it to TIMEOUT
   and mask a correct row count" is retired for Q51. Q82 (`OK 556 s`, 44 s) is
   again the narrowest margin, which is what `RESULTS.md` said before §5
   contradicted it.
2. **The 587 s → 47 s drop (12.5×) is almost certainly not a speed fix.** The
   leading explanation is that the old answer was *empty*: with zero qualifying
   rows the `LIMIT 100` could never saturate, so the full-outer-join and window
   layers had to be drained to completion; with 100 rows it fills early. That is
   consistent but **unverified** — nothing here measures it, and it is not
   claimed as a finding.

## Attribution — what is measured and what is inherited

The SF0.5 bisect on record attributes both flips to **M0125-0009**: Q49 went
`MISMATCH 24/25` → `PASS 25` and Q51 `MISMATCH 0/100` → `PASS 100` between
`sweep-20260729-004730` (`7a7a2639`) and `sweep-20260729-033758` (`3fbce36a`),
and both still PASS at HEAD.

**This SF=1 run does not, by itself, confirm that attribution.** It is a single
reading at `f3f31d87`, which contains -0009 *and* everything after it
(-0010/-0011/-0012/-0020 among others). Separating them would need the same two
arms rebuilt and re-run at SF=1; that was not done, and no claim beyond "fixed
somewhere at or before `f3f31d87`, with SF0.5 evidence pointing at M0125-0009"
is made here or in the ledger.

## Files

| file | what |
|---|---|
| `goopg_q49.txt` / `pg_q49.txt` | raw psql output, both engines (`*_result.txt` are the copies `tpcds-value-diff.py` expects by name) |
| `goopg_q51.txt` / `pg_q51.txt` | same for Q51 |

## Follow-on filed, not worked

While adding the Q49/Q51 rows to `ci/batch/tpcds-row-anchors.csv` (the closing
step both items specify) the anchor mechanism turned out to be **dead**:
`ci/batch/lib/summarize.py:485` reads `r["rows"]`, but the TPC-DS anchor CSV's
column is `expected_rows`, so the dict comprehension drops all 61 rows and
`anchors_tpcds` is always empty. TPC-H is unaffected — its CSV really does use
`rows`. Filed as an M-NIGHTLY item; not fixed here, because M-NIGHTLY is parked
below M0125 by the banner and this is not one of the two carve-outs. **The Q49
and Q51 anchors added by this loop are therefore inert until that item lands.**
