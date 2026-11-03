# M0148\-0001j2 — DROP TYPE / DROP DOMAIN: column, attribute and domain dependents

**Parent:** M0148\-0001j1 · **Status:** landed 2026\-11\-04; closed (residue in
M0148\-0001j3)

## Divergence

```sql
create type ct as (a int, b text);
create table tc (x int, y ct, z ct[]);
create type ct2 as (p ct, q int);
create domain dct as ct;
create table tc2 (d dct);
drop type ct;
```

- **PG 18.3** refuses with 2BP01 and lists:
  `column z of table tc depends on type ct[]`,
  `column y of table tc depends on type ct`,
  `column p of composite type ct2 depends on type ct`,
  `type dct depends on type ct`,
  `column d of table tc2 depends on type dct`.
  `DROP TYPE ct CASCADE` drops those five objects. The columns go, but the
  tables stay.
- **goopg before:**
  - **Plain DROP TYPE** succeeded and left the columns typed `???`.
  - **DROP DOMAIN … CASCADE** dropped every table that had a column of the
    domain (data loss).
  - **DROP DOMAIN (RESTRICT)** found nothing in a live server, because its
    table scan used the raw database OID instead of the namespace key, so it
    dropped the domain under live columns.

## PostgreSQL

Every object below has a pg\_depend row on the type:

- `AddNewAttributeTuples` records one for each attribute: on its table, or
  on the composite type's relation for an attribute of that type.
- `GenerateTypeDependencies` records one for a domain on its base type.
- An attribute typed `t[]` depends on the array type, and the array type
  depends internally on `t`.

`findDependentObjects` walks these rows depth\-first, in
`object_address_comparator` order: OID descending, then classId ascending,
then objsubid ascending.

- **Column descriptions.** `getObjectDescription` prints a column as
  `column <att> of <relation description>`.
- **Deletion.** `doDeletion` removes a column with `RemoveAttributeById`.
  It runs none of ALTER TABLE's user\-command checks, so the typed\-table
  and inherited\-column guards do not apply. Each partition's and child's
  column is reached through its own pg\_depend row.

## Change

`internal/executor/drop_type_dependents.go`:

- **Type\-like nodes.** The walk's type nodes generalise to the root type,
  domains, and their array types. Dependents are matched against each
  node's own name and OIDs, so the walk recurses through a domain to its
  columns, functions and further domains.
- **New dependent kinds:**
  - `column`: a table, keyed by (table OID, attnum). The column's declared
    type is `DeclaredTypeName` for a domain column, otherwise `Type.Name`.
  - `attribute`: a composite type's field, keyed by (relation OID, attnum).
    The new `catalog.AllCompositeTypes` lists composite types for it.
  - `domain`: matched by `BaseOID` or by the base type's name.
- **Sibling sort.** Siblings sort by the PG comparator. The class ids are
  pg\_type 1247, pg\_proc 1255 and pg\_class 1259.
- **Cascade actions:**
  - A column is dropped through `dropColumn(…, fromDependency=true)`, the
    ALTER TABLE DROP COLUMN body with the typed\-table and inherited guards
    skipped.
  - An attribute is dropped through `replaceCompositeFields`, the re\-sync
    `execAlterTypeAttrCmds` already used, now shared.
  - A domain is dropped through `dropDomainObject`: its pg\_type and
    constraint rows, then the catalog entry.
- **DROP DOMAIN** (`execDropDomain`) now uses the same walk. RESTRICT is
  2BP01 with the DETAIL list, and CASCADE drops columns, not tables.
  `catalog.DropDomain` ignores dropped columns.
- **Failed CREATE TABLE no longer leaves its table behind.** The rollback
  entry used to be recorded only just before the heap sync, so an error in
  between left the relation registered. The inline FOREIGN KEY type check
  is one such error. This happened even inside BEGIN … ROLLBACK. A deferred
  guard now records the undo entry on that error path. regress enum exposed
  it: the stranded table now correctly blocked a later DROP TYPE.

## Verification

- `TestDropTypeColumnDependents` pins PG 18.3's DETAIL and NOTICE lists for
  the example above. It also checks the surviving columns, fields and domain
  after CASCADE, and that DROP DOMAIN RESTRICT and CASCADE leave the table
  in place. `TestDropTypeDependents` (j1) still passes.
- **Live probes, byte\-identical to a scratch PG 18.3:**
  - the example above;
  - a function plus a column;
  - an enum column with an index;
  - a domain over a partitioned table, its partition, an inheritance parent
    and its child;
  - a failed CREATE TABLE inside BEGIN … ROLLBACK.
- **Regress runner, HEAD → change, 37 suites.**
  - Improved: alter\_table 3648 → 3623, domain 1752 → 1718, create\_table
    639 → 615, plpgsql 4009 → 4000, insert 1057 → 1055.
  - Unchanged: enum, after the CREATE TABLE fix; it was 38 → 138 before
    that fix.
  - Grew: create\_type 402 → 406 (the domain is now listed but in a different
    order, the routine\-OID residue below) and privileges 3879 → 3925. In
    privileges, goopg lacks type USAGE checks, so objects PG refuses
    (`permission denied for type`) exist and now correctly block `DROP
    DOMAIN priv_testdomain1`. HEAD dropped the domain under them.
  - The other 28 suites are line\-count identical.
- **Gates.** `TestPort_RegressSuite`, `TestPort_PgDump*` and
  `TestPort_Isolation*` pass, as do units, tpch\-spotcheck, sf025 (99/99),
  acceptance and the fire\-set (`fires=none` on all three corpora).

## Not covered (M0148\-0001j3)

- **OID order across catalogs.** Routine OIDs come from their own counter,
  so a domain or column listed beside functions can appear out of PG's
  order (create\_type myvarchar).
- **Type USAGE privilege checks** (`aclcheck_error_type` in CREATE TABLE,
  ALTER TABLE ADD COLUMN, CREATE TYPE, CREATE DOMAIN, CREATE FUNCTION and
  CREATE AGGREGATE).
- **DROP TYPE of a domain name.** PG accepts it; goopg's DROP TYPE does not
  resolve domains.
- **Columns through views and rules.** A view column over the type depends
  through its `_RETURN` rule, which this walk does not model.
- **After a restart, user\-type columns reload as `text`**
  (`OIDToTypeName`). The walk cannot see them, and the column's semantics
  are lost. Filed separately as S2 (P0\-E25).
