# 0103-0003 — pgoutput Wire-Byte Interoperability (PG ↔ goopg, Both Directions)

**Status:** accepted — subtest (a) landed 2026-05-14 loop 1; encoder + slot-creation prerequisites for subtest (b) landed 2026-05-14 loop 2; subtest (b) collapsed to a live wrapper after M0103-0008 closure (2026-05-14 — `TestPort_PgoutputInteropGoopgToPG` runs end-to-end, no `t.Skip` outside short-mode).
**Date:** 2026-05-13 (initial), 2026-05-14 (partial impl), 2026-05-14 (closure)
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

## Implementation status (2026-05-14, loop 1)

### Subtest (a) — `TestPort_PgoutputInteropPGToGoopg` — **PASS**

Implemented in `internal/testport/pgoutput_interop_test.go`. Rather
than running `pg_recvlogical` and parsing a captured file (which would
require careful handling of the tool's exit-on-endpos semantics and
file-buffer flush window), the test drives an in-database equivalent:

  1. Spawn upstream PG (binaries under `postgres/local_install/bin`)
     with `wal_level = logical`. The harness skips cleanly when those
     binaries are missing.
  2. Execute `CREATE TABLE t (id int PRIMARY KEY, v text)` +
     `CREATE PUBLICATION p FOR ALL TABLES`.
  3. Pre-create a `pgoutput` logical slot with
     `pg_create_logical_replication_slot('goopg_interop', 'pgoutput')`.
  4. Execute INSERT/INSERT/UPDATE/DELETE — four separate transactions
     so the slot captures four Begin/Commit pairs.
  5. Drain via `pg_logical_slot_get_binary_changes('goopg_interop',
     NULL, NULL, 'proto_version', '1', 'publication_names', 'p')`.
     Each row in the result is one pgoutput message; concatenating
     them gives the exact byte stream a libpq subscriber would see.
  6. Walk the byte blob with a per-kind length-aware splitter, route
     each message through `wal.DecodeMessage`, and assert: kind
     counts (1+ Begin / 1+ Commit / 1+ Relation / 2 Insert / 1 Update
     / 1 Delete), relation name (`t`), column count (2), column type
     OIDs (int4=23, text=25), and tuple contents (`(1,hello)`,
     `(2,world)`, `(2,updated)`, delete-old-tuple `(1,<null>)` — the
     last NULL reflects REPLICA IDENTITY DEFAULT's K-marker shape
     where non-key columns are omitted).

### Divergence found + fixed

PG omits the entire old-tuple section in `'U'` messages when REPLICA
IDENTITY is DEFAULT and the update did not touch a replica-identity
column. The upstream wire bytes go directly from `'U' rel_oid` to
`'N' tuple` — no `'K'` or `'O'` marker, no empty tuple. The goopg
decoder previously required `'K'` or `'O'` and rejected such messages
with `"update old-tuple type=%q want K or O"`. Fixed in
`internal/wal/pgoutput_decoder.go::DecodeMessage` by treating the
`'K'`|`'O' + old_tuple` block as optional.

### Encoder asymmetry — **FIXED 2026-05-14 loop 2**

`internal/wal/pgoutput.go::writeUpdate` previously emitted
`'K' | uint16(0)` when no old tuple was provided. Per upstream
`proto.c::logicalrep_write_update`, the K/O marker must be **omitted
entirely** when no old tuple exists — emitting `'K'` with a zero-attr
tuple is protocol-illegal and PG's apply worker rejects it because
the tuple's `ncols` field must equal the relation's declared column
count, not 0. Fixed by skipping the K/O block when `oldTuple == nil`
and emitting `'U' rel_oid 'N' new_tuple` directly. Pinned by
`TestPgoutputUpdateWithoutOldTupleGoesDirectlyToN` in
`internal/wal/pgoutput_test.go`.

`writeDelete`'s zero-attr K fallback remains as a defensive guard,
not as a normal path: in well-formed DML the caller always provides a
key tuple (REPLICA IDENTITY DEFAULT/INDEX) or the full row (REPLICA
IDENTITY FULL). The guard avoids a server-side panic if a caller ever
violates that contract; a real PG subscriber would reject the
resulting message, which is the desired loud failure. A follow-up
loop may upgrade the guard to a hard error once every caller path is
audited.

### CREATE_REPLICATION_SLOT LOGICAL pgoutput — **FIXED 2026-05-14 loop 2**

`internal/server/replication.go::replyCreateReplicationSlot` now
parses the upstream grammar
`CREATE_REPLICATION_SLOT slot_name [TEMPORARY] LOGICAL output_plugin
[EXPORT_SNAPSHOT|NOEXPORT_SNAPSHOT|USE_SNAPSHOT] [TWO_PHASE]`, calls
`Slots.Create(name, wal.SlotLogical, currentLSN)`, and returns the
four-column reply (`slot_name`, `consistent_point`, `snapshot_name`
NULL, `output_plugin` = "pgoutput"). Only `pgoutput` is accepted as
the plugin — other plugin names land with `feature_not_supported`.
The TEMPORARY and snapshot keywords are syntactically accepted but
not semantically distinguished in v0 (TEMPORARY does not yet auto-
drop on disconnect; snapshot exporting is not implemented). Pinned
by `TestReplicationCreateLogicalSlot` and
`TestReplicationCreateLogicalSlotRejectsUnknownPlugin` in
`internal/server/replication_test.go`.

### Subtest (b) — `TestPort_PgoutputInteropGoopgToPG` — **STILL SKIPPED**

`pubsubcluster` (M0103-0006) landed and the harness is wired up.
Running the test against goopg-pub + PG-sub uncovered two further
publisher-side gaps that PG's CREATE SUBSCRIPTION drives through
libpqrcv *before* it ever reaches START_REPLICATION:

#### Gap 1 — per-query context premature cancellation — **FIXED 2026-05-14**

`internal/server/server.go::runPostStartupLoop` constructs a
per-query context (`queryCtx`) on every `MsgQuery`. On
replication-mode connections the code branched into
`handleReplicationCommand` and — regardless of outcome — called
`queryCancel()` *before* falling through to the regular SQL path
(`handleQueryOrCopy`). Result: PG's libpqrcv probes
(`SELECT pubname FROM pg_catalog.pg_publication WHERE pubname IN
(…)`) entered the SQL executor with an already-cancelled context;
`internal/executor/context.go::acquireRelLock` saw
`context.Canceled` mid-lock-wait and returned SQLSTATE 57014
("canceling statement due to user request"). PG surfaced this back
to the user as "could not receive list of publications from the
publisher: ERROR: canceling statement due to user request".

Fix: defer the `clearQueryCancel()` / `queryCancel()` pair until
after the replication-command dispatcher decides it cannot handle
the frame, so the SQL fall-through sees the live `queryCtx`.
Pinned by
`internal/server/replication_test.go::TestReplicationFallthroughQueryNotCancelled`,
which asserts that a SQL query on a `replication=true` connection
returns a non-57014 error (i.e. the SQL path's natural error
rather than the cancellation shortcut).

#### Gap 2 — parser lacks `VARIADIC` keyword — **OPEN, scope of M0103-0008**

With Gap 1 closed, the next libpqrcv probe — `fetch_table_list`
— is the one that fails. PG18 sends a query of the form:

```sql
SELECT DISTINCT … FROM pg_publication p
  JOIN (SELECT (pg_get_publication_tables(VARIADIC array_agg(p.pubname::text))).*
        FROM pg_publication p WHERE p.pubname IN ('p')) GPT
  …
```

goopg's parser rejects `VARIADIC` with
`syntax error at or near "expected expression (got variadic)"`,
which surfaces back to PG as
"could not receive list of replicated tables from the publisher".
Closing this requires parser-side VARIADIC support plus a working
`pg_get_publication_tables` function (the virtual view
`pg_publication_tables` already exists). That work is the natural
scope of M0103-0008's Scenario B (goopg primary + PG subscriber)
— same publisher-side surface, same failure mode — so subtest
(b) defers there rather than being implemented twice.

Once M0103-0008 lands the probe-survival fix, subtest (b)
collapses to the thin wrapper already coded in
`internal/testport/pgoutput_interop_test.go` (preserved as a
closure under the `t.Skip` for traceability).

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

## Closure (2026-05-14)

M0103-0004 is now fully resolved. Subtest (b) (Gap 2 from the loop-3
analysis above) was deferred to M0103-0008 because the publisher-side
libpqrcv probe surface — `VARIADIC` parser, `pg_get_publication_tables`
SRF, derived-subquery composite expansion, LATERAL pg_catalog-qualified
SRF dispatch, `pg_class.relnatts`, `relreplident`, slot-options list
parsing, logical-walsender keepalive, etc. — is exactly the surface
that M0103-0008 (Scenario B: goopg primary + PG subscriber) drives end
to end. The 17-rung probe-survival ladder under M0103-0008 closed every
gap on that surface (see `docs/design/0103-0023-m0103-0008-scenario-b-closure.md`),
and the live `TestPort_PgoutputInteropGoopgToPG`
(`internal/testport/pgoutput_interop_test.go:193`) now runs the four-
statement INSERT/INSERT/UPDATE/DELETE round-trip against a goopg
publisher + PG subscriber, asserting final state (`id=2 v='updated'`,
`id=1` deleted) and `count(*) == 1` — no `t.Skip` outside the standard
`testing.Short()` short-mode gate. Subtest (a) is unchanged
(`TestPort_PgoutputInteropPGToGoopg`, byte-level wire decode against
upstream PG via `pg_logical_slot_get_binary_changes`).

Verification (live runs covered by M0103-0008 closure, 2026-05-14):
`go test -count=1 -timeout 240s -run TestPort_PgoutputInterop ./internal/testport/`
exercises both subtests; subtest (b) had passed five consecutive runs
at 1.6–1.8 s each on its closure loop. No production code change in
this closure loop — M0103-0008's keystone fixes (rung 16 `pg_class.oid`
numeric flip + `relreplident` column) supplied the missing pieces.
