# 0103-0003 — pgoutput Wire-Byte Interoperability (PG ↔ goopg, Both Directions)

**Status:** draft
**Date:** 2026-05-13
**Milestone:** M0103-0004
**Upstream reference:** `postgres/src/backend/replication/pgoutput/pgoutput.c` (`pgoutput_change`, `pgoutput_begin`, `pgoutput_commit`, `pgoutput_message`), `postgres/src/backend/replication/logical/proto.c` (`logicalrep_write_insert`, `logicalrep_write_update`, `logicalrep_write_delete`, `logicalrep_write_rel`, `logicalrep_read_*`), `postgres/src/include/replication/logicalproto.h` (message format constants).

## Problem

goopg's pgoutput encoder (`internal/wal/pgoutput.go`) and decoder
(`internal/wal/pgoutput_decoder.go`) have been exercised only through an
**in-process pipeline** (`TestE2E_LogicalReplication`, in-process subscription
TAP ports). Whether goopg's emitted bytes are decodable by PostgreSQL's apply
worker, and whether goopg's decoder accepts PG's emitted bytes, is unverified.

A round-trip mismatch would silently break heterogeneous replication during
the M0103 E2E tests. We need a focused interop verification before the
failover tests, so that a downstream failure is unambiguous about cause.

## Upstream message-format reference

From `postgres/src/include/replication/logicalproto.h` (PG18):

| Msg type | Byte | Wire payload |
|---|---|---|
| Begin | `B` | final_lsn (8) + commit_ts (8) + xid (4) |
| Commit | `C` | flags (1) + commit_lsn (8) + end_lsn (8) + commit_ts (8) |
| Relation | `R` | relid (4) + ns_name (cstring) + rel_name (cstring) + replica_ident (1) + ncols (2) + per-col [ flags (1) + col_name (cstring) + atttypid (4) + atttypmod (4) ] |
| Insert | `I` | relid (4) + 'N' + tuple |
| Update | `U` | relid (4) + ['K'|'O' + old tuple]? + 'N' + new tuple |
| Delete | `D` | relid (4) + ['K'|'O' + old tuple] |
| Truncate | `T` | nrels (4) + flags (1) + relid[ ] |
| Type | `Y` | typid (4) + ns_name (cstring) + type_name (cstring) |
| Origin | `O` | origin_lsn (8) + name (cstring) |
| Message | `M` | flags (1) + lsn (8) + prefix (cstring) + length (4) + bytes |

Tuple wire form (per `proto.c::logicalrep_read_tuple`):

```
ncols (2) | per-col [ kind (1: 'n'|'u'|'t'|'b') + (kind=='t': len (4) + value_bytes)? ]
```

Where:
- `'n'` = NULL
- `'u'` = unchanged TOAST
- `'t'` = text-formatted value
- `'b'` = binary-formatted value (PG14+; binary format)

## Solution

### Two new interop test functions

In `internal/testport/pgoutput_interop_test.go`:

**(a) `TestPort_PgoutputInteropPGToGoopg` — goopg consumes PG's pgoutput.**

1. Spawn PG primary via `pgcluster.New` (M0102's package).
2. PG primary runs:
   ```sql
   CREATE TABLE t (id int PRIMARY KEY, v text);
   CREATE PUBLICATION p FOR ALL TABLES;
   ```
3. Open a raw replication-protocol connection to PG (via `pgconn` from pgx,
   or a hand-rolled `net.Conn` doing the startup + replication handshake).
4. Issue `CREATE_REPLICATION_SLOT goopg_test LOGICAL pgoutput;` (this
   creates a logical slot on PG).
5. PG primary runs INSERT/UPDATE/DELETE.
6. Issue `START_REPLICATION SLOT goopg_test LOGICAL 0/0 (proto_version '1',
   publication_names 'p')`.
7. Consume CopyData frames; for each `'w'`-prefixed payload, strip the
   wal-protocol header (24 bytes) and feed the inner bytes to goopg's
   `wal.DecodeMessage`.
8. Assert each decoded message matches the expected SQL operation (by
   relid → table name lookup + reading tuple values).

**(b) `TestPort_PgoutputInteropGoopgToPG` — PG consumes goopg's pgoutput.**

1. Spawn goopg primary via `cluster.NewCluster`.
2. goopg primary runs:
   ```sql
   CREATE TABLE t (id int PRIMARY KEY, v text);
   CREATE PUBLICATION p FOR ALL TABLES;
   ```
3. Spawn PG subscriber via `pgcluster.New`.
4. PG subscriber runs:
   ```sql
   CREATE TABLE t (id int PRIMARY KEY, v text);
   CREATE SUBSCRIPTION s CONNECTION 'host=<goopg> port=<port>
       user=postgres dbname=postgres' PUBLICATION p WITH (enabled = true,
       copy_data = false);
   ```
5. goopg primary runs INSERT/UPDATE/DELETE.
6. Poll PG subscriber's `t` table; assert rows arrive within ~5 s.

### Known divergence points to audit

For each, the design doc cross-references the corresponding goopg encoder
location and the upstream encoder location, so a mismatch can be diagnosed:

| Aspect | goopg site | PG site |
|---|---|---|
| Begin LSN encoding (big-endian 8 bytes) | `internal/wal/pgoutput.go` `encodeBegin` | `proto.c::logicalrep_write_begin` |
| Relation message column type OID | `internal/wal/pgoutput.go` relation cache | `proto.c::logicalrep_write_attrs` |
| Replica-identity marker | `internal/wal/pgoutput.go` (currently hardcoded `'d'`) | `proto.c::logicalrep_write_rel` |
| Tuple null/unchanged marker | `internal/wal/pgoutput.go` `encodeTuple` | `proto.c::logicalrep_write_tuple` |
| Commit_ts (PG uses microseconds since 2000-01-01) | `internal/wal/pgoutput.go` | `proto.c::logicalrep_write_commit` |
| `proto_version` negotiation (currently goopg uses 1) | `internal/server/logicalwalsender.go` | `pgoutput.c::pgoutput_startup` |

### Fix any divergences

If a test fails, the fix is targeted in `internal/wal/pgoutput.go` or the
decoder. Likely areas (rank-ordered by risk):

1. **Type OIDs**: goopg's catalog may emit goopg-internal OIDs (e.g., int4
   ≠ PG's `INT4OID = 23`). Map goopg types → PG OIDs in the relation
   message.
2. **Commit_ts epoch**: PG uses 2000-01-01 microsecond epoch; goopg may
   use Unix epoch.
3. **Tuple text format**: PG outputs `output_function(value)`; goopg must
   match (e.g., int4 → decimal string; text → escaped).
4. **Replica-identity marker**: goopg emits `'d'` (DEFAULT); confirm PG
   accepts when the local table also has DEFAULT identity.

## Files to create / modify

| File | Change |
|---|---|
| `internal/testport/pgoutput_interop_test.go` (new) | Both interop tests |
| `internal/wal/pgoutput.go` | Fixes for any divergence found by the tests |
| `internal/wal/pgoutput_decoder.go` | Same |

## Verification

```bash
go test -v -run TestPort_PgoutputInterop -timeout 5m ./internal/testport/
```

Both subtests must pass. On failure, the test output points at the exact
message type and byte offset of the divergence.

## Risks

- **PG-side slot retention**. The interop tests create slots that persist
  on the PG cluster across test runs. Mitigation: `pgcluster` lifecycle
  destroys the data dir on test cleanup; if shared, drop the slot at test
  end.
- **proto_version drift across PG minor versions**. PG18 ships proto_version
  4 by default. goopg currently advertises 1. The interop tests use
  `proto_version = '1'` explicitly in `START_REPLICATION` options; PG
  honours the client request. Document the constraint; protocol upgrade
  is future work.
- **Hand-rolled replication protocol parsing in test (a)**. Use `pgconn`
  from `github.com/jackc/pgx/v5` (already in `go.mod`) — it has
  `Frontend.Receive` that returns typed messages. Avoid raw `net.Conn`.
