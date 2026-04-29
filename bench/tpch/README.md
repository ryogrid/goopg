# TPC-H Benchmark (HammerDB → PostgreSQL)

Driver scripts that run the TPC-H workload from HammerDB 5.0 against a
locally-built PostgreSQL 18.3 (under `postgres/local_install`) at the
smallest scale factor (SF=1, ≈1 GB).

No server-side stored procedures are used: TPC-H in HammerDB sends the
schema DDL, the bulk-load `INSERT` / `COPY` statements, and the 22 power-test
queries straight from the client. The `pg_storedprocs` toggle exists only
for TPC-C, not TPC-H.

## Prerequisites

- `postgres/local_install/bin/{initdb,pg_ctl,psql,…}` already built.
- `HammerDB-5.0/` extracted at the repo root from
  `HammerDB-5.0-Prod-Lin-UBU22.tar.gz`.
- ~2 GB free disk for the SF=1 cluster + WAL.

The scripts source `env.sh`, which prepends `postgres/local_install/bin`
to `PATH` and `postgres/local_install/lib` to `LD_LIBRARY_PATH` so that
`psql` resolves the bundled `libpq.so.5.18` (the system libpq is older
and lacks symbols like `PQsendPipelineSync`).

## Layout

```
bench/tpch/
├── env.sh                  # shared config (paths, port, credentials, SF)
├── setup_pg.sh             # initdb + pg_hba + postgresql.conf + start
├── stop_pg.sh              # pg_ctl stop -m fast (idempotent)
├── build_schema.sh         # HammerDB → build TPC-H schema (SF=1)
├── run_power_test.sh       # HammerDB → run Q1..Q22 (single-VU power test)
├── run_all.sh              # setup → build → power test → stop
├── tcl/
│   ├── build_schema.tcl    # HammerDB Tcl: schema build
│   └── run_power_test.tcl  # HammerDB Tcl: 22-query power test
├── logs/                   # build_<ts>.log / run_<ts>.log (per invocation)
└── runtime/                # ephemeral cluster state (PGDATA, sockets, tmp)
```

## Quick start

```bash
# Full pipeline: fresh cluster → load SF=1 → run Q1..Q22 → stop.
./bench/tpch/run_all.sh

# Same, but leave the cluster running so you can poke it with psql.
./bench/tpch/run_all.sh --keep-running
```

## Step-by-step usage

```bash
# 1. (Re)create and start the local cluster on 127.0.0.1:65432.
./bench/tpch/setup_pg.sh --reset       # --reset wipes runtime/pgdata first
./bench/tpch/setup_pg.sh               # without --reset, just (re)starts it

# 2. Build the TPC-H schema and load data at SF=1.
./bench/tpch/build_schema.sh

# 3. Run the 22-query power test (single virtual user).
./bench/tpch/run_power_test.sh

# 4. Stop the cluster when done.
./bench/tpch/stop_pg.sh
```

The cluster listens only on `127.0.0.1:65432`. Connect with the
locally-built `psql`:

```bash
PATH=postgres/local_install/bin:$PATH \
LD_LIBRARY_PATH=postgres/local_install/lib \
PGPASSWORD=tpch \
psql -h 127.0.0.1 -p 65432 -U tpch -d tpch
```

## Tunables

All knobs are environment variables; defaults live in `env.sh` and can be
overridden inline:

| Variable                  | Default        | Meaning                                                       |
|---------------------------|----------------|---------------------------------------------------------------|
| `PG_PORT`                 | `65432`        | Listen port for the temporary cluster.                        |
| `TPCH_SCALE_FACT`         | `1`            | TPC-H scale factor (smallest HammerDB accepts).               |
| `TPCH_BUILD_THREADS`      | `nproc`        | Parallel virtual users for the schema build.                  |
| `TPCH_TOTAL_QUERYSETS`    | `1`            | How many times to repeat Q1..Q22.                             |
| `TPCH_DEGREE_OF_PARALLEL` | `2`            | Sets `max_parallel_workers_per_gather` for the run session.   |

Example: run two query sets and double the build parallelism.

```bash
TPCH_TOTAL_QUERYSETS=2 TPCH_BUILD_THREADS=32 ./bench/tpch/run_all.sh
```

## Output

- **Per-query timings** stream to stdout *and* `bench/tpch/logs/run_<ts>.log`.
- **Schema build progress** streams to `bench/tpch/logs/build_<ts>.log`.
- **HammerDB job id** is written to `bench/tpch/runtime/tmp/pg_tproch_jobid`
  after each power-test run; HammerDB also persists the run details inside
  `runtime/tmp/hammer.DB` (a SQLite file you can inspect with
  `HammerDB-5.0/hammerdbcli`).
- **PostgreSQL server log**: `bench/tpch/runtime/postgres.log`.

A successful power-test tail looks like:

```
Vuser 1:Completed 1 query set(s) in 60 seconds
Vuser 1:Geometric mean of query times returning rows (22) is 1.27910
Vuser 1:FINISHED SUCCESS
```

## Cleaning up

The cluster lives entirely under `bench/tpch/runtime/`. To start over:

```bash
./bench/tpch/stop_pg.sh
rm -rf bench/tpch/runtime
```

Logs accumulate in `bench/tpch/logs/`; delete them at will.

## Notes / gotchas

- HammerDB hard-codes `./scripts/...` paths internally, so the wrapper
  scripts `cd HammerDB-5.0/` before invoking `hammerdbcli`.
- `pg_hba.conf` is rewritten by `setup_pg.sh` to require `scram-sha-256`
  for TCP connections (which is what HammerDB uses) while leaving local
  Unix-socket connections on `trust` for admin convenience.
- The bootstrap superuser is `postgres` / password `postgres`, matching
  HammerDB's stock TPC-H defaults; the workload role is `tpch` / `tpch`.
- HammerDB connects via the libpq compiled into its binary, not the one
  in `postgres/local_install/lib`, so the server only needs to be wire-
  protocol compatible (PostgreSQL 18.3 is fine).
