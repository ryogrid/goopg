# M0141-S2b-6-resume — Hashed-vs-Sorted `PathAgg` term-by-term diff at real SF1

Status: accepted (recon complete; landed a small permanent trace + one
GUC-toggle experiment; no cost-model formula changed)
Date: 2026-09-18
Parent: `docs/design/0100-0149/m0141-s2b-scoping-decomposition.md` (frozen at
977 lines under D3.1 — this doc is a fresh file rather than an appended
section, see that doc's own "S2b-6 result" for the synthetic-dataset attempt
this one supersedes)

## Question

S2b-6 (synthetic-dataset probe, refuted) could not tell whether goopg's
`electOrderedGrouping` (`upperorderedgrouping.go`) picking `Hashed`+explicit-
`Sort` over the sort-free `Sorted` candidate for TPC-H Q4/Q5/Q12/Q21 is a
genuine cost-model win/loss or an artifact of a degenerate synthetic dataset
where `numGroups ≈ inputRows` ties the two candidates' Sort terms down to the
float. The follow-up (`M0141-S2b-6-resume`) was gated on the shared `:65433`
TPC-H cluster's data being reloaded — done by P0-E6 (2026-09-18) — and asks
the same question against the real HammerDB SF1 load.

## Instrumentation landed

Two new DPPATH trace lines, gated on the existing `GOOPG_PGSHAPED_DP_TRACE=1`
(no new env var — same convention as every other diagnostic channel in this
package):

- `pathtrace.go`: `traceOrderedGroupingCandidate(strategy, rows)` — the
  `AggStrategy`/`Rows` label the generic `traceOrderedSeedCandidate` line
  does not carry, so a Hashed candidate's pre-sort-stack cost and a Sorted
  candidate's own (input-sort-inclusive) cost can be told apart on sight
  instead of inferred from `contained`.
- `pathtrace.go`: `traceOrderedSortedCandidate(strategy, rows, sortStartup,
  sortTotal)` — the AFTER-sort cost `setCheapest` actually compares, for the
  non-`contained` branch of `addOrderedPaths`.
- `upperordered.go`'s `addOrderedPaths` calls both, gated `input.Kind ==
  PathAgg` so every other `addOrderedPaths` caller (the plain ORDER BY seam)
  is unaffected and emits no new lines.

Both are read-only trace emitters (`if !pathTraceEnabled { return }` first
line, matching every sibling in the file); `go build ./...` and `go test
./internal/optimizer/...` are clean with them in place. This is the "Kind:
impl" component the task's own field names — a permanent trace addition to
`internal/`, per the 2026-09-18 field-naming rule that a trace-only change is
never filed as a bare recon.

## Method

The shared `:65433` cluster is read-only for the loop (no DDL/DML/stop/
reset). `scripts/tpch-estimate-audit-arm.sh` already exists for exactly this
shape of measurement (M0137-0007's private-clone lane): it snapshot-clones
`:65433` online via `pg_basebackup -X fetch` (the shared server is never
stopped) onto a private port (5582), starts a HEAD-built goopg binary there
with `GOOPG_PGSHAPED_DP_TRACE=1`, and runs `cmd/estimate-audit --plan-only`
(EXPLAIN, not EXPLAIN ANALYZE — no execution timing, so it cannot be
contaminated by anything else on the host and needs no nightly-batch
refusal). `PGSHAPED=1` was passed explicitly on every arm — per
`m0141_s2a_fix1_sweep_b`'s own memory: `GOOPG_PGSHAPED_DP` is unset(on), i.e.
DP-shaped, by default, and the script's own `PGSHAPED=0` default is for an
unrelated retired-path A/B, not a values-comparison baseline.

```
PGSHAPED=1 PLAN_ONLY=1 DP_TRACE=1 REFERENCE="" \
  scripts/tpch-estimate-audit-arm.sh m0141-s2b6-resume-2026-09-18 --queries 4,5,12,21
```

Ran clean (rc=0, served-binary sha256 verified against the just-built image).
The server log (`tmp/tpch-audit-*.server.log`, not committed — matches every
prior arm-script run in this file's history) carries the two new trace lines
per candidate.

## Result — term-by-term, real SF1 cardinalities

| query | strategy=0 (Hashed) own cost | strategy=1 (Sorted) own cost | Sorted `contained` (no extra sort needed)? | election |
|---|---|---|---|---|
| Q4  | 517031.028 | 517930.859 | **true** | Hashed+Sort (517031.099) beats Sorted (517930.859) by **899.76** |
| Q5  | 279659.980 | 280150.281 | false (both need a stacked Sort) | Hashed+Sort (279660.623) beats Sorted+Sort (280150.924) |
| Q12 | 353500.272 | 355682.343 | **true** | Hashed+Sort (353500.388) beats Sorted (355682.343) by **2181.96** |
| Q21 | 1616.6435  | 1616.6585  | false (both need a stacked Sort) | Hashed+Sort (1616.6585) beats Sorted+Sort (1616.6735) |

Every one of the four is a **real, non-tied margin** — none of S2b-6's
clamped-float-tie artifact survives at real cardinalities. `DPGROUP elected
shape=Sort-over-Aggregate strategy=0` (Hashed) fires for all four, exactly
reproducing S2b-5's original 6-query finding, now confirmed at real scale
rather than refuted by it.

Q5/Q21 are the uninteresting half of the four: the Sorted candidate's own
presorted order does **not** satisfy the statement's ORDER BY (`contained =
false` for strategy=1 too — GROUP BY key ≠ ORDER BY key, e.g. Q5 groups by
`n_name` but orders by revenue), so both candidates pay their own Sort and
the comparison is apples-to-apples. This reconfirms S2b-5/S2b-6's "no cost
bug" verdict for this pair, now at real SF1 data.

Q4/Q12 are the real question: the Sorted candidate's presorted output
*already* satisfies ORDER BY (`contained = true`), so this is the genuine
input-Sort-vs-output-Sort asymmetry S2b-6 set out to observe. At real
cardinalities the input-Sort (Sorted, pricing the FULL pre-group join output
— 12731 rows for Q4, 28524 for Q12) is decisively more expensive than the
output-Sort (Hashed, pricing only the post-group row count — 5 and 7 rows
respectively), and goopg picks Hashed+Sort by a four-to-five-digit margin.

## Cross-check against live PG — is Hashed+Sort actually WRONG?

A non-tied margin only proves the election is decisive, not that it is
*correct*. Ran fresh `EXPLAIN` (not a stale scratch capture — S2b-5's own
citation was second-hand) against the read-only PG 18.3 reference cluster
(`:65432`, db `tpch`, `max_parallel_workers_per_gather = 0`):

```
-- Q4
GroupAggregate  (cost=190893.67..190990.46 rows=5 width=24)
  Group Key: orders.o_orderpriority
  ->  Sort  (cost=190893.67..190925.92 rows=12899 width=16)
        Sort Key: orders.o_orderpriority
        ->  Nested Loop Semi Join ...

-- Q12
GroupAggregate  (cost=328065.16..328634.21 rows=7 width=27)
  Group Key: lineitem.l_shipmode
  ->  Sort  (cost=328065.16..328136.28 rows=28449 width=27)
        Sort Key: lineitem.l_shipmode
        ->  Hash Join ...
```

Real PG picks **Sorted** (`GroupAggregate` fed by a `Sort` *below* it, no
Sort above) for both Q4 and Q12 — the mirror image of goopg's election. This
confirms the divergence is real: goopg's cost model, even at real SF1
cardinalities that cleanly separate the two candidates, elects the OPPOSITE
of what PG's own cost model elects for the same two queries. Not a tie, not
a synthetic-data artifact — a genuine Hashed-vs-Sorted cost-model bug.

## One hypothesis tested and ruled out: the R113 Sort byte-size currency

`sort_pgrelationbytes.go`'s `GOOPG_PG_SORT_RELATION_BYTES_COST` GUC (R113,
default off) already exists to swap `costSortRun`'s input byte-size formula
from goopg's own executor-entry-byte currency to PG's `relation_byte_size`
formula — exactly the class of bug this milestone (M0141-S2a's currency-narrowing
line) has been chasing. The `DPPGSORT` trace line each arm already emits
shows a real gap for Q4's Sorted-candidate input Sort: `goopginput=2.3425e+06`
vs `pginput=1.22218e+06` — goopg's own formula prices that exact sort at
roughly **1.9x** PG's.

Re-ran Q4/Q12 with the GUC flipped on (`GOOPG_PG_SORT_RELATION_BYTES_COST=1`,
no code change, pure env toggle against the same private clone/binary):

```
GOOPG_PG_SORT_RELATION_BYTES_COST=1 PGSHAPED=1 PLAN_ONLY=1 DP_TRACE=1 REFERENCE="" NO_BUILD=1 \
  scripts/tpch-estimate-audit-arm.sh m0141-s2b6-resume-2026-09-18b --queries 4,12
```

`DPPGSORT` confirms the switch took (`currency=pg` instead of
`currency=goopg`), but the **election did not change**: `DPGROUP elected
shape=Sort-over-Aggregate strategy=0` fires for both Q4 and Q12 again, and
Hashed+Sort (508726.90) still beats Sorted (509633.37) for Q4 by ~906 —
almost the identical margin as the goopg-currency run (899.76). **The R113
Sort byte-size currency is not the root cause of this divergence** — ruled
out cleanly rather than assumed. Whatever is wrong lives elsewhere: most
likely `costAgg`'s own `AggStrategyHashed` pricing (build+probe cost) being
too cheap relative to PG's `cost_agg` hashed-strategy formula for these
group counts, or a difference in what each side charges the join/scan input
feeding the two candidates. Root-causing which is explicitly **out of
scope** here — the resume task's job was the term-by-term diff, not the fix.

## Conclusion

1. S2b-6's "cannot answer with synthetic data" verdict is resolved: real SF1
   data gives four genuine, non-tied Hashed-vs-Sorted cost comparisons.
2. Q5/Q21 confirm "no cost bug" (their ORDER BY never matches GROUP BY, so
   both candidates pay a comparable Sort) — no further action.
3. Q4/Q12 are a **confirmed real cost-model divergence from PG**: goopg
   elects Hashed+Sort, PG elects Sorted, and the R113 currency swap (the
   nearest existing suspect) does not explain it.
4. Follow-up fix work is filed as **M0141-S2b-10** (`.ralph/fix_plan.md`;
   M0141-S2b-8/-9 are already taken by the unrelated TPC-DS
   Incremental-Sort candidate-pool line of work), `Kind: recon`,
   `Parent: M0141-S2b-6-resume` — root-cause `costAgg`'s
   Hashed-vs-Sorted formula gap for Q4/Q12 shaped queries (GROUP BY key ==
   ORDER BY key, low output cardinality, large join-output input) before
   attempting a fix, since this doc's own R113 experiment already shows the
   nearest hypothesis was wrong and a blind formula edit here risks exactly
   the kind of regression `docs/design/cost-model/` (the "0077 line" bundle)
   warns against.

## Gates run

- `go build ./...` clean.
- `go test ./internal/optimizer/...` PASS (full package, no `-count=1`).
- `scripts/tpch-estimate-audit-arm.sh` (both arms) rc=0, served-binary
  sha256 verified.
- `python3 scripts/ralph_protected_regions.py check-designdocs` exit 0.
- Private-lane only: no read/write against `:65432`/`:65433` beyond
  `pg_basebackup -X fetch` (never stops/starts/writes the shared cluster)
  and read-only `EXPLAIN` on `:65432`.
