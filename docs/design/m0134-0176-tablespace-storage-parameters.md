# M0134-0176 — tablespace storage parameters: validate, persist, ALTER

Status: implemented (2026-08-29)
Task: M0134-0176 (`tablespace.sql`)
Upstream reference: `postgres/src/backend/commands/tablespace.c`,
`postgres/src/backend/access/common/reloptions.c`

## The gap

`pg_tablespace.spcoptions` was hardcoded NULL in all three places that build a
pg_tablespace row, and the `WITH (...)` clause of `CREATE TABLESPACE` was parsed
into a raw token dump — `CreateTablespaceStmt.Options []string`, every token
including `=` and the values — that **no consumer ever read**. So:

```sql
CREATE TABLESPACE t LOCATION '' WITH (some_nonexistent_parameter = true);
-- PG:    ERROR:  unrecognized parameter "some_nonexistent_parameter"
-- goopg: CREATE TABLESPACE
CREATE TABLESPACE t2 LOCATION '' WITH (random_page_cost = 3.0);
SELECT spcoptions FROM pg_tablespace WHERE spcname = 't2';
-- PG:    {random_page_cost=3.0}
-- goopg: NULL
```

This is the *declared but unconsumed* pattern again (the fourth instance after
`client_min_messages`, `fillfactor` and the relation reloptions of M0134-0160):
the clause was lexed, bounded by a grammar, carried on the AST and dumped — and
did nothing. As with M0134-0160 the silent acceptance also **cascades**: the
tablespace the failing statement created turns the next (valid) `CREATE
TABLESPACE` of the same name into a spurious "already exists".

Separately, **`ALTER TABLESPACE` did not parse at all**. All four forms fell
through every arm of the hand-written `parseAlter` to its closing
`expectKeyword(KwTable)` and surfaced as

```
ERROR:  syntax error at or near "expected keyword table (got tablespace)"
```

## Upstream model

PG funnels the whole tablespace-option lifecycle through the *same* pair of
functions every other relation kind uses:

- `transformRelOptions` (`reloptions.c:1160`) merges the caller's `DefElem` list
  into the existing array. Order is load-bearing: surviving old elements first
  in their original order, minus every name mentioned in the new list, then the
  new elements appended in source order. Replacing an option therefore **moves
  it to the end**.
- `tablespace_reloptions` (`reloptions.c:2091`) then validates the **merged**
  array against `RELOPT_KIND_TABLESPACE`, whose admissible set is exactly four
  names: `random_page_cost`, `seq_page_cost`, `effective_io_concurrency`,
  `maintenance_io_concurrency`.

`CreateTableSpace` (`tablespace.c:359`) and `AlterTableSpaceOptions`
(`tablespace.c:1015`) both go through that pair, which is why goopg has one
helper serving both.

Three consequences of validating the *merged array* rather than the input list
are easy to get wrong, and all three were verified against a live PG 18.3:

1. **`RESET (bogus_never_set)` SUCCEEDS.** RESET only ever removes elements, so
   an unknown name never enters the array and can never be rejected. A name is
   only checked on the way *in*.
2. **`RESET (name = value)` is a SYNTAX error** (42601, "RESET must not include
   values for parameters"), not a parameter error — upstream notes the grammar
   cannot enforce it and rejects it in `transformRelOptions`
   (`reloptions.c:1228-1243`). Expressing this needs the clause as a *list* with
   a "had a value" bit, which is why `TablespaceOption` is not a map.
3. **Emptying the array returns spcoptions to SQL NULL, not `{}`** — spcoptions
   is `BKI_DEFAULT(_null_)` and `AlterTableSpaceOptions` writes `repl_null` when
   the merged array comes back empty (`tablespace.c:1063-1066`).

## What landed

| layer | change |
|---|---|
| `internal/executor/reloptions_catalog.go` | `relOptTablespace` kind + the four upstream names; `validateRelOptionNamesInOrder` (the sortless variant, for callers that hold the clause in source order) |
| `internal/parser/ast.go` | `TablespaceOption{Name,Value,HasValue}`; `CreateTablespaceStmt.Options` retyped; new `AlterTablespaceStmt` |
| `internal/parser/ddl.go` | `parseTablespaceOptionList` (shared by WITH and SET/RESET); `parseAlterTablespaceTail`; dispatch of `ALTER TABLESPACE` at the head of `parseAlter` |
| `internal/catalog/catalog.go` | `tablespaceRow.options`; `TablespaceOptions`/`SetTablespaceOptions`/`RenameTablespace`/`SetTablespaceOwner`; `pgTextArrayLiteral` rendering in `tablespaceVirtualRows` |
| `internal/executor/tablespace_options.go` | `tablespaceOptionArray` (the merge+validate pair) and `execAlterTablespace` |
| `internal/optimizer/planner.go`, `internal/postmaster/dispatch.go` | route the new statement / command tag |

`AlterTablespaceStmt` folds upstream's three node types
(`AlterTableSpaceOptionsStmt` at `gram.y:9358` plus the tablespace arms of
`RenameStmt` and `AlterOwnerStmt`) into one node discriminated by `Action`,
because all three land in the same tiny executor path.

**No grammar change was needed.** Tablespace DDL is one of the statement classes
the goyacc port deliberately does not carry (playbook §12): `CREATE TABLESPACE`
already lived in `parseCreateTablespaceTail`, and `routedCreatePairs["alter"]`
has no `"tablespace"` entry, so `ALTER TABLESPACE` never reaches the grammar.
The new code sits beside its `CREATE` sibling rather than splitting the feature
across two parsers.

## Effect on `tablespace.sql`

854 → 811 diff lines; `^+ERROR` 32 → 25, `^-ERROR` 27 → 24. Every
`CREATE TABLESPACE ... WITH` and `ALTER TABLESPACE SET/RESET/RENAME/OWNER` line
in the case now matches the oracle byte-for-byte.

The successful `RENAME` **unmasks** a downstream failure that was previously
hidden behind it: `DROP TABLESPACE regress_tblspace_renamed` now reports
"is not empty", because `ALTER TABLE ALL IN TABLESPACE ... SET TABLESPACE
pg_default` does not parse and so never moves the relations off it. That is an
honest new cascade, not a regression — before this change the rename itself
failed and the renamed tablespace never existed.

## Deliberately not done

Recorded in `.ralph/deferral_ledger.md` (2026-08-29, M0134-0176):

- **No value typing.** `ALTER TABLESPACE t SET (seq_page_cost)` stores
  `seq_page_cost=true` where PG raises `invalid value for floating point option
  "seq_page_cost": true` (oracle-verified). goopg validates option *names* only;
  this is the same gap M0134-0160 recorded for relation reloptions.
- **No owner permission checks.** Each upstream entry point calls
  `object_ownercheck(TableSpaceRelationId, ...)`; goopg has no tablespace-owner
  ACL to check against, so `OWNER TO` only records a name.
- **spcoptions is registry-only.** The pg_tablespace *heap* row
  (`buildPGTablespaceRow`) still writes `NullDatum`, so options do not survive a
  restart — the on-disk shared-catalog in-place-update gap, not a tablespace one.
- **`ALTER {TABLE|INDEX|MATERIALIZED VIEW} ALL IN TABLESPACE`** is unparsed, and
  `pg_tablespace_location()` is catalogued but has no handler. Both are filed as
  M0134-0176a/b.

## Sibling check

`internal/initdb/pg_tablespace_bootstrap.go` and
`internal/executor/sys_pg_tablespace.go` build the same five columns as the
virtual view and were deliberately left writing NULL spcoptions: they seed and
journal the *heap*, which no live query reads for this column (`pg_tablespace`
is served by the virtual builder). Changing them without the restart-reload half
would produce two catalogs that disagree.
