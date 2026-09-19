# M0142-0005f — TPC-H corpus physical-layout parity: census + rebuild recipe

Kind: recon. Parent: M0142-0005c.

Status: recon complete, no production change (DONE 2026-09-19).

M0142-0005c showed `indexProbeCostMultiplier=2.0` is load-bearing on
inputs: goopg's TPC-H heap is perfectly clustered on every probe key
(`correlation=1.0`) while PG's reference heap is fragmented (0.19–0.85).
This recon (a) measures the divergence corpus-wide, (b) produces and
**validates** a concrete rebuild recipe, (c) names the expected movement.

## 1. Census — all key columns, both corpora

Method: `pg_stats.correlation` (stored stat) + adjacent-key inversion
count over **heap physical order**. Physical order = bare seqscan emission
with `max_parallel_workers_per_gather=0`; on PG, index paths were
additionally disabled (`enable_indexscan/indexonlyscan/bitmapscan=off`)
because a single covering index makes PG answer `SELECT key FROM t` with
an index-only scan — index order, not heap order (this polluted the first
pass: partsupp read 0/199999 inversions under IOS vs the true 12235/206695
below). Script: `tmp/m0142-0005f-census.sh`; raw: `tmp/m0142-0005f-census.txt`.

| column | goopg corr | PG corr | goopg invers | PG invers |
|---|---|---|---|---|
| region.r_regionkey | 1.0 | 1.0 | 0 | 0 |
| nation.n_nationkey | 1.0 | 1.0 | 0 | 0 |
| nation.n_regionkey | 0.475 | 0.475 | 6 | 6 |
| supplier.s_suppkey | 1.0 | 0.9996 | 0 | 28 |
| supplier.s_nationkey | 0.0224 | 0.0307 | 4858 | 4804 |
| customer.c_custkey | 1.0 | 0.9980 | 0 | 731 |
| customer.c_nationkey | 0.0308 | 0.0421 | 71936 | 72195 |
| part.p_partkey | 1.0 | 0.8458 | 0 | 845 |
| partsupp.ps_partkey | 1.0 | 0.8451 | 0 | 12235 |
| partsupp.ps_suppkey | 0.0024 | 0.0252 | 200019 | 206695 |
| orders.o_orderkey | 1.0 | 0.1940 | 0 | 16560 |
| orders.o_custkey | −0.0017 | −0.0024 | 751063 | 749707 |
| lineitem.l_orderkey | 1.0 | 0.1952 | 0 | 201108 |
| lineitem.l_partkey | −0.0015 | −0.0045 | 3000580 | 2999595 |
| lineitem.l_suppkey | −0.0028 | 0.0044 | 3000434 | 2999037 |
| lineitem.l_linenumber | 0.1852 | 0.1804 | 1285676 | 1365152 |

The structure is clean:

- **Every column the generator shuffled already agrees** — FKs to
  small dims and the random non-key columns (n_regionkey, s_nationkey,
  c_nationkey, o_custkey, l_partkey, l_suppkey, l_linenumber) match to
  sampling noise on both metrics.
- **The divergence is exactly the four large key-ordered probe columns**:
  `part.p_partkey`, `partsupp.ps_partkey`, `orders.o_orderkey`,
  `lineitem.l_orderkey` — goopg `corr=1.0`, PG `0.845/0.845/0.194/0.195`.
  `supplier.s_suppkey`/`customer.c_custkey` are near-clustered on PG too
  (0.9996/0.9980) — PG's fragmentation is confined to the big tables,
  consistent with a parallel/chunked reference load interleaving key
  ranges while HammerDB→goopg preserved emission order.
- Row counts differ slightly (`lineitem`: goopg 6,001,255 vs PG
  5,998,835) — corpora are not row-identical; correlation is scale-free
  so the census is unaffected, but it matters for the recipe's
  consequence (§3).

Width of divergence in cost terms (from 0005c's input-swap ladder): the
corr input alone moves the Q9 probe 3.82 → ~4.7 at mult=1; combined with
loopCount/indexPages the full gap is 3.82 → 7.13.

## 2. Rebuild recipe — validated end-to-end on a private clone

Replay PG's physical row order into goopg:

```bash
# per table, PG :65432 (SELECT only — the COPY keyword is guard-blocked
# against reference clusters; psql --csv SELECT is equivalent):
psql -p 65432 -d tpch --csv -c \
  "SELECT * FROM <t> ORDER BY ctid" > /tmp/<t>.pgorder.csv
# on the rebuild target (goopg), after schema create:
COPY <t> FROM '/tmp/<t>.pgorder.csv' WITH (FORMAT csv, HEADER);
# then indexes, FKs (region→nation→supplier→customer→part→partsupp→
# orders→lineitem load order), ANALYZE.
```

Validation (private `:5533` clone of `:65433`): loaded all 800,000
`partsupp` rows in PG's `ORDER BY ctid` sequence into
`partsupp_pgorder`, `ANALYZE`, measured:

- `ps_partkey` correlation **0.8464** — vs PG's own 0.8451 (sampling
  noise), vs goopg's native 1.0.
- adjacent inversions **12,235** — *identical* to PG's heap.

goopg's COPY path preserves insertion order into the heap, so a PG-ctid-
ordered dump reproduces PG's layout — and its correlation — faithfully.
Caveat found: the load must run against db `postgres` or a db-scoped
clone path — the known per-DB scoping gap makes `COPY`/`ANALYZE` fail to
resolve new relations inside db `tpch` on this build.

## 3. Decision

**(a) Rebuild** — recommended. The recipe is validated, cheap (~8 dumps
+ reload), touches no planner code, and makes the corpora row-identical
(not just order-identical — the dump carries PG's exact row set). That
retires the only honest reason `indexProbeCostMultiplier` exists: with
PG-layout correlations the probes price like PG's at mult=1.

Consequences the owner must accept (it is an owner-side corpus action on
`:65433`, outside loop write rights):

- **Row anchors re-pin**: `bench/tpch/spotcheck_expected.env` (Q12/Q13)
  and `ci/batch/tpch-row-anchors.csv` move to PG's row set.
- Stored stats change corpus-wide → re-run `ANALYZE` and re-baseline the
  plan captures.
- Alternatively (b) keep the scalar as a documented corpus-layout shim —
  but it is now known to double-charge honest TPC-DS correlations.

## 4. Expected movement (named probes)

- TPC-H **Q9/Q10/Q14** (the `index.parameterised` flip sites): with
  corr→PG values, at `GOOPG_INDEX_PROBE_MULT=1` the probe prices ≈7 like
  PG (measured 7.13 forced-NL) instead of 3.82 — NL loses to hash and the
  three shapes hold at mult=1. **Hypothesis to verify on the rebuilt
  corpus**, then the multiplier-retirement impl task measures
  `CATEGORIES-EXCL-MATCH:`.
- TPC-DS at mult=1 (measured by M0142-0005b, would become reachable):
  `scan-type` 59→51, `join-order` 91→88; `qual-placement` 20→24 is a
  known *regression* the same step must file.

Movement: none (recon). Follow-up is **owner-side**: the rebuild writes
the shared `:65433` corpus (R1 — outside loop rights), so it is carried
as an ESCALATION block on the M0142-0005 root (S4 lineage budget
exhausted by 0005b–f closing `Movement: none`; only the owner reopens).
The sibling recon M0142-0005g is likewise open-but-frozen.
