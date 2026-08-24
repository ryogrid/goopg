# M0134-0109 — `create_am.sql`: built-in AM handler function resolution

Status: PARKED (`failed`, sized live 2026-08-24). One contained, oracle-verified
fix landed; the case's dominant remaining diff is a structurally larger,
unrelated feature gap (`CREATE TABLE ... USING <am>` and friends), not fixed
here.

## What this test exercises

`postgres/src/test/regress/sql/create_am.sql` covers `CREATE ACCESS METHOD`
(both index-AM and table-AM variants), custom opclasses over a second index
AM (`gist2`, an alias for the built-in GiST handler), `DROP ACCESS METHOD
[CASCADE]`, `CREATE TABLE ... USING <am>`, `ALTER TABLE/MATERIALIZED VIEW
... SET ACCESS METHOD`, partitioned-table AM inheritance, `default_table_access_method`,
and `pg_depend`/`pg_describe_object` bookkeeping for AM-owned objects.

## Root cause found and fixed

`CREATE ACCESS METHOD gist2 TYPE INDEX HANDLER gisthandler;` (and every
variant naming one of PG's 7 built-in AM handler functions —
`heap_tableam_handler`, `bthandler`, `hashhandler`, `gisthandler`,
`ginhandler`, `spghandler`, `brinhandler`) raised `function gisthandler(internal)
does not exist`, even though these functions are seeded into `pg_proc` at
initdb bootstrap (`internal/initdb/pg_proc_seed_data.go`, OIDs 3 and 330-335)
and are visible to `SELECT * FROM pg_proc`.

`execCreateAccessMethod` → `resolveAccessMethodHandlerFunc`
(`internal/executor/operators_ddl.go`) only searched
`catalog.Catalog.Routines()` — the live `CREATE FUNCTION`/`CREATE PROCEDURE`
registry — for a one-`internal`-arg routine with a matching return type
(`index_am_handler`/`table_am_handler`). goopg has no pluggable
storage-engine registry, so the 7 built-in handler names were never
registered there; they exist only as static `pg_proc` seed rows consumed by
`initdb`'s own bootstrap AM-registration path (`internal/initdb/initdb.go`
comments at lines 1068-1078), not by anything reachable from
`internal/executor` (which cannot import `internal/initdb` — an established
import-cycle boundary, see `pg_proc_names_generated.go`'s header for the
same constraint applied to a different lookup).

## Fix

Added `builtinAMHandlerFuncs`, a small hardcoded
`name -> {OID, AMType}` table for exactly the 7 built-in handler functions
(`internal/executor/operators_ddl.go`), duplicating the fixed OIDs already
seeded by `internal/initdb/pg_proc_seed_data.go` — the same leaf-package
duplication pattern `internal/catalog/pg_proc_names_generated.go` already
uses for the OID→name direction, justified by the same import-cycle
constraint (memory: "Version constants must live in leaf config pkg").

`resolveAccessMethodHandlerFunc` now falls back to this table (for an
unqualified or `pg_catalog`-qualified name) after the `Routines()` scan comes
up empty, before reporting 42883. A name that resolves but has the wrong AM
kind (e.g. `HANDLER heap_tableam_handler` under `TYPE INDEX`) reports PG's
own 42809 `function %s must return type %s` — matching
`postgres/src/backend/commands/amcmds.c`'s `lookup_am_handler_func` exactly,
since goopg's "return type" for a built-in handler is inferred from the
table's fixed `AMType` field rather than an actual return-type check.

## Verified

Manual psql session against a throwaway server (`/tmp/amtest-data`, port
5533) reproduced every handler-resolution line from
`postgres/src/test/regress/expected/create_am.out` byte-for-byte:

```
CREATE ACCESS METHOD gist2 TYPE INDEX HANDLER gisthandler;        -- OK (was 42883)
CREATE ACCESS METHOD bogus TYPE INDEX HANDLER int4in;              -- 42883 (unchanged, correct)
CREATE ACCESS METHOD bogus TYPE INDEX HANDLER heap_tableam_handler;-- 42809 "must return type index_am_handler"
CREATE ACCESS METHOD heap2 TYPE TABLE HANDLER heap_tableam_handler;-- OK (was 42883)
CREATE ACCESS METHOD bogus TYPE TABLE HANDLER int4in;               -- 42883 (unchanged, correct)
CREATE ACCESS METHOD bogus TYPE TABLE HANDLER bthandler;            -- 42809 "must return type table_am_handler"
```

`go build ./...` clean. `go test ./internal/catalog/... ./internal/executor/...`
PASS (cache-warm).

## Sizing verdict: PARKED

Running the full script against the fix showed the handler-resolution fix
unblocks only the first ~8 lines; the test immediately hits a structurally
unrelated wall: **`CREATE TABLE ... USING <am>` has no parser support at
all** (`syntax error at or near "using"`), and none of `CREATE TABLE AS
... USING`, `CREATE MATERIALIZED VIEW ... USING`, `ALTER TABLE ... SET
ACCESS METHOD`, or partitioned-table AM inheritance exist anywhere in goopg
either. The test also depends on `fast_emp4000` (a `create_index.sql`
fixture — itself excluded, "GiST index; out of scope for goopg v0." per
`internal/testport/regress_suite_test.go`'s `regressExcluded` table) and a
real custom opclass over `gist2`, which requires functioning GiST index
machinery goopg does not have.

This is the table/index-AM equivalent of a from-scratch storage-engine
pluggability feature — multiple milestones' worth of work (parser grammar,
DDL executor, planner AM-choice, catalog `relam` plumbing, `pg_depend`
tracking, partition inheritance) — not a contained fix. Parked per the
established M0134 pattern (cf. M0134-0104/-0105/-0106/-0108): the one
genuinely independent, oracle-verified sub-fix is landed; the rest is
ledgered.

## Deferred (see `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0109)

- `CREATE TABLE/MATERIALIZED VIEW/... USING <am>` clause: zero parser
  support (`internal/parser/ddl.go`'s `CREATE TABLE`/`CREATE TABLE AS`/
  `CREATE MATERIALIZED VIEW` paths have no `USING` token handling).
- `ALTER TABLE/MATERIALIZED VIEW ... SET ACCESS METHOD [DEFAULT]`: not a
  parser case.
- `default_table_access_method` GUC: not validated/consulted anywhere
  (`SET default_table_access_method = 'heap2'` would need to both validate
  against `pg_am` and feed `relam` selection on unqualified `CREATE TABLE`).
- Partitioned-table AM inheritance (new partitions inherit the current
  default AM at creation time, independent of the partition root's AM).
- `pg_depend` rows recording a table's dependency on its (non-default) AM,
  and `pg_describe_object` support for describing those dependency rows.
- Full pluggable index-AM machinery for a genuinely new index AM (`gist2`
  exercises an *alias* over the existing GiST handler, but still requires
  `CREATE OPERATOR CLASS ... USING gist2` and an actual index build/scan
  through the aliased AM — goopg's GiST support itself is excluded from the
  regress harness, `regressExcluded["gist"]`).
