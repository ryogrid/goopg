# M0146-0005 slice 28 — Q17/Q25/Q29 gather-height residual is a probe-cost epsilon, not a mechanism gap

Recon slice (no code). Resolves the last `parallelism`-classified
first-divergence family left by M0146-0027 slice 3: Q17, Q25, Q29 all
show `PG Nested Loop Inner | goopg Gather Merge` — i.e. both engines
gather the same shape but at different join-tree heights.

## The fork

TPC-DS Q17 SF0.25. Both engines agree on the serial upper spine
(`NL(d1)`, `NL(d3)` probes above the join chain). The divergence is the
placement of `catalog_sales` relative to the `Gather Merge`:

- **PG**: `Gather Merge -> Sort -> NL-chain over {ss,sr,d2,store,item}`,
  then `Index Scan catalog_sales` probed **above** the gather.
- **goopg**: `catalog_sales` probed **inside** the partial chain
  (`NL({sr,d2} partial, cs)` at the 3-rel level), so the Gather Merge
  covers six relations.

The census reads this as `parallelism`, but both candidate sets are
complete on both sides — the divergence is pure cost adjudication.

## goopg side (DPPATH, `GOOPG_PGSHAPED_DP_TRACE=1`, private clone :5590)

relids: ss=0 sr=1 cs=2 d1=3 d2=4 d3=5 store=6 item=7.

At the 6-rel joinrel `{0,1,2,4,6,7}`:

| candidate | total | verdict |
|---|---|---|
| `gather.merge.sort` over cs-inside partial NL (input 3841.76) | **4841.90** | **accepted (elected)** |
| `gather` same partial | 4841.86 | dominated (no pathkeys) |
| `nestloop.index` outer={0,1,4,6,7} inner={2}=cs | 4862.38 | dominated (Δ≈20.5) |
| `nestloop.partial` outer={0,1,4,6,7} inner={2} | 3862.28 | dominated |

So the PG shape — serial NL probing cs over the gathered 5-rel chain —
**was generated and priced** (4862.38); it lost to GM6 (4841.90) by
~20.5, i.e. ~0.4% of subtree cost.

## PG side (instrumented plancand, tpcds025@:5560)

rti: ss=1 sr=2 cs=3 d1=4 d2=5 d3=6 store=7 item=8.

At the 6-rel joinrel `{1,2,3,5,7,8}` PG also files **both** arms:

- `k=368` Gather over the cs-inside partial NL (3715.84): total
  **4715.94** — `rej ... via=tie cmp=cost:EQ keys:EQ outer:EQ`.
- `k=369` GatherMerge: total **4715.98** — `rej ... via=tie` vs the NL.
- Winner `k=356` `NestLoop out={1,2,5,7,8} in={3}` (NL over GM5 probing
  cs): **4715.98** — a dead fuzzy tie won by filing order.

So PG's own margin between "gather over cs-inside partial" and "probe cs
above the 5-rel gather" is ~0.04 — inside `add_path`'s fuzzy band. goopg
and PG disagree only on which side of a sub-1% tie to land.

## Where the sign flips: the 3-rel probe arm

Both engines carry the same `{sr,d2}` partial outer (goopg 3546.68 /
PG ≈3713.7). The ordering of the two probe targets differs:

| 3-rel partial NL arm | goopg | PG |
|---|---|---|
| `{sr,d2} ⋈ ss` probe | 3860.95 (probe Δ≈314) | 3714.44 |
| `{sr,d2} ⋈ cs` probe | 3839.84 (probe Δ≈293) | 3736.47 |

Per-probe parameterized index costs bound to sr:

| probe | goopg | PG |
|---|---|---|
| ss 3-key pkey (item,customer,ticket) | **2.055** | **1.313** |
| cs item_sk index + customer filter | **1.916** | **1.458** |

PG prices the unique 3-key ss pkey probe cheapest; goopg prices the cs
item_sk probe cheapest. The per-probe ordering flip (~0.3 units) —
amplified by full-rescan inner repetition (~144 outer rows/worker) —
swings ~20 units and elects the cs-inside partial in goopg vs the
ss-first partial in PG.

Q25/Q29 carry the identical signature in the sweep capture
(`plans-20260928-004613.txt`): same `catalog_sales_pkey` probe inside
the partial chain at cost ~1.92.

## Adjudication

**Priced-input / cost-adjudication residual — no mechanism gap.**

- Candidate pool: complete on both sides (cs-inside partial, cs-last
  serial NL, gathers at both levels all filed).
- Admission: all gather/partial shapes admitted correctly on both sides.
- Election: differs by ~0.4% total at the joinrel; PG itself decides the
  same fork by fuzzy-tie filing order (`via=tie`).
- Micro-cause: per-probe index-cost epsilon (cs-vs-ss ordering flip) —
  same class as slice 26's memoize-probe epsilon; goopg probes run
  uniformly ~0.5-0.75 dearer with a different inter-probe ordering.

Any change here would be plan-forcing or a heuristic — forbidden. The
legitimate fix is cost-model calibration, out of M0146-0005's
mechanism scope.

## Routing

- Probe-cost epsilon (index probe per-qual/descent pricing) →
  M0142-0005c cost-model lineage (same routing as M0146-0005z).
- Q17/Q25/Q29 remain `parallelism`-classified in the census, but the
  true residual is documented here; re-file them as cost-adjudication
  when the census next re-runs.

## Evidence files

- `goopg-dppath-excerpts.txt` — DPPATH lines for the 3-rel fork arms,
  per-probe param costs, and the 6-rel adjudication.
- `pg-plancand-excerpts.txt` — instrumented-PG equivalents including the
  `via=tie` rejections and win lines.
- Raw captures: `tmp/m0146-0005s28/server.log` (goopg trace, ~42k
  lines), `tmp/m0146-0005s28/q17-pg-plancand.txt` (PG plancand, 56k
  lines distilled via `scripts/pg-plancand-distill.py`).
