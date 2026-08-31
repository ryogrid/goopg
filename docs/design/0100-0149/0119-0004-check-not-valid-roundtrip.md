# 0119-0004k — `NOT VALID` CHECK constraint round-trip in pg_dump (DU-002 slice 308)

**Milestone:** M0119-0004 (pg_dump 002–010 catalog-view parity battery; source M0110-0001)
**Status:** accepted / implemented
**Oracle:** PostgreSQL 18.3 (`./postgres/local_install`), `pg_dump --no-sync`

## Problem

A CHECK constraint added with `NOT VALID` is recorded in PG as
`pg_constraint.convalidated = 'f'`: existing rows were never scanned, so the
constraint is enforced only for *new* writes until an explicit
`ALTER TABLE … VALIDATE CONSTRAINT` runs. pg_dump must preserve that state — a
restored CHECK that was `NOT VALID` on the source must remain `NOT VALID`, or the
restore would re-scan the very rows the operator deliberately grandfathered and
potentially fail on data that does not satisfy the predicate.

This is the **CHECK** half of the same shared tail that slice 307 wired for the
FK path. PG's `pg_get_constraintdef_worker` (ruleutils.c:2604) appends a
`" NOT VALID"` suffix for *any* constraint type with `convalidated='f'`, after
the type-specific body:

```c
else if (!conForm->convalidated)
    appendStringInfoString(&buf, " NOT VALID");
```

Crucially, pg_dump treats an unvalidated CHECK differently from a valid one. In
`getTableAttrs` (pg_dump.c:9757) it sets `separate = !validated`: a valid CHECK
is dumped *inline* in the `CREATE TABLE` body, but a `NOT VALID` CHECK is dumped
as a **standalone** `ALTER TABLE … ADD CONSTRAINT … NOT VALID;` in the
`SECTION_POST_DATA` phase — *after* the table data loads — precisely so that
possibly-violating rows are present before the constraint is (re)created without
validation. `dumpConstraint` (pg_dump.c:18564) emits it as (not `ONLY`, so it
propagates to children):

```
ALTER TABLE public.nvc_tbl
    ADD CONSTRAINT nvc_chk CHECK ((val > 0)) NOT VALID;
```

## goopg gap

goopg tracked the `NotValid` state end-to-end for FKs but not for CHECKs:

- The parser **accepted** `ADD CONSTRAINT … CHECK (…) NOT VALID` but *discarded*
  the `NOT VALID` token (ddl.go consumed it only to advance past the trailer).
- `catalog.NamedCheckConstraint` had no `NotValid` field, so the
  `pg_constraint` virtual builder hardcoded `convalidated='t'` for every
  contype='c' row (catalog.go ~4715).
- `pg_get_constraintdef`'s CHECK branch (expr.go ~7063) emitted only
  `CHECK ((expr))` [+ optional ` NO INHERIT`], never the ` NOT VALID` tail.

Net effect: a `NOT VALID` CHECK dumped as a *valid* inline constraint, silently
re-validating on restore.

## Fix

Five-site thread, mirroring the FK path:

1. **parser** (`internal/parser/ddl.go`, `AlterTableAddCheck` arm): capture the
   `NOT VALID` token into `act.NotValid` instead of discarding it.
2. **catalog struct** (`internal/catalog/catalog.go`): add
   `NamedCheckConstraint.NotValid bool` and a `AddCheckWithNotValid` helper.
3. **executor** (`internal/executor/operators_ddl.go`, `AlterTableAddCheck`
   case): call `tbl.AddCheckWithNotValid(name, expr, oid, act.NotValid)`.
4. **pg_constraint builder** (catalog.go): project `convalidated='f'` when
   `nc.NotValid` for the named-CHECK rows.
5. **deparse** (`internal/executor/expr.go`, `pg_get_constraintdef` CHECK
   branch): append ` NOT VALID` after the `CHECK ((expr))` [`NO INHERIT`] body
   when `nc.NotValid`, matching PG's byte order.

Dump-fidelity only; no execution-semantics change (goopg v0 enforces all CHECKs
on new writes regardless, and has no deferred existing-row scan).

## Scope notes

- Only the `ALTER TABLE ADD CONSTRAINT … CHECK … NOT VALID` path is wired.
  Inline `NOT VALID` in `CREATE TABLE` is not a PG-valid form (a fresh table has
  no existing rows to grandfather), so it is intentionally unsupported.
- Inherited/partition-child CHECKs created from a `NOT VALID` parent are not
  marked `NotValid` (the `AddCheckInherited` path defaults to `false`); this is
  out of scope for the slice and untested by it (it uses a plain table).

## Gates

- New DU-002 slice 308 in `TestPort_PgDumpConnectionSetup` (real `pg_dump`
  binary against goopg) asserts
  `ADD CONSTRAINT nvc_chk CHECK ((val > 0)) NOT VALID;` in stdout — PASS.
- `internal/parser`, `internal/catalog`, `internal/executor` suites — PASS.
- `go build ./...` clean.
- pgbench TPC-B smoke — pre-commit hook.

## Oracle reference

- `postgres/src/backend/utils/adt/ruleutils.c:2604` — shared ` NOT VALID` tail.
- `postgres/src/bin/pg_dump/pg_dump.c:9757` — `separate = !validated` for CHECK.
- `postgres/src/bin/pg_dump/pg_dump.c:18564` — `dumpConstraint` CHECK branch.
