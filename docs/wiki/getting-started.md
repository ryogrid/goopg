# Getting Started

goopg is an experimental PostgreSQL-compatible database server written in Go.
This page covers building, initializing, starting, connecting to, and stopping
a goopg cluster, plus running its tests. It targets **x86-64 Linux only** (the
project is developed and tested under WSL2).

## Prerequisites

- **Go** — matching the `toolchain` directive in [`go.mod`](../../go.mod)
  (`go 1.25.0`).
- **A locally built upstream PostgreSQL client toolchain** under
  `postgres/local_install/`:
  - `postgres/local_install/bin/` — `psql`, `pg_ctl`, etc.
  - `postgres/local_install/lib/` — matching shared libraries (`libpq.so*`, ICU, …)

  The upstream `postgres/` submodule is the read-only oracle; only its **client
  tools and libraries** are needed at runtime (the upstream `postgres` server
  binary is not used). If it is not built yet, build it with
  `--prefix=$(pwd)/local_install` and run `make install` inside `postgres/`.

### Environment for the in-tree client tools

`psql` and friends load shared libraries from `postgres/local_install/lib`,
which is not on the system loader path. Prepend both directories before
invoking any client tool manually:

```bash
export PATH="$PWD/postgres/local_install/bin:$PATH"
export LD_LIBRARY_PATH="$PWD/postgres/local_install/lib:${LD_LIBRARY_PATH:-}"
```

`make print-env` prints the exact lines the Makefile uses.

## Installation / Build

```bash
make build          # → ./bin/goopg
```

Or the raw equivalent:

```bash
go build -o ./bin/goopg ./cmd/goopg
```

## First Run (one-shot lifecycle via `make`)

```bash
make init           # initialize a data dir at tmp/goopg-data/  (override: DATA_DIR=...)
make start          # background server on 127.0.0.1:5432; log: tmp/goopg.log
make psql           # connect with the in-tree psql
make stop           # graceful shutdown
make clean-data     # remove the data directory
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

`goopg start` exits on `SIGINT`/`SIGTERM`, or when `goopg stop -D <datadir>`
requests a shutdown over the control socket.

Common overrides:

```bash
make start LISTEN=0.0.0.0:55432 DATA_DIR=/tmp/my-cluster
make psql  LISTEN=0.0.0.0:55432 DATA_DIR=/tmp/my-cluster PSQL_DBNAME=postgres
```

## Running tests

```bash
# Unit tests (fast, no server needed)
go test ./...

# End-to-end replication tests (requires postgres/ client tools on PATH)
export PATH="$PWD/postgres/local_install/bin:$PATH"
export LD_LIBRARY_PATH="$PWD/postgres/local_install/lib:$LD_LIBRARY_PATH"
go test ./internal/testport/... -v -timeout 300s
```

> The project also maintains heavier gates (TPC-H spot-check, TPC-DS SF0.5
> regression sweep, race gate, nightly batch). Those are Ralph-loop gates, not
> part of the day-to-day build; see the `Makefile` targets
> (`tpch-spotcheck`, `race-gate`, `nightly-batch`, …) for details.

## Configuration

- **Data directory** — `-D <dir>` (or `DATA_DIR=` in make). Holds the on-disk
  catalogs, heap files, and WAL.
- **Listen address** — `--listen <host>:<port>` (the flag is `--listen`, not
  `-p`).
- **`postgresql.conf` / GUCs** — goopg parses a `postgresql.conf` and supports
  `SHOW` / `SET` / `RESET` for runtime GUCs (defaults match PostgreSQL 18).
- **Authentication** — `pg_hba.conf` with `trust`, `reject`, `md5`,
  `scram-sha-256`.

## Memory-capped execution (WSL2 OOM containment)

On the WSL2 dev box, a runaway goopg can thrash swap and trip the system-wide
OOM killer. Run servers through the per-run cgroup cap (`scripts/goopg-test-run.sh`
wraps `systemd-run --user --scope`):

```bash
GOOPG_CG_UNIT=goopg-test scripts/goopg-test-run.sh \
    ./bin/goopg start -D tmp/goopg-data --listen 127.0.0.1:5533
```

`make start` / `make goopg-test-server` are already capped. Stop a backgrounded
scope with `systemctl --user stop <unit>.scope`.

## Where to Go Next

- [architecture.md](architecture.md) — system shape and data flow.
- [README.md](README.md#module-map) — module map with one-line purposes.
- `modules/*.md` — per-module deep-dives for the six core engine packages and
  the `cmd/goopg` entry point.
