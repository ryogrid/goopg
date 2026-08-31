# 0119-0004ad — CONSTRAINT TRIGGER round-trip in pg_dump (DU-002 slice 327)

Status: accepted

## Problem

A `CREATE CONSTRAINT TRIGGER` could not round-trip through pg_dump, and in fact
could not even be re-parsed: pg_dump's `getTriggers` emits
`pg_get_triggerdef(t.oid, false)` verbatim, and `pg_get_triggerdef_worker`
(ruleutils.c) renders a constraint trigger as

```
CREATE CONSTRAINT TRIGGER <name> AFTER <ev> ON <nsp>.<rel>
    [NOT ]DEFERRABLE INITIALLY {IMMEDIATE|DEFERRED} FOR EACH ROW
    EXECUTE FUNCTION <nsp>.<fn>(...);
```

The `CONSTRAINT ` prefix is gated on a valid `pg_trigger.tgconstraint`, and the
deferrability clause (`[NOT ]DEFERRABLE INITIALLY {IMMEDIATE|DEFERRED}`) is
emitted between the ON-table name and `FOR EACH ROW` for every constraint
trigger — PG always spells out the full clause, even for the
`NOT DEFERRABLE INITIALLY IMMEDIATE` default.

goopg had three gaps:

1. **Parser dead branch.** The `CREATE CONSTRAINT TRIGGER` case in
   `parseCreate` matched via `p.acceptIdentKeyword("constraint")`, but
   `CONSTRAINT` is a *reserved* keyword token — `acceptIdentKeyword` only
   matches unreserved identifiers, so the branch never fired and
   `CREATE CONSTRAINT TRIGGER …` failed to parse outright.
2. **No constraint state.** `CreateTriggerStmt` / `catalog.Trigger` had no
   `IsConstraint` / `Deferrable` / `InitDeferred` fields, and the
   `[NOT] DEFERRABLE [INITIALLY …]` clause was never parsed.
3. **No deparse.** `buildTriggerDefString` always wrote `CREATE TRIGGER` and
   never emitted the deferrability clause; `pg_trigger.tgconstraint` /
   `tgdeferrable` / `tginitdeferred` were hard-coded `0`/`f`/`f`.

So a constraint trigger was unusable (parse error) and, had it parsed, would
have silently degraded into a plain `CREATE TRIGGER` on restore.

## Fix (dump-fidelity only)

goopg does **not** enforce constraint-trigger semantics (deferred firing at
COMMIT); this slice reproduces the dump text only.

- **Parser** (`internal/parser/ast.go`, `ddl.go`): new
  `CreateTriggerStmt.{IsConstraint,Deferrable,InitDeferred}`.
  `parseCreateTriggerTail` takes an `isConstraint bool`; the `CREATE CONSTRAINT
  TRIGGER` case now matches the `KwConstraint` keyword token (fixing the dead
  `acceptIdentKeyword` branch) and passes `true`. After `ON <table>`, a
  constraint trigger parses the optional `[NOT] DEFERRABLE [INITIALLY
  {IMMEDIATE|DEFERRED}]` via the existing `parseConstraintDeferrable` helper
  (reused from CREATE TABLE constraints). The FK-internal `FROM
  referenced_table` clause is not modelled.
- **Catalog** (`internal/catalog/catalog.go`): new
  `Trigger.{IsConstraint,Deferrable,InitDeferred,ConstraintOID}`. The
  `pg_trigger` virtual builder projects a non-zero `tgconstraint`
  (= `ConstraintOID`) plus `tgdeferrable`/`tginitdeferred` for a constraint
  trigger, and the prior `0`/`f`/`f` for an ordinary trigger.
- **Executor** (`internal/executor/operators_ddl.go`): `execCreateTrigger`
  copies the three flags and, for a constraint trigger, allocates
  `ConstraintOID` from the catalog OID counter (the implicit `pg_constraint`
  row of contype `'t'`).
- **Deparse** (`internal/executor/expr.go`): `buildTriggerDefString` writes
  `CREATE CONSTRAINT TRIGGER` when `IsConstraint`, and emits the
  `[NOT ]DEFERRABLE INITIALLY {DEFERRED|IMMEDIATE} ` clause right after the
  ON-table name, mirroring `pg_get_triggerdef_worker`.

## Blast radius

Nil for ordinary triggers: `IsConstraint` defaults `false`, so the deparse and
the `pg_trigger` projection are byte-identical to slice 326 for every
non-constraint trigger. The dead `acceptIdentKeyword("constraint")` branch is
replaced by a keyword-token match, so `CREATE CONSTRAINT TRIGGER` goes from
parse-error to a real statement. TPC-H/pgbench carry no constraint triggers.

## Gates

- `TestParseCreateConstraintTrigger` (parser): default deferrability, explicit
  NOT DEFERRABLE, DEFERRABLE INITIALLY DEFERRED/IMMEDIATE, plain-trigger control
  keeps `IsConstraint=false`.
- `TestBuildTriggerDefString` (executor): two new cases — `CREATE CONSTRAINT
  TRIGGER … NOT DEFERRABLE INITIALLY IMMEDIATE …` and `… DEFERRABLE INITIALLY
  DEFERRED …`.
- `TestPort_PgDumpConnectionSetup` **DU-002 slice 327**: `trg_cdef`
  (default → `NOT DEFERRABLE INITIALLY IMMEDIATE`) and `trg_cdfr`
  (`DEFERRABLE INITIALLY DEFERRED`) re-emit byte-identical vs real pg_dump 18.3.
- `internal/parser` + `internal/catalog` + `internal/executor` suites PASS;
  `go build ./...` clean; pgbench smoke via pre-commit hook.

## Still open under M0119-0004

Richer trigger forms — `WHEN (condition)` (`tgqual`, needs an OLD/NEW-qualified
expression deparser), `REFERENCING … OLD/NEW TABLE` transition tables
(`tgoldtable`/`tgnewtable`); GRANT/ACL (`relacl`) + named-role policies (per-role
OID registry + the `ARRAY(SELECT …)`/`quote_ident` query stack goopg lacks);
extended-protocol commit-time deferral.
