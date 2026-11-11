# M0148\-0001j4 — one OID counter for routines, relations and types

**Parent:** M0148\-0001j3 · **Status:** item (1) landed 2026\-11\-11; the task
stays open for items (2) to (5)

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

## Not covered

- **CREATE TABLE draws one OID; PG draws three.** PG draws the relation,
  its row type and the row type's array type. goopg's tables
  (`a`=16406, `b`=16408) draw one each. Absolute OIDs therefore drift from
  PG's, but the relative order of objects is now PG's. Ledger row.
- **j4 items (2) to (5) are still open:**
  - composite\-array `proargtypes`;
  - field selection on a composite parameter, and the typed\-table
    column\-option duplicate;
  - grouping sets that hash an ordered aggregate;
  - view `_RETURN` type dependencies.
