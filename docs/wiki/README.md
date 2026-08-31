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

## Diagrams

- [Class diagram](diagrams/class-diagram.md) — the central structs/interfaces
  and their composition (optimizer → executor → storage, plus replication).
- [Sequence diagrams](diagrams/sequences.md) — simple-query lifecycle, startup
  recovery / catalog reload, and streaming replication.
- [Additional diagrams](diagrams/additional.md) — WAL append-and-flush flow,
  query plan-to-execution flow, and DDL catalog-heap sync flow.

## Module Map

| Module | Purpose |
|---|---|
| [`cmd/goopg`](modules/cmd-goopg.md) | The server binary: CLI lifecycle, foreground startup, control plane, standby. |
| [`internal/postmaster`](modules/postmaster.md) | Server process TCP listener, per-connection goroutines, v3 wire protocol, SQL dispatch, COPY. |
| [`internal/parser`](modules/parser.md) | SQL parser / lexer / analyzer: goyacc LALR(1) grammar + hand-written DDL parsers. |
| [`internal/optimizer`](modules/optimizer.md) | SQL planner statement dispatch, subquery unnesting, join-order search, cardinality, plan IR. |
| [`internal/nodes`](modules/nodes.md) | Plan/expression node IR: serializer/deserializer pair, PG `transformExpr`-style resolver. |
| [`internal/executor`](modules/executor.md) | Execution engine operators, expression evaluation, DML/DDL, stored routines. |
| [`internal/catalog`](modules/catalog.md) | System-catalog layer: `InMemory` registry, virtual + heap-backed catalogs, codec, OID allocator. |
| [`internal/libpq`](modules/libpq.md) | PostgreSQL v3 wire protocol: framing, message writers, authentication (SCRAM/MD5/trust). |
| [`internal/storage`](modules/storage.md) | Buffer manager, storage manager, heap/tuple layer, FSM/VM, lock manager, AIO. |
| [`internal/access/transam`](modules/transam.md) | Transaction manager: MVCC snapshots, clog, subxact, multixact, SSI, XID generation. |
| [`internal/access/transam/xlog`](modules/xlog.md) | Write-ahead logging: WAL format, append/stripe/emit, recovery/replay, physical+logical decoding. |
| [`internal/access/nbtree`](modules/nbtree.md) | B-tree index access method: search, insert, split, dedup, posting lists, PG on-disk format. |
| [`internal/initdb`](modules/initdb.md) | Cluster bootstrap + startup recovery: data-dir creation, WAL replay, catalog heap reload. |
| [`internal/replication`](modules/replication.md) | Streaming + logical replication: walsender/walreceiver, apply launcher, table sync. |
| [`internal/access/amcheck`](modules/amcheck.md) | Index/table verification: heap all-indexed checks, nbtree verification, bloom filter. |
| [`internal/access/common/pglz`](modules/pglz.md) | PGLZ LZ compression: inline-compressed varlena codec (PG `pg_lzcompress.c` port). |
| [`internal/backup`](modules/backup.md) | Physical backup: `pg_basebackup`-compatible streaming tar output, manifest. |
| [`internal/commands/vacuum`](modules/vacuum.md) | VACUUM: dead-tuple reclamation, tuple freeze, VM update, tail truncation. |
| [`internal/pl/plpgsql`](modules/plpgsql.md) | PL/pgSQL language parser/AST: function body parser, statement AST types. |
| [`internal/utils`](modules/utils.md) | Utility packages: GUC registry, datetime/interval formatting, encoding, activity tracking. |
| [`internal/port`](modules/port.md) | Platform runtime: nanotime, semaphore, process pinning — Linux `linkname` shims + fallbacks. |

## Getting Started

See [getting-started.md](getting-started.md).