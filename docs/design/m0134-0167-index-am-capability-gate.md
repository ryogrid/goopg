# M0134-0167 — `DefineIndex`'s index-AM capability gate

Status: accepted (2026-08-29)
Milestone task: M0134-0167 (`spgist.sql`)
Code: `internal/executor/amutils.go` (`checkIndexAMCapabilities`),
`internal/executor/operators_ddl.go` (`execCreateIndex`)
Guard: `internal/executor/index_am_capability_test.go`

## Why this exists

`spgist.sql` was sized live for the first time this loop (`not-tried` →
`failed`). It is a **23-line diff with exactly one hunk**, and that hunk has
exactly one cause: goopg has **no SP-GiST access method at all**. `USING
spgist` registers catalog metadata only (`execCreateIndex`'s catalog-only
branch, shared with gist/gin/brin), so the case's

```sql
explain (costs off) select * from spgist_domain_tbl where f1 = 'fo';
```

plans a `Seq Scan` where PG plans a `Bitmap Index Scan on spgist_domain_idx`.
Implementing SP-GiST (a radix/quad trie AM with its own page layout, WAL
records and five opclass support functions) is REFACTOR-tier and belongs to
its own milestone, so **the case is PARKED** and its CSV row stays `failed`.

Everything else in the file already matched byte-for-byte — including the
out-of-range `fillfactor` rejections, which only started matching after
M0134-0160's reloption-name registry.

What the case *did* expose, on the "index over a domain" / `create index …
using spgist(…)` theme it is built around, is an **engine-wide
silent-acceptance gap** with the same shape as M0134-0161's `indimmediate`
finding: goopg had a hand-curated `pg_am` capability table with **one
consumer**, and the DDL path that most needs it never looked at it.

## The gap

`postgres/src/backend/commands/indexcmds.c:838-892` is a block upstream
labels *"look up the access method, verify it can handle the requested
features"*. It runs five checks against the resolved `IndexAmRoutine`:

| upstream line | flag | error (all `ERRCODE_FEATURE_NOT_SUPPORTED` / 0A000) |
|---|---|---|
| 869 | `amcanunique` | `access method "%s" does not support unique indexes` |
| 874 | `amcaninclude` | `access method "%s" does not support included columns` |
| 879 | `amcanmulticol` | `access method "%s" does not support multicolumn indexes` |
| 2228 | `amcanorder` | `access method "%s" does not support ASC/DESC options` |
| 2233 | `amcanorder` | `access method "%s" does not support NULLS FIRST/LAST options` |

goopg's `execCreateIndex` enforced **one** of them, and not by reading a flag
— by a hardcoded AM-name list:

```go
if len(s.IncludeColumns) > 0 {
        switch method {
        case "brin", "gin", "hash":
                return &ExecError{Code: "0A000", …}
        }
}
```

So goopg silently accepted, against a live PG 18.3 oracle:

```sql
create unique index … using spgist(p);   -- PG: does not support unique indexes
create unique index … using gist(p);     -- PG: does not support unique indexes
create unique index … using gin(b);      -- PG: does not support unique indexes
create unique index … using brin(a);     -- PG: does not support unique indexes
create unique index … using hash(a);     -- PG: does not support unique indexes
create index … using spgist(p, box1);    -- PG: does not support multicolumn indexes
create index … using hash(a, b);         -- PG: does not support multicolumn indexes
create index … using spgist(p desc);     -- PG: does not support ASC/DESC options
create index … using spgist(p nulls first);  -- PG: NULLS FIRST/LAST options
```

**This is not cosmetic.** gist/spgist/gin/brin are catalog-only in goopg, so a
`CREATE UNIQUE INDEX … USING spgist` produced a relation that *advertises*
uniqueness in `pg_index.indisunique` and enforces nothing — a constraint the
user believes exists and the engine never checks. The multicolumn and
ordering cases are the milder "accept and ignore" variety.

## The capability table already existed

M0134-0090 added `catalog.IndexAMCapability` / `indexAMCapabilities`
(`internal/catalog/catalog.go`) — a hand-curated mirror of the six in-tree
AMs' `IndexAmRoutine` literals, transcribed 1:1 from
`postgres/src/backend/access/{nbtree,hash,gist,gin,spgist,brin}/*.c`, with
`CanUnique` / `CanMultiCol` / `CanInclude` / `CanOrder` fields already
present and correct. Its doc comment says it is used *"exclusively by
pg_indexam_has_property / pg_index_has_property /
pg_index_column_has_property"*, and that was literally true: the compat
surface could **report** that spgist cannot do unique indexes while the DDL
path happily created one.

The fix gives the table its second consumer. No new capability data was
invented; nothing in `catalog.go` changed.

## What landed

`checkIndexAMCapabilities(method string, s *parser.CreateIndexStmt)` in
`internal/executor/amutils.go` performs the five checks in upstream's order
(unique → include → multicol → per-column ASC/DESC → per-column NULLS
FIRST/LAST). The AM name passed in is the **declared** one, so `hash` answers
as hash even though goopg builds a hash index on the B-tree substrate
(`catalog.Index.DeclaredHash`); the call therefore sits *before*
`execCreateIndex`'s `hash` → `btree` rewrite. An AM outside the six in-tree
index AMs has no capability row and is passed through untouched — diagnosing
a bogus access method is the existing existence check's job, not this one's.

### Check ordering moved, deliberately

`execCreateIndex` previously resolved `method` late, after every reloption
range check. Upstream resolves the AM first (including the `rtree` → `gist`
NOTICE substitution, `indexcmds.c:848`), runs the capability checks, and only
then calls `index_reloptions()` (`:911-914`); `index_create`'s name-conflict
test is later still. The method-resolution block plus the new gate therefore
moved up to immediately after the relation lookup, ahead of
`validateRelOptionNames`. Verified live against the oracle:
`create unique index … using spgist(p) with (bogus_opt=1)` now reports the
*unique* error, as PG does, not `unrecognized parameter`.

## Verification

A 15-statement oracle A/B (goopg:5533 vs PG 18.3:5534, same script) covering
all five checks, both directions, plus the still-legal
`create unique index … using btree(a, b)` and
`create index … using gist(p, box1)`: **byte-identical**. A second
7-statement script covering check ordering and the `rtree` NOTICE:
byte-identical except the two residuals below.

15-case regress A/B against a HEAD worktree (`spgist gist gin brin hash_index
create_index index_including indexing amutils alter_table create_table
create_table_like reloptions replica_identity constraints`): every case
unchanged, **zero regressions**. `create_index.diff` differs between the two
runs only in a Go pointer address that a pre-existing `pg_get_indexdef` bug
prints verbatim (ledgered separately) — nondeterministic run to run, not
caused by this change.

No upstream regress case exercises any of the five errors (verified: the
strings appear nowhere under `postgres/src/test/` or `postgres/contrib/`), so
`internal/executor/index_am_capability_test.go` is their only coverage. It is
revert-checked: 12 of its subtests fail when the gate is restored to the old
hardcoded INCLUDE list.

## Deferred

Two orderings remain unreachable because `parser.IndexColOrder` is lossier
than upstream's `SortByDir` / `SortByNulls` tri-states:

- **explicit `ASC`** — `opt_index_dir` (`grammar/goopg_ext.y`) yields a plain
  `bool`, so `ASC` and "no direction" both arrive as `Descending: false`. PG
  errors on `USING spgist(p ASC)` because `attribute->ordering != SORTBY_DEFAULT`
  is true for `SORTBY_ASC` too.
- **`NULLS LAST` on an ascending key** — `newIndexElem`
  (`internal/parser/support.go:1488`) receives the explicitness as
  `nullsFirst *bool` but collapses it into `IndexColOrder.NullsFirst`,
  defaulted from `Descending`. `NULLS LAST` on an ascending key therefore
  becomes `false == Descending` and is indistinguishable from the default.

Both are ledgered with their resume points. Closing them needs an
`opt_index_dir` tri-state (a grammar edit, so
`docs/design/not_ralph/06-goyacc-parser-playbook.md` §12 applies) plus
explicitness fields on `IndexColOrder`.

Also deferred: the same gate on the other index-creating paths (table
constraints, `ALTER TABLE ADD CONSTRAINT … USING INDEX`), and upstream's two
remaining `DefineIndex` checks — `amgettuple == NULL` for exclusion
constraints (`indexcmds.c:883`) and the `gist`-only `WITHOUT OVERLAPS`
restriction (`:888`).
