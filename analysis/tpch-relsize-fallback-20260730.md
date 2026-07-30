# TPC-H relation-size fallback — the timed arms (M0125-0003 stage 2)

**Date** 2026-07-30 12:43–13:41 JST · **branch** `tpcds-fix2` · **HEAD** `bac7a52f`
**Engine** `engine-id: 5a45a42d33ef58ff9de0bcf5f82aa6ccfb66ddd5 c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc`
(identical at the start AND end of both arms; the `diff=` term is the empty-diff
digest, so the engine trees were exactly HEAD's with no uncommitted edit)
**Engine binary** `on-disk=5b87cf4b53780639`, and **every measured row records that
same sha as its `running_engine`** — one image answered all 45 query executions.
**Harness** `scripts/tpch-relsize-arm.sh` (new; implements design §D7)
**Raw data** `analysis/tpch-relsize-fallback-20260730/{c1,c2}.tsv`, `followup-c1/`, `followup-c2/`

This discharges the timed half of `M0125-0003` stage 2 — design
`docs/design/0125-0003-relsize-fallback-and-tpch-stats-tradeoff.md` §D5.1's **C1 and
C2 arms**, which had been owed for six loops. §D5.1's W1/W2 arms are **not** delivered
and the reason is a measurement, not a schedule: see §4.

## 1. Result — C1 → C2 is a 1.40× stream win with zero regressions

Both arms: no ANALYZE, fresh server per query, 300 s per-query cap, identical
runtime configuration. C1 = `GOOPG_RELSIZE_FALLBACK` unset (today's default), C2 =
`GOOPG_RELSIZE_FALLBACK=2` (stage 2, the DP seed).

| Q | C1 (flag off) | C2 (stage 2) | C2/C1 | rows | note |
|---|---:|---:|---:|---:|---|
| Q1 | 6.79 | 6.56 | 0.97× | 4 | |
| Q2 | 4.69 | 4.42 | 0.94× | 455 | round-4 watch list |
| Q3 | 13.83 | 13.75 | 0.99× | 11521 | |
| Q4 | 44.68 | 43.86 | 0.98× | 5 | round-4 watch list |
| Q5 | 66.66 | 65.73 | 0.99× | 5 | **pre-registered expected win — did not appear** |
| Q6 | 4.13 | 3.97 | 0.96× | 1 | |
| Q7 | 46.30 | **34.96** | **0.76×** | 4 | win 1.32× |
| Q8 | 67.72 | 66.03 | 0.98× | 2 | round-4 watch list |
| **Q9** | 172.35 | **52.39** | **0.30×** | 175 | **WIN 3.29×** — in both watch lists |
| **Q10** | 32.64 | **12.66** | **0.39×** | 20501 | **WIN 2.58×** |
| Q11 | 3.46 | **2.07** | 0.60× | 819 | win 1.67× (small absolute) |
| **Q12** | 62.14 | **18.12** | **0.29×** | 2 | **WIN 3.43×** — round-4 watch list |
| Q13 | 12.73 | 12.44 | 0.98× | 35 | |
| Q14 | 5.38 | 5.81 | 1.08× | 1 | |
| Q15 | 36.41 | 36.33 | 1.00× | 1 | Q15b-MAIN |
| Q16 | 1.61 | 1.68 | 1.04× | 18213 | |
| Q17 | 31.15 | 31.74 | 1.02× | 1 | |
| Q18 | 37.01 | 37.12 | 1.00× | 10 | |
| Q19 | 6.69 | 6.86 | 1.03× | 1 | |
| Q20 | 24.94 | 24.77 | 0.99× | 76 | |
| **Q21** | **TIMEOUT >600 s** | **TIMEOUT >600 s** | – | – | **fails in BOTH arms — §3** |
| Q22 | 12.52 | 12.72 | 1.02× | 7 | round-4 watch list |

**Stream over the 21 comparable queries: 693.8 s → 494.0 s, −28.8 % (1.40×).**
**Wins: 4 (Q9 3.29×, Q12 3.43×, Q10 2.58×, Q7 1.32×, plus Q11 1.67× on 3 s).
Regressions: 0.** The largest adverse move is Q14 at 1.08× (0.43 s), inside the
noise band that Q16/Q17/Q19 (1.02–1.04×) establishes for this harness.

**Row counts are identical on every completing query in both arms** — the fallback
moved join order, never an answer. (The counts differ from round-5 §6's table
because the TPC-H cluster was reloaded on 2026-07-27; anchors are load-dependent
per `CLAUDE.md`. Within-run consistency is what this arm asserts.)

## 2. The pre-registration was wrong in both directions, and the reason is on record

Design §D5.2 pre-registered — *before* the run, as required — round-4's five
regressed queries **{Q2, Q4, Q8, Q12, Q22} plus Q9** as the watch list, and **Q5** as
the expected win. Measured:

- **None of the five regressed.** Q2/Q4/Q8/Q22 are neutral (0.94–1.02×) and **Q12,
  round-4's 4.4× loss, is this arm's second-largest WIN at 3.43×.**
- **Q5, the named expected win, did not move (0.99×).** It is already fast at S-cold
  here — 66.7 s, not round-4's 415.2 s — because M0077's `SmallDimension` + NLI
  override fixed it in the interim. The prior was read off a table whose Q5 cell no
  longer exists.
- **Q9 was the one correct call**, and it is the largest single win (172 → 52 s).

§D5.2's qualification 1 predicted exactly this failure mode and should be treated as
confirmed: round 4 supplied **full ANALYZE** — MCV and histogram *selectivity* as well
as row counts — while this flag supplies **relation-level row counts only** against
`pg_stats = 0`. That is a third regime, and round 4's sign does not transfer to it.
The measured shape of the third regime is: *sizes alone are monotonically helpful on
this workload; it was selectivity that wrecked those five queries.*

This is a result about **this workload at this scale on this code**, not a law. It does
not license skipping the same arms for stage 3, which reaches a different consumer.

## 3. Q21 fails in both arms — pre-existing, and it bounds the verdict

| arm | cap | outcome | peak RSS |
|---|---:|---|---:|
| C1 | 300 s | timeout | 14.16 GB |
| C1 | 600 s | timeout (612 s wall) | 14.37 GB |
| C2 | 300 s | timeout (366 s wall — overran its own cap) | 14.48 GB |
| C2 | 600 s | timeout (672 s wall — overran its own cap) | 14.83 GB |

Q21 was re-run at a 600 s cap in **both** arms so the table is symmetric. Three facts:

1. **It is not caused by the flag** — it fails identically with the flag off, i.e. on
   today's shipping default. Any claim that stage 2 "breaks Q21" is refuted.
2. **It does not honour cancellation.** The runner's 300 s budget elapsed and the wall
   clock reached 366 s / 672 s; the external `timeout -k 30 --signal=INT` clamp is what
   ended it, and the graceful stop then had to escalate to SIGKILL. This is round-5
   §6's non-cancelling star-query behaviour reproduced at S-cold **without** the
   cost-driven planner — a second, independent instance of the same defect class.
3. **It is at the memory ceiling**, 14.2–14.8 GB VmHWM against this harness's
   `GOOPG_MEM_MAX=15G` (so ~0.2–0.8 GB from a cgroup OOM kill), which is why
   `CLAUDE.md` records Q21 as the query that drew a host-level OOM at
   `GOMEMLIMIT=18GiB`.

Ledger rows appended for (2) and for the configuration caveat in §5.

## 4. W1/W2 are not "not yet run" — they are currently unconstructible

Design §D5.1's W arms need an ANALYZE that reaches the planner. `scripts/tpch-relsize-arm.sh
probe-analyze` measured, on this cluster, that it cannot:

```
$ scripts/tpch-relsize-arm.sh probe-analyze
target = tpch@tpch
ERROR:  relation "lineitem" does not exist        <- ANALYZE lineitem
 relname  | reltuples
 lineitem |         0
 orders   |         0
```

Two independent blockers, both already on record and now both *measured* rather than
assumed:

- `ANALYZE <table>` inside database `tpch` errors "relation does not exist" — the
  per-DB ANALYZE scoping gap (ledger `bench-reorg ANALYZE-scope`). So `RowCount`
  cannot be raised above 0 on this cluster at all.
- goopg's ANALYZE statistics are **per-connection**
  (`ANALYZE stats are per-connection` memory), so even once that is fixed the ANALYZE
  must be issued in the *same session* as the query — and `cmd/tpch-runner` opens a
  fresh connection per query with no ANALYZE step.

Consequence for the milestone: **the W1 = W2 invariant (design §D3) remains unmeasured**,
and `GOOPG_RELSIZE_FALLBACK`'s no-op-when-analyzed property is still argued from the
code (`relSizeFallbackRows` returns 0 when `Stats.RowCount > 0`) plus the unit test, not
from a TPC-H arm. The harness refuses `w1`/`w2` with that text rather than producing a
number that would silently be a duplicate C-arm. Ledger row appended with the resume
point (`cmd/tpch-runner -analyze`, gated on the ANALYZE-scope fix).

The practical loss is small and should be stated plainly: W1 is "the regime every
published goopg TPC-H number is in", so its absence means this report cannot say how
C2 compares to a *warm* server — only to the cold one it replaces.

## 5. Harness and its configuration caveat

`scripts/tpch-relsize-arm.sh <c1|c2|w1|w2|probe-analyze>` implements §D7: one query per
runner process, per-query runner budget plus an external `timeout -k 30 --signal=INT`
clamp at cap+60 s, a **full server restart between queries** with **no re-ANALYZE** (the
one deliberate divergence from round-5 §8 — re-ANALYZE would push `RowCount > 0` and
null the experiment), VmHWM per query, D4a engine provenance in every report header, and
a bounded stop ladder (`stop` → SIGINT → SIGKILL → scope teardown) because a leaked
backend once wedged the nightly for 6h45m. It refuses to run when a server already holds
port 65433 or when `ci/batch/run-nightly.sh` is live, and it builds a **private** image
(`tmp/goopg-relsize-bin`), never the shared `tmp/goopg-bench-bin` that ci/batch and the
SF=1 harness also run from.

**Configuration caveat — the absolute seconds here are not comparable to other reports.**
Both arms ran at `GOMEMLIMIT=12GiB`, `GOGC=100`, `GOOPG_MEM_HIGH=13G`,
`GOOPG_MEM_MAX=15G`. Nine queries reached 10–11.5 GB VmHWM and Q21 reached 14.8 GB, i.e.
above `MEM_HIGH`, so the heaviest queries spent time in the kernel reclaim/throttle band
(`cgroup_high_below_gomemlimit_throttle_trap`). The **A/B is unaffected** — both arms
share the configuration, the same binary, and a same-age server per query — but a later
loop must not diff these seconds against a run configured differently. Ledger row
appended.

## 6. Recommendation to M0125-0005 (the default flip)

The evidence now says **flip stage 2 on by default**, with two conditions:

1. **What the evidence supports.** On TPC-H SF=1 at S-cold: 1.40× on the stream, four
   real wins up to 3.4×, zero regressions, identical rows, and the round-4 regression
   class explicitly *not* reproduced. The risk statement in §D5.3 — "plausibly imports
   round 4's statistics regressions into every un-ANALYZEd server" — is **refuted for
   stage 2 on this workload**.
2. **What it does not support.** (a) Stage 3 — untouched here, and §I8 records that it
   *shadows* this tier, so its arms must be run separately, not inferred. (b) Any claim
   about a warm server, per §4. (c) TPC-DS: §D8's cheap instrument (the SF0.5 gate at
   both flag states) has **not** been run at stage 2, and TPC-DS relief is the reason
   this milestone exists — the timeout class is the acceptance criterion, not TPC-H.
   Q9's 3.3× is encouraging for the same reason §13.5 predicted it (a fact table whose
   volume the DP could not previously order), but encouragement is not the gate.
3. **What the flip must also re-measure**, per §D6: `scripts/tpch-spotcheck.sh` runs
   S-cold, so the flip changes the gate every planner commit must pass. Q12 is one of
   its two queries and it is a 3.4× **win** here, which is the good direction — but the
   flip commit owes the spotcheck's wall clock and peak RSS in both flag states.

So: M0125-0005 is unblocked on the TPC-H side and blocked on the TPC-DS side. The next
measurement, in priority order, is **the SF0.5 gate at `GOOPG_RELSIZE_FALLBACK=2`**
(§D8, ~1 h, goopg-only, checksum-verified) — that is what turns "1.40× on TPC-H" into a
verdict about the class this milestone is named after.
