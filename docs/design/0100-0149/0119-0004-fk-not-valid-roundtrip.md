# 0119-0004j — `NOT VALID` FOREIGN KEY round-trip in pg_dump (DU-002 slice 307)

**Milestone:** M0119-0004 (pg_dump 002–010 catalog-view parity battery; source M0110-0001)
**Status:** accepted / implemented
**Oracle:** PostgreSQL 18.3 (`./postgres/local_install`), `pg_dump --no-sync`

## Problem

A constraint added with `NOT VALID` is recorded in PG as
`pg_constraint.convalidated = 'f'`: existing rows were never checked, so the
constraint is enforced only for *new* writes until an explicit
`ALTER TABLE … VALIDATE CONSTRAINT` runs. pg_dump must preserve that state — a
restored FK that was `NOT VALID` on the source must remain `NOT VALID`, or the
restore would (a) be forced to re-scan and validate the very rows the operator
deliberately grandfathered and (b) potentially fail outright on data that does
not satisfy the predicate.

PG carries the state through `pg_get_constraintdef_worker` (ruleutils.c:2604),
whose **shared tail** — common to every constraint type, appended *after* the
`DEFERRABLE`/`INITIALLY DEFERRED` clauses — emits:

```c
else if (!conForm->convalidated)
    appendStringInfoString(&buf, " NOT VALID");
```

pg_dump's `getConstraints` renders each FK via `pg_get_constraintdef(oid)` and
emits `ALTER TABLE ONLY … ADD CONSTRAINT <name> <condef>;`, so the suffix rides
along automatically.

goopg already tracked the unvalidated state end-to-end:

* the parser consumes the `NOT VALID` trailer on `ALTER TABLE ADD CONSTRAINT …
  FOREIGN KEY` (`internal/parser/ddl.go`, `act.NotValid`);
* the executor stores it on `catalog.ForeignKey.NotValid`
  (`internal/executor/operators_ddl.go`);
* the `pg_constraint` virtual builder projects `convalidated='f'`
  (`internal/catalog/catalog.go:4938`).

The only gap was the **deparse**: `buildForeignKeyDefString`
(`internal/executor/expr.go`) — the implementation of `pg_get_constraintdef`
for FKs that pg_dump reads — never emitted the ` NOT VALID` tail. So a NOT-VALID
FK dumped **without** the suffix and silently re-validated on restore.

## Fix

Append ` NOT VALID` to the FK def string after the `DEFERRABLE` clauses, exactly
where the upstream shared tail sits:

```go
if fk.Deferrable {
    def += " DEFERRABLE"
    if fk.InitiallyDeferred {
        def += " INITIALLY DEFERRED"
    }
}
if fk.NotValid {
    def += " NOT VALID"
}
```

One-line logic addition; no new fields, no parser/catalog/WAL/dump-format
change. The `NotValid` flag and every other consumer already existed.

## Blast radius

Bounded to `buildForeignKeyDefString`. A validated FK (`NotValid=false`, the
overwhelming majority) is byte-unchanged. CHECK constraints are unaffected — they
do not yet carry a `NotValid` flag in the catalog (`NamedCheckConstraint` has no
such field), so a `CHECK … NOT VALID` round-trip remains a separate, future
slice; this change deliberately scopes to the FK path that is already wired.
goopg has no logical replication; this is dump-fidelity only.

## Verification

* New **DU-002 slice 307** in `TestPort_PgDumpConnectionSetup`
  (`internal/testport/pgdump_connsetup_test.go`): `public.nv_child` gets
  `ADD CONSTRAINT nv_child_fk FOREIGN KEY (ref_id) REFERENCES public.nv_ref (id)
  NOT VALID`; the dump now emits the line **with** the trailing ` NOT VALID;`,
  asserted as a substring of pg_dump's stdout.
* `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` PASS.
* `go test ./internal/executor/ ./internal/parser/ ./internal/catalog/` PASS.
* `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

* `CHECK … NOT VALID` round-trip (needs a `NotValid` field on
  `catalog.NamedCheckConstraint` + `convalidated` projection for contype='c').
* FK `MATCH FULL` round-trip (parser/deparse do not yet surface the match type).
* pg_dump 002–010 catalog-view parity battery (further slices).
* extended-protocol commit-time deferral (architecturally entangled).
