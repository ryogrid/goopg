# M0148\-0001j4 — one OID counter for routines, relations and types; user type OIDs in pg\_proc

**Parent:** M0148\-0001j3 · **Status:** items (1), (2) and (3a) landed
2026\-11\-11; the task stays open for items (3b), (4) and (5)

## Divergence

```sql
create type myvarchar;
create function myvarcharin(cstring, oid, integer) returns myvarchar …;
…
create domain myvarchardom as myvarchar;
drop type myvarchar;
```

- **PG 18.3** lists the dependents in OID order, newest first
  (`findDependentObjects` sorts with `object_address_comparator`):
  `type myvarchardom` first, then `function myvarcharin(…)`.
- **goopg before** listed the function first. Routines took OIDs from a
  private counter starting at `FirstRoutineOID` (`1<<17`). Relations,
  types and domains took theirs from `InMemory.nextOID`, which starts at
  16384. So every function sorted above every table or type, whatever the
  creation order.
- **Also affected:** typed\_table (`persons, get_all_persons(), persons2`)
  and the CASCADE notice lists.

## PG mechanism

`ProcedureCreate` (pg\_proc.c) gets its OID from `GetNewOidWithIndex`.
`heap_create_with_catalog` and `TypeCreate` use the same call, and it draws
from `GetNewObjectId`'s single cluster\-wide counter
(`ShmemVariableCache->nextOid`). Objects therefore get OIDs in creation
order across catalogs.

## Change

- `NewInMemory` calls `c.routines.shareOIDCounter(c.AllocOID,
  c.AdvanceNextOIDPast)` (internal/catalog/catalog.go).
  - `Routines.Create` draws a new routine's OID from the catalog counter.
  - `Routines.CreateDuringRecovery` (the pg\_proc heap reload) advances
    the catalog counter past each recovered OID.
- **Lock order.** `InMemory.DropDatabase` holds `c.mu` while it calls
  `routines.DropAllInDB`, so the order is `c.mu` → registry lock. Calling
  `AllocOID` under the registry lock would invert that order and could
  deadlock.
  - `Create` therefore peeks under a read lock and draws the OID before it
    takes the write lock.
  - It retries if the signature vanished in between (a concurrent DROP).
- **No OID drawn when PG draws none.**
  - CREATE OR REPLACE keeps its OID.
  - A duplicate CREATE fails before allocation.
  - An OID left unused after losing a race is just a gap, like an aborted
    CREATE's.
- **Fallback.** A bare `NewRoutines()` registry, used in unit tests, keeps
  its own counter from `FirstRoutineOID`.
- **Existing clusters.** Their routines have OIDs ≥ 131072, and the reload
  moves the shared counter above them. New relation OIDs therefore cannot
  collide with them.

## Tests

`TestRoutineOIDsShareTheCatalogCounter` (catalog) checks:

- a function created between two tables gets an OID between theirs;
- CREATE OR REPLACE and a duplicate CREATE draw no OID;
- a recovered routine advances the counter;
- a bare registry still starts at `FirstRoutineOID`.

Manual probe with two restarts: `a`, `fa`, `b`, `dd` got 16406, 16407,
16408, 16409. After the restarts `fa` keeps 16407, and new objects get
16455 and 16456 (the counter resumes above the checkpointed value). Both
functions still execute.

## Measurement

Regress runner, HEAD (`d5a987211`) → change, 19 suites:

| suite | before | after |
|---|---|---|
| create\_type | 406 | 402 |
| typed\_table | 133 | 131 |
| plpgsql | 3991 | 3962 |
| alter\_generic | 804 | 805 |

- The create\_type and typed\_table hunks that went away are exactly the
  OID\-order lists.
- plpgsql and alter\_generic are known nondeterministic cases. Their
  movement here is variance.
- The other 15 suites are line\-count identical.

## Item (2): user type OIDs in pg\_proc

### Divergence

`create type st as (a int, b text);` followed by
`create function f3(st, en, en[], dm, rg) returns st[] …`:

- **PG 18.3** stores each type's OID in `proargtypes` / `proallargtypes` /
  `prorettype`. It takes the OID that `LookupTypeName` found during
  `interpret_function_parameter_list` and `ProcedureCreate`, so an array
  of a user type gets the array type's OID.
- **goopg before** stored a wrong OID for every user enum, domain,
  composite or range type:
  - The pg\_proc heap row and the `(proname, proargtypes, pronamespace)`
    index key resolved names with `catalog.TypeNameToOID`, which maps any
    unknown name to text (25).
  - The virtual pg\_proc view, which is what SELECT reads, used its own
    name map and wrote 0.

### Change

`executor.PGProcTypeOID(cat, spelling, routineDB)`
(internal/executor/sys\_pg\_proc.go) resolves a type in this order:

1. A non\-text answer from `TypeNameToOID` is a built\-in and wins.
2. Otherwise the user enum, domain, composite and range registries of the
   routine's database are consulted. `t[]` returns the array type's OID.
3. The text fallback is kept only for a name that resolves nowhere.

Database keys: routines carry the namespace key (`NamespaceDBOid`), but
types are keyed by the raw connection OID. So for the default namespace the
resolver tries every raw OID that folds into it: 5 (postgres), 1, and 0.

Callers — all three producers now agree:

- The heap row (`buildPGProcRow` via `pgProcArgOIDs`) and the index key
  (`insertPgProcIndexEntries`) share `pgProcInputArgOIDs(cat, r)`.
- The view (`userRoutineTypeOIDStr` in internal/initdb/pg\_proc\_view.go)
  checks in this order:
  1. the CREATE\-time resolved OID, which now also reaches the view for the
     `char` spellings;
  2. its own built\-in map;
  3. the shared resolver.

  It keeps 0 for an unresolved name.

The routine reload reads its JSON metadata, not these OIDs, so the change
is restart\-safe.

### Tests and measurement

- **`TestPGProcUserTypeOIDs` (executor)** checks every argument OID:
  - composite, composite array, enum, enum array and domain resolve to
    their registry OIDs;
  - `int[]` and `text` keep their built\-in OIDs;
  - the result type `st[]` resolves;
  - the text fallback stays for an unknown name.
- **Server probe.** Before this change, `proargtypes` of `f1(st[])` was 0.
  It is now `16407` (`_st`). For `f3` it is
  `16406 16409 16410 16412 16414`, with prorettype `_st`.
  `f5("char", char, jsonb)` gives `18 1042 3802`.
- **Regress (19 suites).** Line counts are neutral; only the known
  nondeterministic suites moved, by a few lines.

### Blocked by P0\-E26

After a restart the composite type registry is empty: composite types are
registered only by DDL, and nothing re\-registers them from pg\_type. Their
OIDs therefore fall back to 0 in the view (and to 25 in a rewritten heap
row) until P0\-E26 is fixed. Enum, domain and range OIDs survive a restart.

## Item (3a): field selection on a composite SQL\-function parameter

### Divergence

`create type st as (total int, n int);` and then:

- **`select (s).total`, `select ($1).total`, `return (s).total + (s).n`**
  (SQL functions): goopg failed with `column notation .total applied to
  type unknown`. PG returns the field.
- **`select s.total`** failed with a syntax error at `.`.
- **PL/pgSQL `return (s).total`** failed with `unsupported PL/pgSQL
  expression *parser.FieldSelect`.

PG 18.3 returns 5, 5, 7 and 5 for these calls. Regress rangefuncs
`extractq2(t int8_tbl)` (`select t.q2`) and updatable\_views
`rw_view1_aa` hit the same failure.

### PG mechanism

- **Typed Params.** `sql_fn_parser_setup` (executor/functions.c) installs
  `sql_fn_param_ref`, so each Param carries its argument's declared type.
  `transformIndirection` / `ParseFuncOrColumn` then build a FieldSelect
  against that composite type.
- **Name resolution.** `sql_fn_post_column_ref` resolves the names `s`,
  `s.f` and `fn.s` (and `fn.s.f`) as the parameter or a field of it. A table
  column always wins ("Never override a table\-column reference").

### Change (three sibling paths)

- **Planner.** `catalog.SearchPathCatalog.ParamTypes` and
  `ParamDeclaredType(n)` carry the declared types.
  - The optimizer's `fieldSelectOnArg` asks for them through
    `paramDeclaredType`, which walks the catalog wrapper chain the way
    `currentTempOwner` does.
  - It does so only when the operand is an untyped `$n`.
  - `bindSQLRoutineParamTypes` sets them on the body's plan catalog in
    `executeSQLRoutine`, `executeSQLProcedureCore` and
    `evalSQLFunctionSetof`.
  - A routine without parameters clears types inherited from its caller.
- **Named\-parameter rewrite** (`rewriteSQLNamedParamsFor`):
  - `name.field` becomes `($n).field`;
  - `x.name` (the parameter name as a later part) stays a column reference;
  - `funcname.name` becomes `$n`, so `fcompos3.v.*` becomes `($1).*`.
- **PL/pgSQL.** `lowerPLpgSQLExpr` gets a FieldSelect arm: `(v).field` over
  a composite variable goes through the existing `v.field` path.

### Tests and measurement

- **`TestRewriteSQLNamedParamsQualified`** covers the qualified, column,
  `.*`, string\-literal and function\-qualifier rewrites.
- **`TestSQLFunctionCompositeParamFieldSelect`** checks the four SQL forms
  and the PL/pgSQL form end to end.
- **Probe.** Also correct: setof unions, nested calls, NULL arguments, and
  `t.total + k` with a table alias.
- **Regress, 32 suites:**
  - rangefuncs 2289 → 2268, rowtypes 1298 → 1297, updatable\_views
    4018 → 4017;
  - every SQL\-function body that previously failed with "syntax error at
    `.`" now parses (extractq2, fcompos3, rw\_view1\_aa);
  - the other suites are unchanged or show only the known variance
    (plpgsql, polymorphism, alter\_generic).

### Remaining

- **Column precedence is not modelled.** Text substitution cannot apply
  PG's rule that a table column beats a parameter. `s.f` therefore binds
  to parameter `s` even when a FROM item is also called `s`.
- **A view's row type is not resolved.** `compositeTypeFields` excludes
  views, so `rw_view1_aa(x rw_view1)` now fails with "not a composite type"
  instead of a syntax error.
- **`(param).*` as a function argument** fails with "unsupported expression
  IndirectionStar" (regress rowtypes fcompos3).
- **Item (3b) is still open:** `CREATE TABLE … OF t (col WITH OPTIONS …)`
  repeating a column (transformOfType, 42701).

## Not covered

- **CREATE TABLE draws one OID; PG draws three.** PG draws the relation,
  its row type and the row type's array type. goopg's tables
  (`a`=16406, `b`=16408) draw one each. Absolute OIDs therefore drift from
  PG's, but the relative order of objects is now PG's. Ledger row.
- **j4 items (3b), (4) and (5) are still open:**
  - the typed\-table column\-option duplicate;
  - grouping sets that hash an ordered aggregate;
  - view `_RETURN` type dependencies.
