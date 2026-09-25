# M0144-0004 — Instrumented PG 18.3, instrument 1: `OPTIMIZER_DEBUG` build

Instrumented-oracle capture. PG 18.3 built from a **scratch checkout** with
`OPTIMIZER_DEBUG` defined; a private clone of the TPC-DS SF0.25 corpus served
on a `55xx` port; per-relation survivor pathlists captured for a
representative set of M0144-0002 first-divergence queries.

Design doc: `docs/design/0100-0149/m0144-0004-optdebug-instrumented-pg.md`.

## Build recipe (reproducible)

`./postgres/` stays read-only. Everything lives under `tmp/pg18-optdebug/`
(git-ignored):

```bash
git clone postgres tmp/pg18-optdebug/src        # HEAD 62d6c7d3df6 "Stamp 18.3"
cd tmp/pg18-optdebug/src
./configure --prefix=<abs>/tmp/pg18-optdebug/install \
    --enable-cassert --enable-tap-tests \
    CPPFLAGS="-DOPTIMIZER_DEBUG"                # lands in src/Makefile.global
make -j12 && make install                     # ~8 min on 16 cores
install/bin/initdb -D ../data -A trust --no-sync
install/bin/pg_ctl -D ../data -l ../server.log \
    -o "-p 5560 -c listen_addresses=localhost" start
```

Private corpus clone — loaded from the same TSVs the regression harness
uses, **no reference-cluster access at all**:

```bash
createdb -h 127.0.0.1 -p 5560 tpcds025
psql -f third-party/tpcds-postgres/DSGen-software-code-3.2.0rc1/tools/tpcds.sql
for t in <25 tables>: COPY $t FROM 'bench/tpcds/runtime_goopg/tpcds-data-sf025/$t.tsv'
ANALYZE
ALTER SYSTEM SET max_parallel_workers_per_gather = 4   # match :65438 (ref GUC dump)
```

Row counts verified against TSV line counts (store_sales 719876 etc.).

## What `OPTIMIZER_DEBUG` emits

`pprint(rel)` (nodeToString of the whole RelOptInfo) at survivor-selection
points — i.e. after `add_path` tournament + `set_cheapest`:

| site | dump |
|---|---|
| `allpaths.c:562` (`set_rel_pathlist`) | base rel pathlist + partial_pathlist |
| `allpaths.c:3523` (join levels) | each join RelOptInfo after `set_cheapest` |
| `allpaths.c:4416` (`set_append_rel_pathlist`) | appendrel children incl. `APPENDPATH` entries |
| `planner.c:1310` | `After canonicalize_qual()` BOOLEXPR (not a pathlist) |

Output goes to the backend's **stdout** → captured via `pg_ctl -l
server.log`, sliced per query by log line offsets.

## Captured set (12 queries, covers every census category with n≥2)

| query | census cluster it represents | RELs dumped |
|---|---|---|
| Q7, Q10 | `Limit → GroupAggregate` vs `Sort` (n=14, dominant) | 20, 25 |
| Q3 | `Limit → Incremental Sort` vs `Sort` (n=5) | 6 |
| Q40 | `Limit → Finalize GroupAggregate` vs `Sort` (n=4) | 20 |
| Q5, Q56 | `Limit → Sort` vs `CTE` (n=7); Q5 also the Parallel-Append witness | 31, 55 |
| Q18 | aggregation-strategy under `Sort` | 49 |
| Q12 | join-method `NL → PHJ` (n=2) | 6 |
| Q42 | join-order leaf-set variant (n=2) | 6 |
| Q34 | qual-placement (n=4) | 14 |
| Q47 | `WindowAgg` vs `Sort` (n=2) | 18 |
| Q23 | parallelism `Parallel Seq Scan` vs `Seq Scan` (n=8); two-statement query | 104 |

Artefacts under `analysis/m0144/optdebug-0004/`:

- `q<N>-plan.txt` — the instrumented build's EXPLAIN output (Q23 = both
  statements concatenated).
- `q<N>-survivors.txt` — distilled per-rel survivor tables produced by
  `scripts/pg-optdebug-survivors.py` (new tool this task): per
  `{RELOPTINFO` block, every `pathlist`/`partial_pathlist` survivor with
  node type, `required_outer`, rows, disabled_nodes, parallel_workers,
  startup..total cost, pathkeys presence, indexoid, jointype — plus the
  `cheapest_total_path` marked `*cheapest*`.
- Raw nodeToString slices stay in `tmp/pg18-optdebug/captures/` (~211 MB
  for the 12 queries — too large for git; regenerate via the recipe).

## Fidelity check

All 12 captured plans **structurally match the :65438 reference plans**
(node-type sequence compared against
`analysis/m0142/m0142-0012verify-tpcds-pg.txt`): 11/11 single-statement
queries + Q23's two statements. Only ANALYZE re-sample drift in estimates
(e.g. Q7 `rows=44` vs reference `46`, `date_dim` filtered 212 vs 214) —
expected; stats are re-drawn per ANALYZE.

## Known limits (task-mandated record)

- **Survivors-only.** `add_path` prints nothing — rejected candidates are
  already evicted when `pprint` fires. What survives is the post-tournament
  pathlist + the `set_cheapest` winner. The rejected-candidate/rejection-
  verdict view is exactly what M0144-0005's `debug_plan_candidates` trace
  GUC exists to add.
- **No rel-name labels.** `RELOPTINFO` dumps carry `relids` bitmapsets, not
  table names; mapping rel→table needs the RTE (the `:rte` subtree is in the
  raw log but not distilled).
- **No subquery separators.** CTEs/subqueries planned via
  `subquery_planner` emit their own rel sets in dump order with no explicit
  boundary marker (Q5: four `relids=(b 1)` blocks = four subquery
  plannings). Order is preserved; boundaries must be inferred.
- **Volume.** ~6 M lines of nodeToString for 12 queries — hence the
  distiller. The distilled files keep every surviving path's identity and
  cost; the raw quals/targetlists are only in `tmp/`.

## First reads worth noting

- **Q7 (dominant cluster):** rel 1 (`store_sales`, `b 1`) survives with a
  `req_outer=(b 4)` INDEXPATH `rows=44` — the parameterized NL-inner that,
  under `Gather Merge`, produces `i_item_id` ordering for free →
  GroupAggregate-under-Limit wins without a full Sort. The `*cheapest*`
  marks + per-path costs are the direct input for 0007's cost-margin census.
- **Q5 (appendrel witness):** 11 `APPENDPATH` survivors across four rel
  sets, each with a `*cheapest*` APPENDPATH + a `pw=3` partial APPENDPATH —
  PG's `add_paths_to_append_rel` partial-path admission captured in full;
  the concrete template for M0144-0003b.
- **Q23:** `APPENDPATH` ×5 — includes `Parallel Append`-class partial
  append survivors under CTE subplanning.

## Resume points

- Scratch tree + private cluster persist at `tmp/pg18-optdebug/` (`src/`
  built, `install/` binaries, `data/` tpcds025 loaded, `server.log`).
  M0144-0005 can patch the same tree (`debug_plan_candidates` GUC) and
  rebuild in place; the data dir is reusable as-is (same PG version).
- `scripts/pg-optdebug-survivors.py <log-slice>` — the distiller; reuse for
  0005/0006 captures.
