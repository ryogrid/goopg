# R31b — the heap reload, done; and it does NOT move parity

`FINDINGS.md` ended by calling the bench-data reload an owner decision
and treating it as blocking. **That framing was wrong and is corrected
here.** The reload only needs owner approval for the SHARED cluster
under `bench/tpcds/`; every parity measurement in this workstream runs
against a PRIVATE clone, which is mine to rebuild. It was rebuilt, and
the question it was blocking is now answered.

## 1. What was done

A fresh private cluster (`/tmp/pp2/ds05fresh`, :5547) was built with the
current binary by replicating `cmd_load_goopg` exactly: `init`, the
stock `tpcds.sql` schema, `COPY` of the same read-only sampled TSVs,
then per-table `ANALYZE`. No shared state was written. Schema parity was
verified before measuring: **25 tables and 24 indexes on all three
clusters** (fresh goopg, old goopg, PG).

## 2. The physical divergence is gone on the fact tables

| table | old goopg | **fresh goopg** | PG 18.3 | fresh vs PG |
|---|---|---|---|---|
| `store_sales` | 29761 | **25866** | 25928 | −0.24% |
| `catalog_sales` | 20530 | **18713** | 18760 | −0.25% |
| `web_sales` | 10269 | **9382** | 9416 | −0.36% |
| `inventory` | 25460 | **25460** | 25504 | −0.17% |

The ~15% fact-table inflation was indeed a stale-load artifact: the
current binary reproduces PG's density to within 0.4%.

Dimension tables still diverge and in the OTHER direction — `customer`
1979 vs PG 2872, `item` 716 vs 1284 — so goopg packs wide-varchar rows
more tightly than PG. That is a separate, still-open divergence (K41);
it was masked earlier by the fact-table error running the opposite way.

## 3. Worker counts: fixed, exactly as predicted

| Workers Planned | old clone | **fresh clone** | PG |
|---|---|---|---|
| 1 | 11 | 11 | 35 |
| 3 | 12 | **31** | 147 |
| **4** | **19** | **0** | **0** |

`store_sales` fell from 29761 to 25866 pages, back under PG's
27648-page rung for a fourth worker. goopg no longer plans 4 workers
anywhere, matching PG. The `compute_parallel_worker` ladder was faithful
all along; only its input was wrong, and the input is now right.

## 4. Parity: unmoved. The confound was not the blocker.

```
old clone    join-order=95 join-method=79 scan-type=71 parameterisation=45
             aggregation-strategy=83 sort-strategy=82 parallelism=90
             qual-placement=13 rendering=32     match=0
fresh clone  ... identical except qual-placement 13 -> 12 ...   match=0
```

One category moved by one. `PLAN-PARITY` is unchanged: `match=0
shapediff=70 missingnode=26 error=3`.

**This is the round's real result, and it contradicts the priority
`FINDINGS.md` assigned.** A 15% error in every fact table's page count —
which feeds `cost_seqscan` directly — changed essentially nothing about
which plans goopg picks. The remaining divergence is therefore not a
statistics-input problem at all; it is in the cost computation and the
planning logic themselves.

Two consequences, stated plainly:

- The reload is **not** a prerequisite for resuming parity work, and
  R31's recommendation to re-baseline before continuing is withdrawn.
- It **is** still worth doing on the shared cluster for measurement
  honesty (§5), just not urgently, and not as a blocker.

## 5. Standing value of the rebuild

The fresh clone is retained as the parity baseline going forward. Its
cost inputs are PG-faithful on the fact tables, so a future costing
round is measured against PG's own page counts rather than against a
15%-inflated heap. R30's TPC-DS numbers were taken on the inflated
cluster; its conclusion (scan-type −1, join-method +2) has not been
re-verified here and should be re-measured on the fresh clone before
being built upon.

## 6. Where the parallelism gap actually lives

Not in worker counts — those now match. The gap is the one `FINDINGS.md`
§2 measured: goopg emits 42 `Gather`s to PG's 184 and 0 `Parallel Hash`
to PG's 314 by default, because `GOOPG_GATHER_PATHS` is off, and turning
it to `all` adds the nodes without improving parity. K37/K38 stand
untouched by this round.
