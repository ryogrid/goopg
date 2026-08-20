[![Run Tests](https://github.com/ryogrid/goopg/actions/workflows/test.yml/badge.svg)](https://github.com/ryogrid/goopg/actions/workflows/test.yml)

# goopg

goopg is an experimental PostgreSQL-compatible database server written in Go.
It is driven entirely by coding agents as a study in agent-led implementation:
can an AI agent build and evolve a meaningful PostgreSQL-like server while
staying behaviourally aligned with upstream PostgreSQL?

The project currently targets **x86-64 Linux only** (developed and tested under
WSL2); other platforms and architectures are out of scope for now.

The project has three research axes:

1. **Agent-driven implementation** — can coding agents produce correct,
   maintainable Go code for a complex stateful system, using PostgreSQL 18 as
   the behavioural oracle?
2. **Go concurrency characteristics** — how do throughput and latency scale as
   execution paths become more concurrent?
3. **Direct I/O trade-offs** — what happens when storage paths bypass the
   OS page cache?

This repository is research-oriented and intentionally iterative. It is not
intended for production use.

The upstream PostgreSQL repository (REL_18_3) is included as a submodule
under `postgres/` and is used as the reference for correctness.

---

## Implemented Features

> **A note on "implemented":** a feature listed as implemented exists and is
> exercised, but that does **not** necessarily mean the implementation is
> complete or exhaustive — many features are partial or carry known gaps. For a
> per-feature breakdown of what is implemented, deferred, or missing, see
> [`docs/reference/coverage_table.csv`](docs/reference/coverage_table.csv).

The highlights below are a non-exhaustive excerpt of the major subsystems:

### Wire Protocol & Connection
- PostgreSQL wire protocol v3 (simple and extended query modes)
- Authentication via `pg_hba.conf`: `trust`, `reject`, `md5`, `scram-sha-256`
- GUC / `postgresql.conf` parser with `SHOW`, `SET`, `RESET`
- `pg_stat_activity` system view

### Storage & MVCC
- 8 KB heap pages in PG18-compatible on-disk format (PageHeaderData, ItemId,
  HeapTupleHeaderData)
- MVCC with snapshot isolation (READ COMMITTED and REPEATABLE READ)
- HOT (Heap-Only Tuple) updates — avoids index updates when non-indexed columns change
- Visibility Map — tracks all-visible pages for Index-Only Scan eligibility
- Index-Only Scan (single-column B-tree)
- Opportunistic page pruning — reclaims dead tuple chains inline during HOT updates
- VACUUM with tuple freeze (prevents XID wraparound)
- Autovacuum background worker
- Free Space Map (FSM) — guides INSERT to pages with sufficient free space
- Buffer pool with clock-sweep eviction
- Async I/O engine (AIO) for prefetch and parallel page reads

### Indexes
- B-tree index on integer, numeric, text, date, timestamp, boolean columns
- Unique constraint enforcement via B-tree
- Primary key constraint
- `CREATE INDEX`, `DROP INDEX`
- Index scan, range scan, index-only scan

### Query Engine
- Full SQL parser (SELECT, INSERT, UPDATE, DELETE, COPY FROM)
- Planner: sequential scan, index scan, index-only scan, hash join,
  nested-loop join, sort, aggregate, limit, project
- Multi-way bushy hash join (spill-to-disk for joins that exceed memory)
- Correlated subquery optimization and IN-unnesting
- Join-order reordering with cost-based cardinality estimates
- MCV histograms and per-column statistics (ANALYZE)
- Window functions: ROW_NUMBER, RANK, LAG, LEAD
- CTEs (WITH clause, non-recursive)
- Subqueries (EXISTS, NOT EXISTS, scalar, lateral)
- UPSERT: `INSERT ... ON CONFLICT DO UPDATE`
- SELECT FOR UPDATE / FOR SHARE (pessimistic row locking)
- Aggregates: COUNT, SUM, AVG, MIN, MAX, ARRAY_AGG, STRING_AGG
- Type coercions, CASE/WHEN, CAST, string/date/numeric operators
- JSON / JSONB data type support
- LIMIT / OFFSET / ORDER BY / GROUP BY / HAVING
- EXPLAIN and EXPLAIN ANALYZE

### Transactions & Concurrency
- MVCC with per-backend ProcArray snapshot
- SAVEPOINT / ROLLBACK TO SAVEPOINT
- Deadlock detection
- Serializable isolation (SSI anomaly prevention) — partial
- Read-only commit skip (no WAL emit for SELECT-only transactions)

### WAL & Durability
- WAL writer with PG18-compatible on-disk format (pg_waldump-readable)
- Checkpoint with fsync
- Crash recovery from WAL
- WAL segment preallocation
- Concurrent WAL append (M0026)

### Replication
- **Physical (streaming) replication**: goopg→goopg and goopg↔PG18 (async and sync)
- **Logical replication** (pgoutput): goopg→PG18 and PG18→goopg (async and sync)
- Replication slots, walsender, walreceiver
- Standby promotion
- 9 replication patterns verified end-to-end (see `internal/testport/`)

### Catalog & DDL
- `pg_class`, `pg_attribute`, `pg_index` heap tables in PG18-compatible format
- `pg_namespace`, `pg_type`, `pg_proc` views
- `CREATE TABLE`, `DROP TABLE`, `ALTER TABLE`
- `CREATE SEQUENCE`, `nextval`/`currval`
- `CREATE VIEW`, `DROP VIEW`
- `CREATE INDEX`, `DROP INDEX`
- `CREATE PUBLICATION`, `CREATE SUBSCRIPTION`
- Catalog recovery from heap on startup (no JSON side-channel)
- `pg_internal.init` relcache init file written for PG standby fast-start

### Procedural SQL
- `plpgsql` stored procedures / functions (incl. `EXCEPTION` blocks)
- Triggers on INSERT / UPDATE / DELETE

### Benchmark Coverage
- All 22 HammerDB TPC-H queries pass (SF=1)
- pgbench: standard, simple-update, select-only workloads

---

## Project Status

goopg is an ongoing experiment; the current state in brief:

- **Concurrency model** — already implemented with thread-level parallelism: a
  single process running goroutines, rather than PostgreSQL's multi-process
  backend model.
- **Direct I/O** — not yet implemented. It proved harder than initially
  expected, so storage still goes through the OS page cache.
- **Performance** — on OLTP, `pgbench` reaches roughly half of vanilla
  PostgreSQL's throughput. OLAP workloads (TPC-H, TPC-DS) run end-to-end, but
  most queries are several to tens of times slower than PostgreSQL.
- **Replication** — goopg can form a streaming-replication pair with vanilla
  PostgreSQL 18.3, and even keeps working when the two sides' data-cluster
  directories are swapped; a notable result for a from-scratch engine.

---

## PostgreSQL Test Coverage

goopg tracks its compatibility against upstream PostgreSQL 18.3 by porting
that release's own test suites and running them against goopg instead of
real PG. The live, generated snapshot of this coverage — current pass/fail
counts per suite — is
[`docs/test-port/postgres-oracle-target-inventory.md`](docs/test-port/postgres-oracle-target-inventory.md),
rendered from the authoritative per-case CSV
[`docs/test-port/postgres-oracle-target-inventory.csv`](docs/test-port/postgres-oracle-target-inventory.csv)
(schema and status vocabulary in
[`docs/test-port/README.md`](docs/test-port/README.md)). Each row there is an
individual upstream test case, grouped by a `suite_id` naming which upstream
suite it came from:

| `suite_id` | What it covers |
| --- | --- |
| `regress-sql` | `postgres/src/test/regress/sql/*.sql` — the core SQL-level regression suite (`pg_regress`): one case per script, exercising a feature area end to end (types, joins, DDL, functions, …). This is the primary correctness suite and the largest single bucket. |
| `regress-expected` | `postgres/src/test/regress/expected/*.out` — the expected-output files pg_regress diffs against. Most cases have exactly one `.sql`/`.out` pair, so their outcome is tracked once under `regress-sql`; this bucket holds only the upstream *alternate-output* variants that have no `.sql` of their own (pg_regress picks between them by platform/encoding/collation, e.g. `char_1.out`, `xml_2.out`, `collate.icu.utf8_1.out`). |
| `isolation-specs` | `postgres/src/test/isolation/specs/*.spec` — the multi-session isolation-tester suite: concurrent-session interleaving specs that exercise MVCC, locking, and serializability behavior. |
| `recovery-tap` | `postgres/src/test/recovery/t/*.pl` — TAP tests for crash recovery, WAL replay, and physical (streaming) replication. |
| `subscription-tap` | `postgres/src/test/subscription/t/*.pl` — TAP tests for logical replication (`CREATE SUBSCRIPTION`/`CREATE PUBLICATION`, `pgoutput`). |
| `client-tools-tap` | `postgres/src/bin/*/t/*.pl` — TAP tests for the client-side tools themselves (`psql`, `pg_dump`, `pg_basebackup`, `initdb`, …) run against a goopg server. |
| `modules-suites` | `postgres/src/test/modules/*` — the in-tree PostgreSQL test/extension modules (each a feature-focused mini test suite). |
| `contrib-suites` | `postgres/contrib/*` — the bundled contrib extensions' own test suites (each contrib module ships its own regress-style tests). |

A case's `status` (`pass` / `failed` / `not-tried` / `port` / `defer` /
`excluded`) and whether it is part of the must-pass set (`pass_required`) are
also defined in `docs/test-port/README.md`; the generated inventory doc is
regenerated with `make regen-testport` whenever the CSV changes, so it never
drifts from the authoritative data.

---

## Quickstart

The lifecycle below — build, init, start, connect with psql, stop, drop —
is driven from the top-level `Makefile`. Run `make help` for the full list
of targets and overridable variables.

### Prerequisites

- Go (matching `go.mod`'s toolchain directive).
- A locally built upstream PostgreSQL client toolchain under
  `postgres/local_install/`. The Makefile expects:
  - `postgres/local_install/bin/` — `psql`, `pg_ctl`, etc.
  - `postgres/local_install/lib/` — matching shared libraries (`libpq.so*`, ICU, …)

  If you have not built it yet, build the `postgres/` submodule with
  `--prefix=$(pwd)/local_install` and run `make install` inside `postgres/`.
  Only the client tools and libraries are needed; the upstream `postgres` server
  binary is not used at runtime.

### Environment for the in-tree PostgreSQL client tools

`psql` and other client tools under `postgres/local_install/bin` load shared
libraries from `postgres/local_install/lib`, which is not on the system loader
path. Every Makefile target that invokes a client tool prepends both
directories. If you run steps manually, prepend them explicitly:

```bash
export PATH="$PWD/postgres/local_install/bin:$PATH"
export LD_LIBRARY_PATH="$PWD/postgres/local_install/lib:${LD_LIBRARY_PATH:-}"
# macOS: also set DYLD_LIBRARY_PATH to the same value.
```

`make print-env` prints the exact lines the Makefile uses.

### One-shot lifecycle via make

```bash
make build          # → ./bin/goopg
make init           # → tmp/goopg-data/  (override: DATA_DIR=...)
make start          # background server on 127.0.0.1:5432; log: tmp/goopg.log
make psql           # connect with the in-tree psql
make stop           # graceful shutdown
make clean-data     # remove the data directory
# optionally:
make clean          # also removes ./bin/goopg
```

Common overrides:

```bash
make start LISTEN=0.0.0.0:55432 DATA_DIR=/tmp/my-cluster
make psql  LISTEN=0.0.0.0:55432 DATA_DIR=/tmp/my-cluster PSQL_DBNAME=postgres
```

### Equivalent raw commands

```bash
# 1. Build.
go build -o ./bin/goopg ./cmd/goopg

# 2. Initialize the data directory.
./bin/goopg init -D ./tmp/goopg-data

# 3. Start the server in one terminal.
./bin/goopg start -D ./tmp/goopg-data --listen 127.0.0.1:5432

# 4. Connect from another terminal (with PATH / LD_LIBRARY_PATH set as above).
psql -h 127.0.0.1 -p 5432 -U postgres -d postgres

# 5. Stop the server.
./bin/goopg stop -D ./tmp/goopg-data

# 6. Drop the cluster.
rm -rf ./tmp/goopg-data
```

`goopg start` exits when it receives `SIGINT`/`SIGTERM` or when
`goopg stop -D <datadir>` requests a shutdown over the control socket.

### Running tests

```bash
# Unit tests (fast, no server needed)
go test ./...

# End-to-end replication tests (requires postgres/ client tools on PATH)
export PATH=$PWD/postgres/local_install/bin:$PATH
export LD_LIBRARY_PATH=$PWD/postgres/local_install/lib:$LD_LIBRARY_PATH
go test ./internal/testport/... -v -timeout 300s
```

---

## Repository Layout

First level only (git-tracked directories; dot-directories and scratch
directories such as `tmp/` are omitted):

```
analysis/             investigation and performance-analysis notes
bench/                TPC-H / TPC-DS / pgbench benchmark harnesses
ci/                   CI workflows and nightly batch jobs
cmd/                  Go entry point (CLI: init / start / stop / promote)
docs/                 design docs, milestone tracking, reference material
internal/             all backend packages (parser, planner, executor, storage, wal, mvcc, …)
maintenance_prompts/  housekeeping scripts for the Ralph tracker files
postgres/             upstream PostgreSQL 18.3 source (submodule; read-only oracle)
practice/             research notes and reference material
scripts/              build / test / benchmark helper scripts
third-party/          vendored third-party code (e.g. tpcds-postgres submodule)
tools/                auxiliary developer tooling (gocomplexity, mdtablefix, …)
wp/                   WordPress compatibility test fixture
```
