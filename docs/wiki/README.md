# goopg

goopg is an experimental PostgreSQL-compatible database server written in Go —
a from-scratch reimplementation of PostgreSQL 18.3, built agent-first as a study
in whether coding agents can produce a correct, maintainable stateful system
while staying behaviourally aligned with upstream. It targets x86-64 Linux
(developed under WSL2) and is research-oriented, not production-use.

This wiki documents the **reference** layer (what/how). Strategic design
rationale ("why") lives in the repo's own `docs/design/` bundles.

## Key Concepts

- **Volcano iterator model** — every plan node executes as `Open → Next → EOF →
  Close`, mirroring PostgreSQL's executor.
- **Two-engine executor** — a legacy `Operator`-tree builder and a
  GC-pointer-free "fast slab" builder; the fast path is the live server entry.
- **PG-shaped join search** — a faithful reproduction of
  `standard_join_search` level lists, capped at 16 base relations per join problem.
- **Byte-compatible on-disk format** — 8 KiB pages, heap tuple headers, and
  WAL that vanilla PG 18.3 can consume (and vice versa).
- **Physical + logical replication** — streaming WAL and `pgoutput`-based
  logical replication, interoperable with PG 18.3.
- **Bootstrap + recovery** — a data directory that is both generated from
  scratch (`init`) and rebuilt into an in-memory catalog by replaying WAL and
  scanning the on-disk catalog heaps (`open`).

## Entry Points

- `cmd/goopg/main.go` — the single binary: `init` / `start` / `stop` /
  `restart` / `reload` / `checkpoint` / `promote` / `status` / `version`.
- `cmd/goopg/standby.go` — streaming-standby lifecycle and promotion.

## High-Level Architecture

A client connects over the PostgreSQL v3 wire protocol to the `postmaster`,
which runs one goroutine per connection. SQL is parsed (`internal/parser`),
planned (`internal/optimizer`), and executed (`internal/executor`) against a
buffer pool and storage manager (`internal/storage`). The data directory is
bootstrapped and recovered by `internal/initdb`, and WAL is streamed to
standbys by `internal/replication`. The whole thing is orchestrated by the
`cmd/goopg` binary.

See [architecture.md](architecture.md) for the system diagram and data flow.

## Module Map

| Module | Purpose |
|---|---|
| [`cmd/goopg`](modules/cmd-goopg.md) | The server binary: CLI lifecycle, foreground startup, control plane, standby. |
| [`internal/postmaster`](modules/postmaster.md) | Server process: TCP listener, per-connection backend goroutines, v3 wire protocol, SQL dispatch, COPY. |
| [`internal/optimizer`](modules/optimizer.md) | SQL planner: statement dispatch, subquery unnesting, join-order search, cardinality, plan IR. |
| [`internal/executor`](modules/executor.md) | Execution engine: operators, expression evaluation, DML/DDL, stored routines. |
| [`internal/storage`](modules/storage.md) | Buffer manager, storage manager, heap/tuple layer, FSM/VM, lock manager, AIO. |
| [`internal/initdb`](modules/initdb.md) | Cluster bootstrap + startup recovery: data-dir creation, WAL replay, catalog heap reload. |
| [`internal/replication`](modules/replication.md) | Streaming + logical replication: walsender/walreceiver, apply launcher, table sync. |

Adjacent packages referenced throughout but **out of this wiki pass's scope**:
`internal/parser`, `internal/catalog`, `internal/nodes`, `internal/libpq`,
`internal/access/{transam,xlog,nbtree,amcheck}`, `internal/commands`,
`internal/utils`, `internal/pl`.

## Getting Started

See [getting-started.md](getting-started.md).
