# `collate.sql`: reject `COLLATE` on a non-collatable column type (M0134-0101)

## Status: PARKED (`failed`)

## Summary

`collate.sql` sized live at a 558-line diff against the PG 18.3 oracle, 0%
parity. The very first divergence — and the one that cascaded the widest,
since it left an entire `collate_test_fail` table sitting in the catalog
that PG never creates — was a missing semantic check: PostgreSQL rejects an
inline `COLLATE` clause on a column whose type carries no `typcollation`
(only `text`/`varchar`/`bpchar`/`name`, and domains/arrays over them, are
collatable):

```sql
CREATE TABLE collate_test_fail (
  a int COLLATE "C",
  b text
);
-- PG: ERROR:  collations are not supported by type integer
-- goopg (before this fix): silently created the table
```

goopg's `CREATE TABLE` path threaded `ColumnDef.Collation` straight onto
`catalog.Column.Collation` with zero validation — the collation name was
recorded for `pg_dump` round-tripping but never checked against the column's
type. Fixed with a new `validateColumnCollation` check in the `addCol`
closure of `execCreateTable` (`internal/executor/operators_ddl.go`), mirrored
against real PG's `transformColumnType`
(`postgres/src/backend/parser/parse_utilcmd.c:4044-4067`):

```c
if (column->collClause)
{
    Form_pg_type typtup = (Form_pg_type) GETSTRUCT(ctype);
    LookupCollation(cxt->pstate, column->collClause->collname, ...);
    if (!OidIsValid(typtup->typcollation))
        ereport(ERROR, (errcode(ERRCODE_DATATYPE_MISMATCH),
                 errmsg("collations are not supported by type %s", ...)));
}
```

`validateColumnCollation` reuses `catalog.TypeNameToOID` (already the
canonical alias-normalizing lookup used by `columnTypeStorageCode` right next
to it) to resolve the column's type to an OID and checks it against the
four collatable base OIDs (`OIDText`, `OIDVarChar`, `OIDBpChar`, `OIDName`).
It runs on the **domain-resolved** type name (`typeName`, already resolved by
`execCreateTable`'s `ResolveColumnType` earlier in `addCol`) so a domain over
`text` stays collatable, but reports the error using the **originally
declared** type spelling (`c.Type.Name`, e.g. `int` → `integer` via
`pgFormatTypeName`) to match PG's `format_type_be` output. Arrays need no
special case: per the existing `ColumnType.IsArray` convention, an
`int4[]` column's `Type.Name` is already the unsuffixed element name
`"int4"`, so the same OID check naturally applies to the element type.

Verified live: the `LINE 2:  a int COLLATE "C",` / `  ^` position pointer and
error text now match the PG 18.3 oracle byte-for-byte, and the phantom
`collate_test_fail` table (and its downstream `\d` divergence) is gone. The
same check also caught `CREATE DOMAIN testdomain_i AS int COLLATE "POSIX"`'s
missing `-- fail` marker in principle, but that case's `COLLATE` clause is
parsed and **discarded** by `parseCreateDomain`
(`internal/parser/ddl.go:11033-11039`, `if ... == "collate" { p.advance();
_, _ = p.parseIdent(); continue }`) before it ever reaches a
`CreateDomainStmt` field — `CreateDomainStmt` has no `Collation` field at
all, so this fix does not reach `CREATE DOMAIN`; ledgered as its own item.

Diff: 558 → 551 lines (`^+ERROR` unchanged at 22 — this bucket is a missing
`-ERROR`/extra-success divergence, not a `+ERROR`; `^-ERROR` 36 → 35).

## Remaining buckets (ledgered, PARKED)

`collate.sql` has at least eight more independent root causes, none
contained enough to ship in this loop:

1. **`CREATE DOMAIN ... COLLATE` on a non-collatable base type** — same
   `42804` rule as above, but `CreateDomainStmt` needs a new `Collation`
   field threaded through the parser (currently discarded) and
   `execCreateDomain` needs the same `validateColumnCollation` call.
2. **`LIKE <table>` source-table lookup ignores `search_path`** — `CREATE
   TABLE collate_test_like (LIKE collate_test1)` errors "relation
   collate_test1 does not exist" even though `collate_test1` exists in the
   session's `search_path`-active schema (`collate_tests`, set via `SET
   search_path = collate_tests` earlier in the file). Independent of the
   collation-validation bug; needs its own investigation of the `LIKE`
   clause's table-name resolution path in `execCreateTable`.
3. **Explicit-collation mismatch detection is entirely absent** —
   `b COLLATE "C" >= 'bbc' COLLATE "POSIX"` should be `42P21 collation
   mismatch between explicit collations`; goopg evaluates it silently
   (collations are tracked for round-tripping, never enforced at
   expression-evaluation time). This is the root cause behind most of the
   file's remaining diff (`string_agg`/`array_agg` collation propagation,
   `UNION`/`INTERSECT`/`EXCEPT` collation-conflict errors, recursive-CTE
   collation mismatch, subselect hashing-collation errors) — a real
   collation-execution engine, same class as the ICU/libc PARKs
   (M0134-0099/-0100).
4. **`information_schema.views` relation missing** — `SELECT ... FROM
   information_schema.views` errors "relation views does not exist".
5. **`CAST(... AS type COLLATE name)` syntax not parsed** — `SELECT
   CAST('42' AS text COLLATE "C")` is a syntax error in goopg.
6. **`unnest((subquery))` rejects a scalar subquery argument** — "subqueries
   are not supported in this context"; PG allows a scalar-subquery argument
   to a set-returning function.
7. **`pg_get_indexdef` prints a raw Go struct pointer** for an expression
   index carrying an explicit `COLLATE` (`(&{53 0x363c82b8e680 POSIX})`
   instead of `((b || 'foo'::text)) COLLATE "POSIX"`) — the index-expression
   deparser has no case for a `CollateExpr` node inside a parenthesized index
   key. Cosmetically the worst bug in the file (leaks an internal pointer to
   the client) but scoped to `pg_get_indexdef`'s deparser only.
8. **`SELECT collation FOR (...)` syntax not parsed** — `pg_collation_for`
   already exists as a function (see M0134's `foldPgCollationFor` machinery
   in `internal/optimizer/planner.go`), but the `COLLATION FOR (expr)`
   special syntax form (distinct from the function-call form) isn't in the
   grammar.
9. **`CREATE COLLATION` duplicate-option detection is entirely absent** —
   `CREATE COLLATION coll_dup_chk (LC_COLLATE = "POSIX", LC_COLLATE =
   "NONSENSE", ...)` should be `42712 conflicting or redundant options`;
   goopg silently keeps the last-parsed value for each option. Also missing:
   the `LOCALE`-vs-`LC_COLLATE`/`LC_CTYPE` and `FROM`-vs-anything conflict
   rules, `builtin` provider's `LOCALE` requirement/validation, unquoted
   lowercase-only option-name enforcement, and `DROP COLLATION` dependency
   checking (`cannot drop collation ... because other objects depend on
   it`).
10. **EXPLAIN plan shape for collation-bearing sort keys** — goopg's `Sort
    Key` line omits/mis-renders `COLLATE` annotations PG includes
    (`x COLLATE "C", y COLLATE "POSIX"` vs goopg's bare `x, y`).

Root cause #3 (execution-time collation enforcement) is by far the largest
and structurally the same "no real collation subsystem" gap already
documented for M0134-0099/-0100; a future collation-execution milestone
should resolve it, #1, #5, #8, and #9 together.

## References

- `postgres/src/backend/parser/parse_utilcmd.c:4044-4067`
  (`transformColumnType` — the exact rule ported here).
- `internal/executor/operators_ddl.go` — `validateColumnCollation`,
  `columnTypeIsCollatable` (new), called from `execCreateTable`'s `addCol`
  closure.
- `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0101.
