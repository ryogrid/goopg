# goopg

goopg is an experimental project that explores whether a coding-agent-driven
Go implementation can reproduce PostgreSQL behavior when PostgreSQL is treated
as the oracle for correctness.

The project focuses on three validation themes:

1. Feasibility of agent-driven implementation:
	can coding agents build and evolve a meaningful PostgreSQL-like server in
	Go while staying behaviorally aligned with upstream PostgreSQL?
2. Performance characteristics under multithreading:
	how do throughput and latency change as execution paths become more
	concurrent?
3. Effects of direct I/O:
	what trade-offs appear when storage paths use direct I/O compared with
	buffered I/O?

This repository is research-oriented and intentionally iterative. It is meant
for experimentation, measurement, and learning, rather than production use.

For oracle-based verification, this repository includes the upstream PostgreSQL
repository as a submodule under postgres/. The current reference codebase is
pinned to REL_18_3.

## Quickstart

The lifecycle below — build, init, start, connect with psql, stop, drop —
is driven from the top-level `Makefile`. Run `make help` for the full list
of targets and overridable variables.

### Prerequisites

- Go (matching `go.mod`'s toolchain).
- A locally built upstream PostgreSQL client toolchain at
  `postgres/local_install/`. The Makefile expects:
  - `postgres/local_install/bin/` — `psql`, `pg_ctl`, etc.
  - `postgres/local_install/lib/` — the matching shared libraries
    (`libpq.so*`, ICU, …).

  If you have not built it yet, build the `postgres/` submodule with
  `--prefix=$(pwd)/local_install` and `make install` in `postgres/` directory.
  The Makefile only needs the client tools and
  their shared libraries; the upstream `postgres` server binary is not
  used at runtime.

### Environment for the in-tree PostgreSQL client tools

`psql` and the rest of the client tools under `postgres/local_install/bin`
load shared libraries from `postgres/local_install/lib`, which is **not**
on the system loader path. Every Makefile target that invokes a client
tool prepends both directories itself; if you run any of the steps
manually, prepend them explicitly:

```bash
export PATH="$PWD/postgres/local_install/bin:$PATH"
export LD_LIBRARY_PATH="$PWD/postgres/local_install/lib:${LD_LIBRARY_PATH:-}"
# macOS users: also set DYLD_LIBRARY_PATH to the same value as LD_LIBRARY_PATH.
```

`make print-env` prints the exact lines this Makefile uses.

### One-shot lifecycle via make

```bash
make build          # → ./bin/goopg
make init           # → tmp/goopg-data/ (override with DATA_DIR=...)
make start          # background server on 127.0.0.1:5432; log: tmp/goopg.log
make psql           # connect with the in-tree psql
make stop           # graceful shutdown
make clean-data     # remove the data directory
# optionally: make clean   # also removes ./bin/goopg
```

Common overrides:

```bash
make start LISTEN=0.0.0.0:55432 DATA_DIR=/tmp/my-cluster
make psql  LISTEN=0.0.0.0:55432 DATA_DIR=/tmp/my-cluster PSQL_DBNAME=postgres
```

### Equivalent raw commands

For the record, the same flow without the Makefile:

```bash
# 1. Build.
go build -o ./bin/goopg ./cmd/goopg

# 2. Initialize the data directory.
./bin/goopg init -D ./tmp/goopg-data

# 3. Start the server. `goopg start` runs in the foreground; either run
#    it in a dedicated terminal or background it as the Makefile does.
./bin/goopg start -D ./tmp/goopg-data --listen 127.0.0.1:5432

# 4. In another terminal, with the in-tree client toolchain on PATH and
#    LD_LIBRARY_PATH (see "Environment" above), connect:
psql -h 127.0.0.1 -p 5432 -U postgres -d postgres

# 5. Stop the server gracefully (from any terminal where the Makefile
#    environment or the binary's path is reachable):
./bin/goopg stop -D ./tmp/goopg-data

# 6. Drop the cluster.
rm -rf ./tmp/goopg-data
```

`goopg start` exits when it receives `SIGINT` / `SIGTERM` or when
`goopg stop -D <datadir>` requests a shutdown over the control socket.

## Active Development Milestones

| Milestone | Title | Status |
|-----------|-------|--------|
| 0053 | HammerDB TPC-H complete-run verification | landed |
| 0054 | TPC-H performance and optimisation | in progress |
| 0055 | Staged B-tree enhancement program | largely landed |
| 0056 | Buffer-pool PinNew race fix + splitMu removal | in progress |
| 0057 | TPC-H measurement prerequisites | planned |
| 0066 | Executor runtime allocation reductions (PIVOT) | landed (`perf-analysis`) |
| 0067 | TPC-H structural runtime — 1200 s baseline + diagnostics | landed (`perf-analysis`) |
| **0068** | **Executor GC-Optimized Pipeline Refactor** | **planned (`perf-analysis`)** |

Milestone 0057 addresses the benchmark infrastructure prerequisites —
background-worker visibility, checkpoint suppression, crash recovery,
and per-query cancellation — needed for reliable M0054-0007 runs.
See `docs/milestones/0057-tpch-measurement-prerequisites.md` for the
full plan and `cmd/tpch-runner/README.md` for the query-runner tool.

Milestone 0068 is a **large-scale executor refactor** targeting GC
overhead and live-heap pointer density. M0066 PIVOT eliminated 99.23 %
of Q5's allocations (`multiHashJoinOp.copyOut`, 2.02 TB on a 60 s pprof
window) and removed `time.Parse` from hot loops; Q5 / Q20 still cancel
at `cancel-after=1200s` because residual cost is **memory-copy bound**
(`runtime.duffcopy` + `memclr` ≈ 60 % of CPU). M0068 replaces
`Row = []Datum` with a PostgreSQL-style `TupleSlot` polymorphism
(Materialized / Virtual / BatchRef), shrinks `Datum` from ~120 to
≤ 48 bytes, introduces a per-batch byte arena for variable-length
payload, and pools slots across queries via `sync.Pool`. The
`BorrowSemantics` row-level contract introduced in M0054-0005a /
M0059 is **removed** in favor of slot-intrinsic lifetime semantics.
See `docs/milestones/0068-executor-gc-pipeline-refactor.md` and the
four design docs `docs/design/0068-000{1..4}-*.md` for the detailed
plan; sources: `practice/go_gc_optimized_programming.md` and
`review/postgres_vs_goopg_performance_divergence.md` §1.
