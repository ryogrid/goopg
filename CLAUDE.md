# goopg — session guidance

goopg is a from-scratch Go reimplementation of PostgreSQL. Vanilla-PG
compatibility is absolute; the read-only oracle is PG 18.3 under `./postgres/`
(never modify it). Build/run/test details: `AGENT.md`.

Also read `AGENT.md` alongside this file before working in the repository — it
holds the build/run/test procedures and the loop's completion-and-deferral
discipline.

## Benchmark clusters (TPC-H / TPC-DS)

Two fully separate benchmark stacks. TPC-H lives under `bench/tpch/`, TPC-DS
under `bench/tpcds/` (moved there 2026-07-27 — TPC-DS previously squatted
inside the TPC-H runtime tree and its SF=1 load had overwritten the TPC-H
cluster).

| port | engine | cluster | data dir |
|---|---|---|---|
| 65432 | PostgreSQL 18.3 | TPC-H reference (db `tpch`, SF=1) | `bench/tpch/runtime/pgdata` |
| 65433 | goopg | TPC-H bench (SF=1, HammerDB load) | `bench/tpch/runtime_goopg/data` |
| 65434 | — | **reserved: nightly TPC-H clone lane** (ci/batch) | `tmp/goopg-nightly-tpch-data` |
| 65435 | — | **reserved: nightly TPC-DS clone lane** (ci/batch) | `tmp/goopg-nightly-tpcds-data` |
| 65436 | goopg | TPC-DS SF=1 | `bench/tpcds/runtime_goopg/data` |
| 65437 | goopg | TPC-DS SF=0.5 (fast regression gate) | `bench/tpcds/runtime_goopg/data-sf05` |
| 65438 | PostgreSQL 18.3 | TPC-DS reference (dbs `tpcds`, `tpcds05`) | `bench/tpcds/runtime/pgdata` |

Setup / start / stop procedures:

- **TPC-H**: `bench/tpch/README.md`. Lifecycle: `bench/tpch/setup_goopg.sh
  [--reset]` / `stop_goopg.sh` (goopg), `setup_pg.sh` / `stop_pg.sh` (PG).
  Data load is HammerDB 5.0 (`build_schema_goopg.sh`, SF=1, ~12 min load +
  indexes, data generated client-side — no dbgen/.tbl files exist). Since the
  per-DB catalog work, goopg persists `CREATE DATABASE`: the tables live in a
  durable `tpch` database and `tpch@tpch` works across restarts (verified on
  the 2026-07-27 rebuild), so `make plan-gate` works against a restarted
  server too. Two known quirks of the rebuilt layout: HammerDB's final
  ANALYZE step fails and `ANALYZE <table>` inside db `tpch` errors
  "relation does not exist" (per-DB scoping gap in the ANALYZE path — see
  the deferral ledger row `bench-reorg ANALYZE-scope`; the gate runs S-cold
  regardless), and heavy queries at S-cold need GC headroom — Q21 drew a
  host-level OOM at `GOMEMLIMIT=18GiB` but completes at `GOGC=100` +
  `GOMEMLIMIT=12GiB`.
- **TPC-DS**: `bench/tpcds/README.md`. Env: `bench/tpcds/env_tpcds.sh`
  (single source of truth for dirs/ports). Lifecycle:
  `bench/tpcds/server.sh {start|stop|status} [sf1|sf05|pg|all]`.
  The SF=0.5 fast regression gate (`scripts/tpcds-sf05-regression.sh sweep`,
  ~1 h) checks goopg row counts against a **git-tracked PG oracle**
  (`bench/tpcds/runtime_goopg/tpcds-results-sf05/oracle.txt`) and needs no
  PG instance.

Row-count anchors are **load-dependent**: `bench/tpch/spotcheck_expected.env`
(Q12/Q13) and `ci/batch/tpch-row-anchors.csv` are pinned to a specific HammerDB
load and must be re-pinned after any TPC-H reload; the TPC-DS SF0.5 oracle is
re-captured only when the dataset or query files change.

## Running a server manually

Always through the cgroup memory cap (WSL2 OOM containment):
`GOOPG_CG_UNIT=<name> scripts/goopg-test-run.sh ./bin/goopg start -D <dir>
--listen 127.0.0.1:<port>`. The flag is `--listen`, not `-p`. Use ports
5533/5534 for throwaway experiments; the 6543x block is allocated per the table
above. **Never `pkill -f goopg`** — it self-matches the invoking shell (exit
144); stop via `goopg stop -D <dir>` or the lifecycle scripts.

Benchmark-timing hygiene (all bitten in practice): hold server age constant in
any A/B (a goopg server that just ran a timeout query sits at GOMEMLIMIT with
GOGC=off and thrashes GC — "sweep-tail collapse" mimics a regression);
`timeout N psql` kills only the client, the server keeps executing — reap
orphans, and materialize the victim set before `pg_terminate_backend`
(`WITH victims AS MATERIALIZED (…)`).

## Test gates

- Manual pre-commit bar: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
  (unit/component suite). The git hook runs the pgbench smoke on EVERY commit —
  never `git commit --no-verify`.
- Planner/executor changes additionally: `scripts/tpch-spotcheck.sh` (fresh
  capped server + canonical Q12/Q13 row counts) and the TPC-DS SF0.5 gate.
- **Never pass `-count=1` to a gate's `go test`** — it defeats the test-result
  cache (~5 min warm vs ~40 min cold). `-count=1` is for one-off probes only.

## Git

- Commit style: `area(scope): summary — detail`. Stage by explicit pathspec
  (a concurrent Ralph loop's WIP may be present; never `git add -A`).
- Repo gofmt baseline is go1.25 — never run `gofmt -w` wholesale (a newer local
  gofmt rewrites unrelated lines); match formatting manually.
