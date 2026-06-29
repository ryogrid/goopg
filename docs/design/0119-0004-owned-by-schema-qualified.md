# 0119-0004h — Schema-qualified `OWNED BY` round-trip in pg_dump (DU-002 slice 304)

**Milestone:** M0119-0004 (pg_dump 002–010 catalog-view parity battery; source M0110-0001)
**Status:** accepted / implemented
**Oracle:** PostgreSQL 18.3 (`./postgres/local_install`), `pg_dump --no-sync`

## Problem

`CREATE SEQUENCE … OWNED BY <owner>` accepts an owner reference of the form
`column`, `table.column`, or `schema.table.column`. PG resolves the **last**
dotted component as the column and everything before it as the (optionally
schema-qualified) table. pg_dump itself always re-emits the fully-qualified
3-part form (`ALTER SEQUENCE public.s OWNED BY public.t.c;`), so a faithful
round-trip must accept that input form.

goopg's `validateSeqOwnedBy` (`internal/executor/operators_ddl.go`) split the
owner string on the **first** dot:

```go
dot := strings.Index(ownedBy, ".")
tblPart := ownedBy[:dot]      // "public"        for public.owner_tbl.id
colPart := ownedBy[dot+1:]    // "owner_tbl.id"
```

For the schema-qualified form `public.owner_tbl.id` this set `tblPart="public"`,
`colPart="owner_tbl.id"`. The catalog lookup of relation `"public"` failed and
the statement errored:

```
ERROR: sequence cannot be owned by relation "public"   (SQLSTATE 42P01)
```

So a sequence created with an explicit schema-qualified `OWNED BY` could never be
created, and pg_dump's own output therefore could not be restored. (The
2-part unqualified form `owner_tbl.id` happened to work — slice 118 — because the
first dot *is* the column separator there.)

The dump side was already correct: `InMemory.dependVirtualRows`
(`internal/catalog/catalog.go`) splits the column off with `strings.LastIndex`
and the schema off the remainder with `strings.Index`. Only the **validation**
side disagreed.

## Fix

Make `validateSeqOwnedBy` mirror `dependVirtualRows`: the column is the last
dotted component.

```go
lastDot := strings.LastIndex(ownedBy, ".")
if lastDot < 0 {
    return &ExecError{Code: "42601", Pos: pos, Message: "invalid OWNED BY option"}
}
tblPart := ownedBy[:lastDot]   // "public.owner_tbl"
colPart := ownedBy[lastDot+1:] // "id"
```

The pre-existing schema-qualified-retry below (`strings.Index(tblPart, ".")`
splits `public.owner_tbl` into schema `public` + name `owner_tbl`) is now reached
with the correct `tblPart`, so the table resolves and the same-schema / virtual /
column-exists checks proceed as before. One-line logic change; no new fields, no
parser change (the parser already produces the 3-part string —
`owner.String() + "." + col`).

## Blast radius

Bounded to `validateSeqOwnedBy`. For the 1-dot (`table.column`) and the implicit
column-only forms the last-dot and first-dot splits are identical, so the
unqualified slice-118 path is byte-unchanged. The stored `OwnedBy` string and
every downstream consumer (`dependVirtualRows`, `SetSequenceOwnedBy`,
`DropSequencesOwnedByTable`) are untouched. No catalog/WAL/dump-format change.

## Verification

* New **DU-002 slice 304** in `TestPort_PgDumpConnectionSetup`
  (`internal/testport/pgdump_connsetup_test.go`): `CREATE SEQUENCE
  public.qowned_seq OWNED BY public.owner_tbl.label` — the schema-qualified
  3-part form that previously errored at CREATE time — now succeeds and the dump
  emits the canonical `ALTER SEQUENCE public.qowned_seq OWNED BY
  public.owner_tbl.label;`, byte-identical to how pg_dump re-qualifies the
  unqualified slice-118 sequence.
* `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` PASS.
* `internal/executor` sequence/identity suite PASS.
* `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

* pg_dump 002–010 catalog-view parity battery (further slices).
* extended-protocol commit-time deferral (architecturally entangled).
