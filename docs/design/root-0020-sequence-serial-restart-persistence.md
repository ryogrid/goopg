# root-0020 — Sequence / SERIAL restart persistence

## Problem

goopg's sequences lived only in the executor's process-global in-memory
registry (`internal/executor/operators_sequence.go`, `seqRegistry`), and a
SERIAL column's pg_attribute heap row stores the PG-canonical base-integer
`atttypid` (deliberately — a real PG18 standby reads those catalog pages).
After a restart, therefore:

- every sequence (implicit serial and explicit `CREATE SEQUENCE`) vanished;
- the reloaded column read back as plain `bigint`/`int4` (`loadUserTablesFromHeap`,
  `internal/initdb/open.go`), so the INSERT auto-generation path — which keys
  on the serial spelling in `Column.Type.Name` (`operators_storage.go`) —
  stopped firing.

Surfaced by WordPress-on-goopg: after the first restart, `wp_usermeta` INSERTs
failed with `null value in column "umeta_id" ... violates not-null constraint`,
and 29 `wp_options` rows were silently written with NULL `option_id`.

## Upstream semantics

PostgreSQL stores each sequence as a one-page relation whose state is
WAL-logged. `nextval` pre-logs `SEQ_LOG_VALS` (32) values ahead of the fetched
one (`postgres/src/backend/commands/sequence.c`, `xl_seq_rec`), so crash
replay restores a counter at or beyond anything handed out — never repeating a
value, at the cost of a gap of up to 32 values. `setval`/DDL log exact state.
SERIAL itself is sugar: base int column + owned sequence + `nextval` default,
all durable via catalogs.

## Design

goopg's established mechanism for logical DDL state with no physical page
representation is the **goopg-private WAL record + startup replay** family
(CREATE DATABASE M0054-0001, CREATE SCHEMA 0110-0012, functions/event
triggers/etc. M0119-0004). Sequences follow it with two new kinds
(`internal/wal/recovery.go`):

- **`RecordKindSequenceState` (65)** — a full-state snapshot (definition +
  counter + owning-column markers). Emitted on: CREATE TABLE with
  SERIAL/IDENTITY, `CREATE SEQUENCE`, `ALTER SEQUENCE` (rename retires the
  old name with a drop record first), `setval`, `TRUNCATE ... RESTART
  IDENTITY`, and — PG-style — every 32nd `nextval` with the counter
  pre-logged 32 values ahead (`maybePreLogNextval`). Replay is
  last-record-wins, so one kind covers create/alter/advance.
  The payload carries `OwnedBy` ("table.column") plus `ColSpelling`
  ("bigserial", ...) / `IdentityKind`, because pg_attribute cannot carry the
  serial marker: replay restores the owning column's catalog type/identity
  flags, which the auto-increment path keys on.
- **`RecordKindDropSequence` (66)** — DROP SEQUENCE, DROP TABLE cascade
  (`DropSequencesOwnedByTable` now returns the dropped names), and the
  old-name half of a rename.

**Write side** (`internal/executor/operators_sequence.go` + emit sites in
`operators_ddl.go`): `WALLogSequenceState(ctx, name)` snapshots live state
under the sequence mutex and appends; temporary sequences are never logged.
`seqState` gains `colSpelling`/`identityKind` markers
(`SetSequenceColumnMarker`) and a `logHorizon` so nextval only re-logs when a
fetched value crosses the pre-logged horizon.

**Replay side** (`internal/initdb/sequence_ddl_recovery.go`,
`replaySequenceDDLRecords`): wired in `open.go` AFTER `loadUserTablesFromHeap`
(owning tables must be registered). Re-registers each surviving sequence via
`executor.RestoreSequenceFromWAL` (counter restored exactly as logged),
re-creates the virtual sequence relation
(`executor.CreateSequenceCatalogRelation`, extracted from
`createSeqCatalogTable` so pg_class/`SELECT * FROM seq` discoverability
survives too), and restores the owning column's serial spelling / identity
flags.

## Semantics and caveats

- **Gap-on-restart ≤ 32 values** for sequences advanced since their last
  exact-state record — identical to PostgreSQL's crash behavior.
- **Cycled sequences** may repeat values after a crash within the wrapped
  range (same class of caveat as PG's unlogged gap; v0 accepts it — noted at
  `maybePreLogNextval`).
- **WAL retention**: like every record in the logical-DDL family, these
  records are read back by `wal.ReadAll` and are NOT protected from
  checkpoint-driven segment pruning (`SlotAwareRetainer`). Actively-used
  sequences self-heal (the periodic pre-log lands in recent segments), but an
  idle sequence's only record can be pruned — the same latent family-wide
  limitation tracked in the deferral ledger.
- **Pre-existing clusters** (data dirs created before this change) have no
  sequence records in WAL; their serial columns remain degraded until the
  schema is re-created. (The WordPress instance under `wp/` was re-installed.)
- The generated-column fallback registration
  (`operators_generated.go`, apply-worker path) has no ctx and stays
  unlogged; it self-heals via the nextval pre-log on the SQL path.

## Tests

- `internal/testport/serial_sequence_durability_test.go` —
  `TestPort_SerialSequenceSurvivesRestart`: BIGSERIAL auto-gen continues
  strictly above the pre-restart max (within the 32-value gap) after a clean
  stop→start; an explicit `CREATE SEQUENCE ... START 100 INCREMENT 5` resumes
  on its series; a dropped sequence stays gone. Mirrors
  `create_schema_durability_test.go`.
- Race gate: `go test -race ./internal/wal/` and executor sequence tests.
