# M0097-0023 — pg_constraint population for named CHECK constraints

**Status:** Implemented (2026-06-06, loop35)
**Area:** catalog / executor DDL
**Predecessor:** `0097-0023-leftjoin-inner-in-pushdown-shift.md` (loop34)

## Problem

`pg_constraint` (OID 2606) is defined as a virtual catalog table whose
`VirtualRows` hook already walks every user table's `NamedChecks` slice and
emits a fully-formed 25-column row per named CHECK constraint
(`internal/catalog/catalog.go`). But the hook short-circuits any constraint
whose `Name == "" || OID == 0`, and **every** named CHECK was being created
with `OID = 0` at all three creation sites in `operators_ddl.go`:

- `execCreateTable` LIKE INCLUDING CONSTRAINTS copy,
- `ALTER TABLE … ADD [CONSTRAINT name] CHECK (expr)`.

So `pg_constraint` was always empty.

The OID was deliberately pinned to 0 because populating the table triggered a
latent planner crash: psql `\d`'s catalog query does
`LEFT JOIN pg_constraint con ON (… AND con.contype IN ('p','u','x'))`, and the
M0063-0005 inner-only-conjunct pushdown rebased the `IN` operand's column ref
incorrectly, panicking `index out of range` in `Slot.Get`. That crash was
**fixed in loop34** (`shiftColumnRefsBy` gained an `*InExpr` case), which
unblocked this work.

## Change

1. **`catalog.Catalog.AllocOID() uint32`** — new interface method, implemented
   on `*InMemory` as an atomic `oid := c.nextOID; c.nextOID++` under the
   catalog mutex. Mirrors the inline pattern already used by
   `CreateTable`/`CreateIndex`, but exposed through the interface so executor
   DDL paths can mint a synthetic OID without a dedicated mutator.

2. **`(*ddlOp).allocConstraintOID(name string)`** — returns a fresh OID for a
   named constraint (`name != ""`) and 0 for an anonymous one. The two named
   AddCheck sites now call it. Anonymous column-/table-level checks stay at
   OID 0 (the current parser leaves them unnamed; PG-faithful auto-naming is a
   separate follow-up — see Limitations).

With a real OID present, `pg_constraint`'s existing VirtualRows hook emits the
constraint row (contype `c`, conrelid = table OID, conbin = raw expression).

## Verification

- `internal/executor/operators_ddl_named_check_test.go`
  `TestCheckViolationReportsNameAndDetail` — extended: the OID-0 guard
  (obsolete) is replaced with assertions that the named check carries a
  non-zero OID and surfaces as exactly one pg_constraint VirtualRows row with
  the correct oid/contype/conrelid/conbin.
- End-to-end against a live goopg server:
  `ALTER TABLE ct ADD CONSTRAINT b_positive CHECK (b > 0)` then
  `SELECT … FROM pg_constraint WHERE conname='b_positive'` returns the row;
  the previously-crashing
  `pg_index LEFT JOIN pg_constraint … contype IN ('p','u','x')` now runs
  cleanly (0 rows, no panic) with pg_constraint non-empty — confirming the
  loop34 fix holds against real data.
- Executor + catalog suites green except the two pre-existing, unrelated
  failures (`TestPgGetPublicationTablesRelidMatchesPgClassOid`,
  `TestToastByteaRoundTrip`), confirmed failing identically on clean HEAD.

## Limitations / follow-ups (unchanged or newly observed)

- **Anonymous CHECK auto-naming.** PostgreSQL generates `t_col_check` /
  `t_check` names (with a dedup counter) for inline/table-level checks and
  surfaces them in pg_constraint. goopg still leaves them unnamed → invisible.
  Generating PG-faithful names would also change violation messages, so it is
  scoped as its own loop.
- **`pg_get_expr` not implemented.** `\d` and many catalog views call
  `pg_get_expr(conbin, conrelid)`; goopg stores the raw SQL in `conbin`
  directly, but the function itself is missing.
- **`\d <table>` still errors** with "operator AND requires boolean operands"
  in a *different* sub-query (the per-attribute detail query joining
  `pg_attrdef`), independent of pg_constraint — reproduces on a
  constraint-free table. Separate pre-existing bug.
- **Persistence.** Synthetic constraint OIDs live only in the in-memory
  catalog; they are not written to a `pg_constraint` heap relation, so they do
  not survive restart. Matches the current handling of `NamedChecks`.
- **connamespace** is hard-coded to 2200 (public) in VirtualRows; schema-aware
  namespace resolution is deferred.
