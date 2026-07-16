# 02c — Phase-B1 application: pg_namespace, pg_proc, pg_sequence

| | |
|---|---|
| Status | draft — **agent-reviewed 2026-07-16**, two lenses (PG-fidelity vs ./postgres; goopg-integration vs code): 4 blocker + 10 major + 8 minor findings, ALL folded in with inline `(review …)` tags |
| Date | 2026-07-16 |
| Scope | Applies the [02b recipe](02b-catalog-conversion-recipe.md) to the three B1 catalogs. Thin by design: only per-catalog inputs, scope decisions, and risk deltas live here. |
| Target | PostgreSQL 18.3 headers cited per section |
| Parent | [02-catalog-heap-journaling.md](02-catalog-heap-journaling.md) §8.2 |

Conversion order: **pg_namespace → pg_proc → pg_sequence** (pg_proc rows carry
`pronamespace`; pg_sequence needs pg_class rows for `seqrelid`).

## 1. pg_namespace (the exemplar conversion)

- **Tuple** (`postgres/src/include/catalog/pg_namespace.h:35`,
  `FormData_pg_namespace`): `{oid, nspname name, nspowner oid, nspacl aclitem[]}`
  — 3 fixed columns + one varlena. nspacl renders from the existing ACL
  machinery (the `default_acl` / relacl precedent).
- **Indexes** (same header): `pg_namespace_nspname_index` **2684**
  (unique, `nspname name_ops`), `pg_namespace_oid_index` **2685** (pkey,
  `oid oid_ops`). Both bootstrapped in DefaultDBOid today by dedicated
  bootstrappers in `internal/initdb/initdb.go` (:1744 names 2684/2685
  explicitly; `relcache_init.go` holds only the nailed descriptor)
  (review MINOR-1); non-default DBs need B0.3.
- **TOAST**: `DECLARE_TOAST(pg_namespace, 4163, 4164)` (same header) — an
  nspacl exceeding the inline varlena limit would need it. Deferred with a
  ledger row: goopg ACL lists stay far below the threshold; the conversion
  errors loudly (not silently truncates) if a row would need TOAST
  (review MINOR-4).
- **DDLs → journal shape**:
  | DDL | PG shape | goopg emit site today |
  |---|---|---|
  | CREATE SCHEMA | heap INSERT + 2 index inserts | `operators_ddl.go:16411` |
  | DROP SCHEMA | heap DELETE | `operators_ddl.go:14628` |
  | ALTER SCHEMA RENAME | non-HOT UPDATE (+ fresh entries in BOTH indexes — nspname is indexed) | `operators_ddl.go:17874` |
  | ALTER SCHEMA OWNER | UPDATE (HOT-eligible — no indexed column) | `operators_ddl.go:17923` |
- **Records that die** (all four, one landing): `RecordKindCreateSchema`(34),
  `RecordKindDropSchema`(35), `RecordKindAlterSchemaRename`(100),
  `RecordKindAlterSchemaOwner`(101); plus
  `internal/initdb/schema_ddl_recovery.go` and
  `internal/wal/schema_alter_ddl.go`.
- **Read model**: write-through — the existing schema registry
  (`RegisterSchema`/rename/owner mutators) gains a TID map; the `VirtualRows`
  builder re-points at it. Never heap-read (schema resolution is hot).
- **Global registry vs per-DB heap (review MAJOR-9)**: goopg's schema registry
  is cluster-global (`RegisterSchema(name)` takes no dbOid,
  `catalog.go:12146`), while pg_namespace is per-database. B1 resolution:
  writes route through the SAME `NamespaceDBOid` mapping every catalog write
  uses today (so a schema created on any connection lands in DefaultDBOid's
  2615 + the postgres-DB mirror, matching where recovery scans); the reload
  descriptor scans that one heap and applies into the global registry. TRUE
  per-DB pg_namespace rows (a schema existing in one DB only) become possible
  only after B0.3's per-DB heaps + a dbOid-aware registry — recorded as a
  ledger row, NOT a B1 deliverable; goopg's observable schema semantics are
  unchanged.
- **Reload**: descriptor at the schema pass's slot (02a §2.4); user schemas
  only (`oid >= FirstUserOID`); builtin schemas (pg_catalog, public,
  information_schema) stay compiled-in + initdb heap rows.
- **Gate caveat (review MAJOR-10)**: ALTER SCHEMA RENAME today also emits
  DropSequence+SequenceState pairs for every sequence owned by the schema
  (`operators_ddl.go:17886-17892` — sequence records are name-keyed). The
  pg_waldump shape "RENAME = HEAP_UPDATE + 2 index inserts" is asserted on a
  schema WITHOUT sequences; schemas with sequences additionally show the
  kind-65/66 records until B1.3b.
- **Residuals (ledger)**: pg_depend row for schema→owner dependency (B3);
  per-DB pg_namespace rows (above); commit-record invalidation content
  unchanged (Part-A scope).
- **Blast radius**: ~12–16 files; the risky surface is regress-wide schema
  resolution if the reload mis-registers builtins — bounded by the
  FirstUserOID policy.

## 2. pg_proc

- **Tuple** (`postgres/src/include/catalog/pg_proc.h:30`, `FormData_pg_proc`):
  ~30 columns (proname, pronamespace, proowner, prolang, prokind, proargtypes
  oidvector, prosrc text, proacl, …). The tuple builder is B1's heavy lift;
  goopg's existing pg_proc virtual row builder + `pg_proc_seed_data.go` provide
  the value sources.
- **Indexes**: `pg_proc_oid_index` **2690** (pkey), `pg_proc_proname_args_nsp_index`
  **2691** (unique: proname, proargtypes, pronamespace).
- **DDLs**: CREATE FUNCTION/PROCEDURE (INSERT; `CREATE OR REPLACE` over an
  existing function is an UPDATE — PG parity; goopg preserves the OID on
  replace, pinned by `TestRoutinesCreateOrReplacePreservesOID`), DROP FUNCTION
  (DELETE), ALTER FUNCTION rename/owner/set-schema (UPDATEs; rename +
  set-schema are non-HOT — 2691 keys change), **ALTER FUNCTION flags
  (volatility/strictness/security) and SET/RESET config (proconfig)** — both
  plain UPDATEs (review MAJOR-3: an earlier draft omitted these two). Bespoke
  kinds that die (actual constants, `internal/wal/recovery.go`):
  CreateFunction=61, DropFunction=62, AlterFunctionRename=63,
  AlterFunctionFlags=64, AlterFunctionOwner=121, AlterFunctionSetSchema=122,
  AlterFunctionConfig=123 + `function_ddl_recovery.go`.
- **Transition wrinkle (review MAJOR-3)**: CREATE AGGREGATE also writes pg_proc
  registry rows via the bespoke CreateAggregate group (converts in B2, scanner
  at `open.go:1337`). During the B1–B2 window the pg_proc HEAP lacks aggregate
  rows while the registry has them — acceptable (the heap is not yet the only
  source), but the pg_proc reload must tolerate registry entries it did not
  create, and the B2 conversion closes the gap.
- **Mapped-catalog note**: pg_proc is in PG's relmapper nailed set; steady-state
  DML emits no relmap record, so this conversion does NOT need B0.4 (02a §5.3).
- **Read model**: write-through function registry (name resolution on every
  call — never heap-read). Builtin rows (~3k, initdb-populated) stay
  compiled-in; reload applies user procs only (`oid >= FirstUserOID`).
- **TOAST**: `DECLARE_TOAST(pg_proc, 2836, 2837)` (pg_proc.h:138) — prosrc is
  varlena-heavy and IS the first realistic TOAST candidate in Phase B
  (review MINOR-4).
- **Risk delta**: assert the longest regress-suite function body stays under
  the inline limit; if any prosrc would need TOAST, error loudly and add the
  catalog-TOAST ledger row before proceeding.

## 3. pg_sequence — catalog row ONLY (normative scope decision)

PG splits sequence state across two relations
(`postgres/src/include/catalog/pg_sequence.h:23`,
`postgres/src/include/commands/sequence.h`):

1. **pg_sequence catalog row** `{seqrelid, seqtypid, seqstart, seqincrement,
   seqmax, seqmin, seqcache, seqcycle}` — the DEFINITION; changed only by
   CREATE/ALTER SEQUENCE (heap INSERT/UPDATE on pg_sequence, index **5002**
   `pg_sequence_seqrelid_index`).
2. **The sequence relation's single page** holding
   `FormData_pg_sequence_data{last_value, log_cnt, is_called}` — the COUNTER;
   `nextval` journals it as `XLOG_SEQ_LOG` (RM_SEQ) on that page, pre-logging
   32 fetches ahead.

goopg conflates both (plus SERIAL/identity ownership markers) in one
full-state `RecordKindSequenceState`(65) record emitted from
`internal/executor/operators_sequence.go:462` (DDL) and `:529` (nextval horizon).

**B1 converts (1) only**:
- CREATE SEQUENCE → pg_class row (existing Family-1 path) + `XLOG_SMGR_CREATE`
  for the sequence relation file + pg_sequence heap INSERT + index 5002 insert.
- ALTER SEQUENCE (definitional fields) → pg_sequence heap UPDATE.
- DROP SEQUENCE → pg_sequence heap DELETE + pg_class row delete.
- Reload descriptor Order=50 rebuilds definitions from the heap.

**What survives this phase (documented residuals, one ledger row each)**:
- Counter state (`last_value/log_cnt/is_called`) stays on kind 65 — retiring it
  requires the RM_SEQ `XLOG_SEQ_LOG` emit + the 1-tuple sequence-relation page
  format (a Part-A-style record flip, staged as **B1.3b** if budget allows,
  else ledgered).
- **`RecordKindDropSequence`(66) survives too** (review MAJOR-10): goopg's
  counter records are NAME-keyed (`seqKey(name, dbOid)`; renames emit
  DropSequence(old)+SequenceState(new) pairs, ≥9 emit sites incl. DROP TABLE
  cascade and ALTER SCHEMA RENAME, `operators_ddl.go:17886-17892`), so while
  any name-keyed kind-65 record exists, kind 66 is what stops replay
  resurrecting dropped/renamed sequences. Both kinds retire together in B1.3b,
  where the counter moves to the seqrelid-keyed sequence relation page and
  name-keying disappears. B1's heap DELETE removes the pg_sequence ROW; the
  kind-66 emit stays beside it.
- SERIAL/identity ownership: `attidentity` migrates into the pg_attribute heap
  row (column exists in the PG18 25-col layout); `OWNED BY` needs pg_depend →
  **B3**.
- Kind 65's payload therefore SHRINKS (definitional fields no longer read at
  recovery) but both constants survive B1; deletion completes in B1.3b + B3.

**Risk delta**: recovery must apply pg_sequence rows BEFORE replaying kind-65/66
counter records (the descriptor slots at the sequence pass, `open.go:1508-1520`,
after index replay at :1501 — pin it with a crash test: CREATE SEQUENCE;
nextval ×N; crash; restart; nextval must not repeat). Name-keyed counter
records bind to heap-reloaded definitions by sequence name at that point in
the pass order; a rename's Drop+State pair keeps the binding correct across
renames exactly as today.

## 4. Gate deltas (on top of 02b §4)

- pg_namespace: `pg_waldump` shape — CREATE SCHEMA = `Heap/INSERT` on 2615 +
  2× `Btree/INSERT_LEAF`; RENAME = `Heap/UPDATE` + 2× index inserts. e2e: PG
  standby `\dn` shows the schema.
- pg_proc: e2e — PG standby `\df` shows the function; regress plpgsql suites
  (function bodies round-trip through prosrc).
- pg_sequence: crash test above; e2e — PG standby `\ds` + `SELECT * FROM
  pg_sequence` row visible.
