# M0144-0004 — Instrumented PG 18.3: `OPTIMIZER_DEBUG` build + survivor-pathlist capture

Status: landed 2026-09-20 (instrument + captures + distiller).
Analysis record: `analysis/m0144/m0144-0004-optdebug-build.md`.

## Purpose

M0144's measurement-first phase needs to see what PostgreSQL's own planner
*kept*, per relation, on the queries where goopg's plan first diverges —
before changing any goopg code. `OPTIMIZER_DEBUG` is upstream's built-in
switch (`#ifdef` sites in `allpaths.c`/`planner.c`) that calls
`pprint(rel)` at each `set_cheapest` survivor-selection point. It is the
first of three Phase-A instruments (0004 `OPTIMIZER_DEBUG` → 0005
`debug_plan_candidates` trace GUC → 0006 `-finstrument-functions` call
graphs).

## Design decisions

1. **Scratch checkout, never the oracle.** `git clone postgres
   tmp/pg18-optdebug/src` — same commit `62d6c7d3df6` ("Stamp 18.3") as the
   read-only `./postgres/` tree. All build artefacts stay under `tmp/`
   (git-ignored).
2. **`CPPFLAGS="-DOPTIMIZER_DEBUG"`** at configure time — lands in
   `src/Makefile.global` (`CONFIGURE_ARGS` records it in `pg_config.h`), no
   source edits needed.
3. **Private corpus on a 55xx port.** `:5560`, database `tpcds025`, loaded
   from the harness's own SF0.25 TSVs
   (`bench/tpcds/runtime_goopg/tpcds-data-sf025/`) + `tpcds.sql` schema +
   `ANALYZE`. R1 applies: zero reference-cluster access. The only non-default
   GUC mirrored from `:65438` is `max_parallel_workers_per_gather = 4`.
4. **Stdout capture via `pg_ctl -l`.** `pprint` writes to the backend's
   stdout; the postmaster log aggregates it. Per-query slices taken by
   log-line offsets around each `EXPLAIN`.
5. **Distill, don't commit raw.** nodeToString volume is ~6 M lines for 12
   queries (~211 MB). `scripts/pg-optdebug-survivors.py` parses the sexp
   stream (depth-tracked, string-aware) and emits one line per surviving
   path — type, required_outer, rows, disabled_nodes, parallel_workers,
   startup..total cost, pathkeys, indexoid, jointype — with
   `cheapest_total_path` marked. Raw slices stay in `tmp/` and are
   reproducible from the recipe.

## Capture mechanics (for reuse by 0005/0006)

```bash
start=$(wc -l < tmp/pg18-optdebug/server.log)
psql -h 127.0.0.1 -p 5560 -d tpcds025 -qc "EXPLAIN $(cat queryN.sql)"
end=$(wc -l < tmp/pg18-optdebug/server.log)
sed -n "$((start+1)),${end}p" tmp/pg18-optdebug/server.log > capture.log
python3 scripts/pg-optdebug-survivors.py capture.log
```

Two gotchas discovered and handled:

- **nodeToString namespaces path fields** — `path.*` for most paths,
  `jpath.path.*` for join paths, bare fields for plain `Path` (`{PATH` =
  seqscan, `pathtype` tag maps to the plan-node name); the distiller matches
  by last dotted component and tracks sexp depth so nested subpaths inside
  join paths don't pollute the entry.
- **Multi-statement queries** (TPC-DS Q23): `EXPLAIN <file>` only explains
  statement 1; statement 2 executes for real. Split on `;` and EXPLAIN each.

## Known limits

Survivors-only: `add_path` emits nothing, so the rejected candidates and
rejection verdicts are invisible — that is precisely the gap 0005's trace
GUC fills. No rel-name labels (relids bitmapsets only), no subquery
boundary markers, no per-path width in the distilled view.

## References

- Upstream emit sites: `postgres/src/backend/optimizer/path/allpaths.c:562`,
  `:3523`, `:4416`; `planner.c:1310`.
- `scripts/pg-optdebug-survivors.py` — distiller.
- `analysis/m0144/optdebug-0004/` — 12 distilled captures + EXPLAIN plans.
