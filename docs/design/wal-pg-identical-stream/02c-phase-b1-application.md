# 02c — Phase-B1 application: pg_namespace, pg_proc, pg_sequence

| | |
|---|---|
| Status | draft — pending agent review |
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
  `oid oid_ops`). Both bootstrapped in DefaultDBOid today
  (`relcache_init.go`); non-default DBs need B0.3.
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
- **Reload**: descriptor Order=10 (02a §2.4); user schemas only
  (`oid >= FirstUserOID`); builtin schemas (pg_catalog, public,
  information_schema) stay compiled-in + initdb heap rows.
- **Residuals (ledger)**: pg_depend row for schema→owner dependency (B3);
  commit-record invalidation content unchanged (Part-A scope).
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
  existing function is an UPDATE — PG parity), DROP FUNCTION (DELETE), ALTER
  FUNCTION rename/owner/set-schema (UPDATEs; rename + set-schema are non-HOT —
  2691 keys change). Bespoke kinds that die: `RecordKindCreateFunction`(716-area
  const), `RecordKindDropFunction`, the ALTER-function variants (kind 122 group)
  + `function_ddl_recovery.go`.
- **Mapped-catalog note**: pg_proc is in PG's relmapper nailed set; steady-state
  DML emits no relmap record, so this conversion does NOT need B0.4 (02a §5.3).
- **Read model**: write-through function registry (name resolution on every
  call — never heap-read). Builtin rows (~3k, initdb-populated) stay
  compiled-in; reload applies user procs only (`oid >= FirstUserOID`).
- **Risk delta**: prosrc/proacl are varlena-heavy — first conversion likely to
  hit tuples near TOAST thresholds; assert the longest regress-suite function
  body stays under the inline limit or add the TOAST note to the ledger.

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
- SERIAL/identity ownership: `attidentity` migrates into the pg_attribute heap
  row (column exists in the PG18 25-col layout); `OWNED BY` needs pg_depend →
  **B3**.
- Kind 65's payload therefore SHRINKS (definitional fields no longer read at
  recovery) but the constant survives B1; the deletion completes in B1.3b + B3.

**Risk delta**: recovery must apply pg_sequence rows BEFORE replaying kind-65
counter records (the descriptor order + the existing sequence recovery pass at
`open.go:1498+` already run in that order — pin it with a crash test:
CREATE SEQUENCE; nextval ×N; crash; restart; nextval must not repeat).

## 4. Gate deltas (on top of 02b §4)

- pg_namespace: `pg_waldump` shape — CREATE SCHEMA = `Heap/INSERT` on 2615 +
  2× `Btree/INSERT_LEAF`; RENAME = `Heap/UPDATE` + 2× index inserts. e2e: PG
  standby `\dn` shows the schema.
- pg_proc: e2e — PG standby `\df` shows the function; regress plpgsql suites
  (function bodies round-trip through prosrc).
- pg_sequence: crash test above; e2e — PG standby `\ds` + `SELECT * FROM
  pg_sequence` row visible.
