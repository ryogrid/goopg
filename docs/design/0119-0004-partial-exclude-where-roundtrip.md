# 0119-0004m — partial EXCLUDE constraint `WHERE` predicate round-trip in pg_dump (DU-002 slice 310)

**Milestone:** M0119-0004 (pg_dump 002–010 catalog-view parity battery; source M0110-0001)
**Status:** accepted / implemented
**Oracle:** PostgreSQL 18.3 (`./postgres/local_install`), `pg_dump --no-sync`

## Problem

An exclusion constraint may be *partial*: `EXCLUDE USING <method> (<col> WITH
<op>) WHERE (<predicate>)` restricts enforcement to the subset of rows matching
the predicate. The predicate is a real, restorable property — it lives on the
backing index (`pg_index.indpred`) and changes the constraint's *meaning*, not
just its presentation: a constraint that excludes only `WHERE (b > 0)` must not
become one that excludes every row on restore.

PG renders an exclusion constraint's whole definition via
`pg_get_indexdef_worker` (called from the `CONSTRAINT_EXCLUSION` arm of
`pg_get_constraintdef_worker`, ruleutils.c), which appends the predicate
**after** the operator/INCLUDE list and **before** the `DEFERRABLE` tail that the
shared constraint-def code adds:

```c
/* pg_get_indexdef_worker, ruleutils.c:1564 */
appendStringInfo(&buf, " WHERE (%s)", str);
```

pg_dump's `getConstraints` renders each EXCLUDE via `pg_get_constraintdef(oid)`
and emits `ALTER TABLE ONLY … ADD CONSTRAINT <name> <condef>;`, so the WHERE
clause rides along automatically — *if* the server reproduces it.

goopg **silently dropped** the predicate at parse time: `parseExcludeConstraint`
(`internal/parser/ddl.go`) consumed `USING method (col WITH op)` and an optional
`INCLUDE (cols)` but never a trailing `WHERE`. The predicate never reached the
catalog, so `buildConstraintDefString`'s EXCLUDE branch had nothing to emit and a
partial exclusion degraded into an all-rows exclusion on restore.

## Fix

Thread the predicate end-to-end, reusing the existing partial-index predicate
plumbing (`catalog.Index.HasPredicate` / `Predicate` / `PredicateString`):

1. **Parser** (`internal/parser/ddl.go`, `parseExcludeConstraint`): after the
   optional `INCLUDE (…)`, accept an optional `WHERE` and parse the predicate
   with `p.parseExpr()` (the same call CREATE INDEX uses), storing it on a new
   AST field.
2. **AST** (`internal/parser/ast.go`): `TableConstraintDef.ExclusionWhere Expr`
   (nil when absent).
3. **Executor** (`internal/executor/operators_ddl.go`): new `applyExclusionPredicate`
   helper sets `idx.HasPredicate` / `idx.Predicate` / `idx.PredicateString =
   defaultExprToSQL(pred)` on the backing index. Wired into **all three** EXCLUDE
   build sites — named btree-equality EXCLUDE, anonymous btree-equality EXCLUDE,
   and the `createExclusionIndexStub` (non-`=`/non-btree operators) — so every
   EXCLUDE form keeps its predicate.
4. **Deparse** (`internal/executor/expr.go`, `buildConstraintDefString`): the
   EXCLUDE branch appends ` WHERE ` + `idx.PredicateString` after the
   operator/INCLUDE list and **before** the `DEFERRABLE` clauses, matching the
   upstream byte order. `defaultExprToSQL` already fully parenthesizes the
   predicate (`(b > 0)`), so the render is byte-identical to PG's `WHERE (%s)`.

The predicate is the same `parser.Expr` representation a partial *index* carries,
so `pg_get_indexdef` on the backing index also renders the WHERE clause
correctly (consistency between the two deparse contexts).

## Blast radius

A non-partial EXCLUDE (`ExclusionWhere=nil`, `PredicateString=""`, the common
case) is byte-unchanged: the new `WHERE` append is gated on a non-empty
`PredicateString`. The new grammar only *adds* an accepted clause; no previously
valid statement changes meaning, and no WAL/dump-format change. Dump-fidelity
only — goopg's v0 exclusion enforcement does not yet filter rows by the partial
predicate (consistent with the minimal exclusion semantics already in place), so
the predicate is recorded and re-emitted for the day enforcement is wired but
does not change current runtime behaviour.

## Verification

* New **DU-002 slice 310** in `TestPort_PgDumpConnectionSetup`
  (`internal/testport/pgdump_connsetup_test.go`): `CREATE TABLE public.pex (a
  integer, b integer, CONSTRAINT pex_excl EXCLUDE USING btree (a WITH =) WHERE (b
  > 0))`; the dump now emits `ADD CONSTRAINT pex_excl EXCLUDE USING btree (a WITH
  =) WHERE (b > 0);`, asserted as a substring of real pg_dump 18.3 stdout. PASS.
* New unit `TestBuildConstraintDefExclusionWhere`
  (`internal/executor/constraintdef_nnd_test.go`): pins the deparse render +
  the WHERE-before-DEFERRABLE order + the non-partial control.
* New integration `TestExclusionConstraintPartialWhereRoundTrip`
  (`internal/executor/exclusion_constraint_test.go`): parse→catalog→deparse —
  asserts the backing index gains `HasPredicate` / `PredicateString="(b > 0)"`
  and the def string round-trips.
* `go test ./internal/executor/ ./internal/parser/ ./internal/catalog/` PASS;
  `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` PASS.
* `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

* pg_dump 002–010 catalog-view parity battery (further slices).
* extended-protocol commit-time deferral (architecturally entangled — extended
  protocol is auto-commit-per-statement).
