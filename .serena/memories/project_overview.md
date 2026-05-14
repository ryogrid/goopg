# goopg Project Overview

goopg is a from-scratch Go reimplementation of PostgreSQL targeting x86_64 Linux.

## Purpose
Full SQL database server compatible with the PostgreSQL wire protocol (v3), targeting PostgreSQL 18.x parity.

## Tech Stack
- Language: Go (≥1.22), no CGo unless unavoidable
- Storage: O_DIRECT, custom buffer pool, heap/btree access methods
- WAL: custom write-ahead log with crash recovery and streaming replication
- MVCC: snapshot-based visibility (no full clog in hot path; lightweight abortedXIDs list)
- Protocol: PostgreSQL wire protocol v3

## Repository Layout
```
cmd/goopg/          # entrypoint (init/start/stop/ctl)
internal/
  server/           # listener, connection lifecycle
  protocol/         # wire protocol framing
  config/           # GUC registry
  storage/          # buffer manager, page format, O_DIRECT
  wal/              # WAL writer, recovery, streaming replication
  mvcc/             # snapshot manager, visibility, transaction IDs
  catalog/          # system catalogs, pg_* views
  parser/           # SQL parser/analyzer
  planner/          # query planner
  executor/         # query executor, physical operators
  access/           # heap, btree
  auth/             # trust/password/md5/scram-sha-256
  initdb/           # data directory open/init (Open() is the main entry)
  testutil/         # cluster/replcluster test helpers
  testport/         # PostgreSQL oracle TAP test ports
docs/design/        # design docs (<milestone>-NNNN-slug.md)
postgres/           # READ-ONLY upstream PostgreSQL source (reference oracle)
.ralph/             # Ralph autonomous agent control files (DO NOT MODIFY)
```

## Module Path
`github.com/goopg/goopg`
