# 0119-0004g — Identity-column sequence options round-trip in pg_dump (DU-002 slice 303)

**Milestone:** M0119-0004 (pg_dump 002–010 catalog-view parity battery; source M0110-0001)
**Status:** accepted / implemented
**Oracle:** PostgreSQL 18.3 (`./postgres/local_install`), `pg_dump --no-sync`

## Problem

A column declared `GENERATED { ALWAYS | BY DEFAULT } AS IDENTITY (sequence_options)`
attaches an implicit backing sequence whose definition PG persists in
`pg_sequence` (`seqstart`, `seqincrement`, `seqmin`, `seqmax`, `seqcache`,
`seqcycle`). `pg_dump` reads that row and renders it inside the
`ALTER TABLE … ADD GENERATED … AS IDENTITY ( … )` block, one option per line.

goopg captured **only `START WITH`** from the identity clause: the parser scanned
the parenthesised option list looking for the `start` keyword and ignored
everything else, and the executor hard-coded the backing sequence to
`increment = 1`, `cycle = false`, `cache = 1`, and type-default min/max
(`operators_ddl.go`, `RegisterSequence(seqName, seqStart, 1, seqMin, seqMax, false)`).

Consequently `INCREMENT BY n`, `MINVALUE n`, `MAXVALUE n`, `CACHE n`, and `CYCLE`
were **silently dropped**. A dump of

```sql
CREATE TABLE t (id integer GENERATED ALWAYS AS IDENTITY
  (START WITH 100 INCREMENT BY 5 MINVALUE 10 MAXVALUE 9999 CACHE 7 CYCLE), x text);
```

emitted `INCREMENT BY 1 / NO MINVALUE / NO MAXVALUE / CACHE 1` (no `CYCLE`),
so restoring the dump produced a column with the wrong step and bounds — a
silent loss of identity semantics. This is not just a display defect: the
backing sequence's `nextval()` step is also wrong at runtime.

## Fix

Capture the full sequence-option grammar on the identity clause and thread it to
the backing sequence — the existing slice-120 dump path then re-emits it.

1. **Parser AST** (`internal/parser/ast.go`, `ColumnDef`): new
   `IdentityIncrement *int64`, `IdentityMin *int64`, `IdentityMax *int64`,
   `IdentityCache *int64`, `IdentityCycle bool` (nil/false = not given →
   type/PG default). `IdentityStart int64` is unchanged.

2. **Parser** (`internal/parser/ddl.go`, the `GENERATED … AS IDENTITY` arm):
   replace the START-only token scan with a proper option loop that mirrors
   `parseCreateSequenceTail` — `START [WITH]`, `INCREMENT [BY]`, `MINVALUE`,
   `MAXVALUE`, `CACHE`, `CYCLE`, and `NO {MINVALUE|MAXVALUE|CYCLE}` (which leave
   the field nil/false so the type default applies). Uses the shared
   `parseInt64`. Unrecognised tokens / unterminated clauses now error instead of
   being silently skipped.

3. **Catalog** (`internal/catalog/catalog.go`, `Column`): mirror the five new
   fields (the sequence-registration loop in `operators_ddl.go` iterates over
   `catalog.Column`, not the parser `ColumnDef`).

4. **Executor** (`internal/executor/operators_ddl.go`, the serial/identity
   sequence-registration loop): compute `seqIncrement` (default 1, override from
   `c.IdentityIncrement`), override `seqMin`/`seqMax` from
   `c.IdentityMin`/`c.IdentityMax` when given, pass `c.IdentityCycle` to
   `RegisterSequence`, and call `SetSequenceCache(seqName, *c.IdentityCache)`
   when a cache size was specified.

No change to the dump path itself: pg_dump reads the backing sequence's
definition (slice 120 already renders the identity block correctly given a
correct `pg_sequence` row), so once `RegisterSequence` receives the real values
the `ADD GENERATED … AS IDENTITY (…)` block is byte-faithful.

## Blast radius

Serial columns share the registration loop but never set the identity option
fields (all nil/false), so their behaviour is byte-identical. The new catalog
fields default to nil/false for every existing column. The dump path is
unchanged. TPC-H/pgbench carry no identity columns.

## Verification

* New **DU-002 slice 303** in `TestPort_PgDumpConnectionSetup`
  (`internal/testport/pgdump_connsetup_test.go`): `idrich` exercises all options
  together (ascending, fully-bounded, `CYCLE`); `idbd` exercises `BY DEFAULT`
  with an explicit `INCREMENT BY` and explicit `NO MINVALUE / NO MAXVALUE`
  (must keep the type-default → `NO MINVALUE / NO MAXVALUE` in the dump, default
  `CACHE 1`). Both `ADD GENERATED … AS IDENTITY (…)` blocks pinned byte-for-byte.
* Both dumps verified **byte-identical to real pg_dump 18.3** for the same DDL
  (`./postgres/local_install`).
* `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` PASS.
* `internal/parser`, `internal/executor`, `internal/catalog` suites PASS.
* `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

* pg_dump 002–010 catalog-view parity battery (further slices).
* `CREATE SEQUENCE … OWNED BY schema.table.column` — goopg mis-resolves the
  3-part qualified owner (`sequence cannot be owned by relation "public"`);
  surfaced while probing this slice, tracked as a separate gap.
* extended-protocol commit-time deferral (architecturally entangled).
