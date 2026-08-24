# M0134-0119 — `drop_operator.sql`: sizing + two contained fixes (FULL PASS)

**Status:** LANDED — case now passes byte-identical against the PG 18.3
oracle (100% parity). Two independent, contained fixes; no PARK.

## Oracle case

`postgres/src/test/regress/sql/drop_operator.sql` is short (~57 lines): it
creates two pairs of `bigint` operators — `===`/`!==` (a self-referential
`COMMUTATOR = ===` plus a `NEGATOR = ===`/`COMMUTATOR = !==` cross-link) and
`<|`/`|>` (a plain `NEGATOR = <|`/`COMMUTATOR = <|` cross-link) — then drops
one operator from each pair (`DROP OPERATOR !==`, then `DROP OPERATOR ===`;
`DROP OPERATOR |>`, then implicitly `DROP OPERATOR <|` as the final
statement) with two catalog-integrity `SELECT`s interleaved after each drop:

```sql
SELECT  ctid, oprcom
FROM    pg_catalog.pg_operator fk
WHERE   oprcom != 0 AND
        NOT EXISTS(SELECT 1 FROM pg_catalog.pg_operator pk WHERE pk.oid = fk.oprcom);

SELECT  ctid, oprnegate
FROM    pg_catalog.pg_operator fk
WHERE   oprnegate != 0 AND
        NOT EXISTS(SELECT 1 FROM pg_catalog.pg_operator pk WHERE pk.oid = fk.oprnegate);
```

Both must return `(0 rows)` after every drop: PG never leaves a sibling
operator's `pg_operator.oprcom`/`oprnegate` pointing at an OID that no longer
exists in `pg_operator`.

Sized live via `scripts/pg-regress-runner.sh --verbose drop_operator` against
the PG 18.3 oracle.

- **Before (first live run):** 30-line diff, 0% parity. `CREATE OPERATOR
  !==(...)` itself failed with a spurious `ERROR: only boolean operators can
  have negators`, cascading into a second spurious `operator does not exist`
  on the following `DROP OPERATOR`, and an identical pair for the `<|`/`|>`
  block.
- **After root-cause #1's fix alone:** 35-line diff (worse-looking, but no
  new bug — see below), the two spurious `CREATE`/`DROP` error pairs gone,
  replaced by the file's actual target behavior: both catalog-integrity
  `SELECT`s now returned a dangling row (`(1 row)` where PG has `(0 rows)`).
- **After root-cause #2's fix:** 0-line diff, `PASS drop_operator (57 lines)`,
  100.0% parity.

## Root cause #1: builtin-proc return-type lookup missing four int8 comparison functions

`CREATE OPERATOR`'s `NEGATOR`/`COMMUTATOR`/`RESTRICT`/`JOIN`/`MERGES`/
`HASHES` validation (`internal/executor/operators_ddl.go`, mirroring PG's
`OperatorValidateParams` in `operatorcmds.c`) requires the operator's
underlying `PROCEDURE` to return `bool` before it may carry a `NEGATOR`
clause (and similarly `RESTRICT`/`JOIN`/`MERGES`/`HASHES`). It resolves the
function's return type via `catalog.LookupBuiltinProc`, a hand-curated
`map[string]BuiltinProc` of PG's real `pg_proc.dat` OIDs/return
types/arg types for the small set of builtin functions goopg's `CREATE
OPERATOR`/`CREATE OPERATOR CLASS` fixtures reference (`int4eq`,
`btint4cmp`, `btint8cmp`, etc. — populated incrementally, one M0134 case at
a time, per its own doc comments).

`drop_operator.sql`'s `PROCEDURE = int8ne` (for `!==`, which then declares
`NEGATOR = ===`) was not in that map: `int8eq`/`int8ne`/`int8lt`/`int8gt`
had never been added, even though `int4eq`/`btint4cmp` (their int4
counterparts) were. `LookupBuiltinProc("int8ne")` returned `(zero-value,
false)`, leaving `funcRetType == ""`, so `returnsBool := strings.EqualFold(
funcRetType, "bool") || ...` evaluated `false` for a function that
genuinely returns `bool` — misfiring `"only boolean operators can have
negators"` on a fully valid definition.

Fixed by adding all four entries to `builtinProcsByName`
(`internal/catalog/catalog.go`) with PG's real `pg_proc.dat` OIDs (467-470,
cross-checked against goopg's own pre-existing
`internal/catalog/pg_proc_names_generated.go` OID→name table, which already
carried the same 467→`int8eq`/468→`int8ne`/469→`int8lt`/470→`int8gt`
mapping generated from the real catalog) — `RetType: "bool"`,
`ArgTypes: []string{"int8", "int8"}`, `Volatile: "i"`, matching the
`int4eq`/`btint4cmp` curation style immediately above them in the file.

This alone made `CREATE OPERATOR`/`DROP OPERATOR` themselves succeed for
both fixture pairs, exposing root cause #2's genuine gap (see next section)
underneath — hence the interim 35-line diff being a *forward* step (two
distinct spurious errors gone) even though the line count went up slightly
(the two `SELECT`s now returned a real, previously-masked, dangling-row
result instead of being skipped due to the earlier `CREATE`/`DROP` failures).

## Root cause #2: `DROP OPERATOR` never cleared sibling `oprcom`/`oprnegate` cross-references

PG's `RemoveOperatorById` (`postgres/src/backend/commands/operatorcmds.c`
~446-470) calls `OperatorUpd(operOid, op->oprcom, op->oprnegate, true)`
*before* deleting the operator's own `pg_operator` row.
`OperatorUpd`/`isDelete=true` (`postgres/src/backend/catalog/pg_operator.c`
~671-820) looks up the commutator and negator operators the row-being-
deleted points to, and if either one's own `oprcom`/`oprnegate` still
points back at the operator being deleted, nulls it to `InvalidOid` — this
is exactly the check the file's two `SELECT`s verify held.

goopg's `DROP OPERATOR` (`internal/executor/operators_ddl.go`'s
`objType == "operator"` arm) called `catalog.InMemory.DropUserOperator`
unconditionally, which only ever deleted the map entry for the operator
being dropped — no other `UserOperator`'s `CommutatorOID`/`NegatorOID` field
was ever touched. So dropping `!==` (whose `NegatorOID` pointed at `===`)
left `===`'s own `NegatorOID` still pointing at `!=='s` now-freed OID — the
live-registry-driven `pg_catalog.pg_operator` virtual view
(`catalog.go`'s `pgOperator.VirtualRows`, built from `ListUserOperators()`)
kept emitting that dangling value verbatim, and the file's
`NOT EXISTS(SELECT 1 FROM pg_operator pk WHERE pk.oid = fk.oprnegate)`
check correctly flagged it.

Fixed by porting `OperatorUpd`'s delete-time cleanup directly into
`catalog.InMemory.DropUserOperator`: before removing the target operator's
map entry, if its `CommutatorOID`/`NegatorOID` (excluding the
self-referential case, e.g. `===`'s own `COMMUTATOR = ===`) names another
live `UserOperator` whose own `CommutatorOID`/`NegatorOID` still points back
at the OID being dropped, that field is zeroed. A new unexported helper,
`(*InMemory) userOperatorByOIDLocked`, does the OID lookup (linear scan over
the small `userOperators` map, called while `c.mu` is already held —
mirrors the existing `LookupUserOperatorByOID`'s scan shape but avoids a
double-lock).

## Verification

Live, byte-for-byte against the oracle:

```
scripts/pg-regress-runner.sh --verbose drop_operator
...
PASS  drop_operator  (57 lines)
======================================================================
pg-regress-runner: 1/1 PASS (100.0% parity, 0 skipped)
```

Gates run:

```
go build ./...
go test ./internal/parser/... ./internal/executor/... ./internal/postmaster/... ./internal/catalog/...
RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh
```

All pass. This is not a planner/executor cost-model change (no optimizer
code touched), so `scripts/tpch-spotcheck.sh` and the TPC-DS SF0.5 gate were
not run, per the task's own scope note.

## Residual gap

None specific to this case — it is a full, byte-identical pass. The fix is
narrow (two independent bugs, one four-entry data-table addition, one
delete-time cross-reference cleanup) and does not attempt to port PG's full
`OperatorValidateParams`/`OperatorUpd`/`OperatorCreate` machinery (e.g. the
conflict-with-existing-pair checks or the shell-operator two-pass resolution
edge cases already catalogued as open gaps in
`docs/design/m0134-0113-create-operator-sizing.md`, items 4 and 6 — those
remain open for `create_operator.sql`, unaffected by this loop).

## Resume point

None needed for this file. If a future case exercises `ALTER OPERATOR ...
SET (COMMUTATOR = ...)` (redefining an *existing* operator's commutator
after the fact) rather than `CREATE`/`DROP`, re-check whether
`OperatorUpd`'s `isDelete=false` branch (the "already set to something else
→ error" conflict check, `pg_operator.c` ~723-748) needs a parallel port —
`drop_operator.sql` never exercises that branch, so it was not built here.
