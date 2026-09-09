# R30 results — ANALYZE sample sorted into physical order

Implements `DESIGN.md`. One behaviour change, in
`internal/executor/operators_analyze.go`: the reservoir is sorted by
`(block, offset)` before column statistics are computed, mirroring
`analyze.c:1312-1322` / `compare_rows`.

## 1. The statistic is now PG-faithful

Correlation on the TPC-DS SF0.5 clone, against PG 18.3 (:65438):

| column | goopg before | goopg after | PG 18.3 |
|---|---|---|---|
| `customer.c_customer_sk` | 0.0737 | **1.0** | 0.999908 |
| `date_dim.d_date_sk` | — | **1.0** | 1.0 |
| `item.i_item_sk` | — | **1.0** | 0.9972569 |
| `store.s_store_sk` | — | **1.0** | 1.0 |
| `store_sales.ss_item_sk` | — | **-0.0017** | 0.00038 |

Surrogate keys now correlate at ~1 as they physically do, and a column
that genuinely is not the physical order (`ss_item_sk`) still reads ~0.
The remaining digits differ because the two samples differ; the
statistic's *meaning* now matches.

## 2. Parity: prediction held, and it cost something

Fresh-ANALYZE A/B, seed pinned, both corpora:

| | TPC-DS base | TPC-DS R30 | TPC-H base | TPC-H R30 |
|---|---|---|---|---|
| **scan-type** | 72 | **71** | 13 | **12** |
| **join-method** | 77 | **79** | 12 | **14** |
| match | 0 | 0 | 1 | 1 |
| every other category | — | unchanged | — | unchanged |

The same trade appears independently on both corpora: **scan-type −1,
join-method +2**. That reproducibility is what makes it a finding
rather than noise (and the A/A in DESIGN §4 confirms the floor).

Reading: correct correlation makes index scans cheaper, so goopg now
picks PG's scan in more places — and those cheaper index scans then
shift *join* choices, where goopg's costing is not yet PG-faithful.
The statistic fix converted a hidden wrong input into a visible
downstream divergence.

**Shipped rather than reverted, deliberately.** The goal requires plans
to agree "as a result of the same statistics, the same cost
computation, and the same planning logic". Restoring a correlation of
0.07 on a perfectly ordered primary key to buy back two join-method
rows would be preserving a compensating error — two wrongs cancelling
in the category count while both inputs stay unlike PG's. Join-method
costing is the next axis (K26), and it is now the *measured* blocker
rather than an assumed one.

## 3. Correctness gates

- `go test ./internal/executor/ ./internal/optimizer/ -count=1` — ok.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — 44
  packages ok, zero FAIL.
- TPC-H values digest — **byte-identical** to the r9 reference.
- TPC-DS SF0.5 sweep —
  `PASS=95 (57 ck-verified) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4`;
  `PLAN-SHAPE same=99 changed=0` there, because that gate cluster keeps
  its stored statistics and was not re-ANALYZEd.

## 4. Baseline note (affects every future round)

Both parity clones were re-ANALYZEd during this round, so the stored
statistics they carried from earlier rounds are gone. Under the
fresh-ANALYZE, seed-pinned protocol the TPC-H baseline reads
**match=1/22**, where earlier rounds quoted 2/22 against stale stored
stats. The earlier figure is not reproducible and the fresh one is;
future rounds should quote the fresh protocol. The lost match was not
identified — the stale state was overwritten before the discrepancy was
noticed, and inventing an explanation for it would be guesswork.

## 5. Follow-ups

- **K34** — join-method is now the top measured blocker on both corpora
  (TPC-DS 79, TPC-H 14) and it *grew* when a scan-cost input was
  corrected, which points at nestloop-vs-hash/merge pricing rather than
  at scan pricing.
- **K33** stands (bitmap priced under a pkey index scan, 12 sites) but
  is smaller than K34.
- Index-level statistics remain absent: `estimateIndexGeometry` still
  synthesises `relpages`/`reltuples`/`tree_height` because ANALYZE does
  not visit indexes. That is the next statistics-axis gap after this one.
