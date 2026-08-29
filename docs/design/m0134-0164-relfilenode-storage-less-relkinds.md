# M0134-0164 — `pg_class.relfilenode` must be 0 for storage-less relkinds

**Status:** landed (2026-08-29)
**Regress case:** `postgres/src/test/regress/sql/sanity_check.sql`
**Task:** M0134-0164 (regress-sql `not-tried` → `failed`, PARKED — see "Remainder")

## The case, and why it is not about `sanity_check`

`sanity_check.sql` is three statements: a `VACUUM` and two catalog-invariant
queries. It carries no schema of its own — in upstream's serial schedule it runs
late, so it audits whatever the preceding tests built. That makes it a pure
*invariant probe* over `pg_class`, and both of its queries are checks a real
cluster is expected to answer with zero rows.

At HEAD the case had never been run (`not-tried`). Run live it produced **77 diff
lines**, split cleanly between the two queries:

| query | goopg at HEAD | root cause |
|---|---|---|
| every system catalog with an `oid` column has a unique immediate index on it | 2 rows (`pg_type`, `pg_class`) | goopg's `pg_index` builder enumerates only user indexes — filed as M0134-0164a |
| relations without storage have no relfilenode | **59 rows** (every view) | this document |

The second bucket is the subject here. It is **engine-wide, not
sanity_check-specific**: the query is a plain `pg_class` predicate, so the same
wrong value was visible to every consumer of the catalog.

## Upstream rule

`heap_create` assigns a relfilenumber only for kinds that own storage
(`postgres/src/backend/catalog/heap.c:335-345`):

```c
/* Don't create storage for relkinds without physical storage. */
if (!RELKIND_HAS_STORAGE(relkind))
    create_storage = false;
else
{
    if (!RelFileNumberIsValid(relfilenumber))
        relfilenumber = relid;
```

and the macro (`postgres/src/include/catalog/pg_class.h:200`) admits exactly five
kinds:

```c
#define RELKIND_HAS_STORAGE(relkind) \
    ((relkind) == RELKIND_RELATION || \
     (relkind) == RELKIND_INDEX || \
     (relkind) == RELKIND_SEQUENCE || \
     (relkind) == RELKIND_TOASTVALUE || \
     (relkind) == RELKIND_MATVIEW)
```

So `v` (view), `c` (composite type), `f` (foreign table), `p` (partitioned
table) and `I` (partitioned index) read back as `relfilenode = 0` — which is
precisely the `relkind IN ('v','c','f','p','I')` set `sanity_check.sql`'s second
query names.

## What goopg did — a sibling-path divergence

goopg has **four** pg_class row builders. They did not agree.

| builder | file | before |
|---|---|---|
| virtual, tables | `internal/catalog/catalog.go` `PGClassRowsForDBOid` | `strconv.Itoa(int(t.OID))` — **unconditional** |
| virtual, indexes | same function | `strconv.Itoa(int(idx.OID))` — **unconditional** |
| heap, tables | `internal/executor/pg18_user_catalog_rows.go` `buildUserPGClassRow` | ad-hoc `if relkind == "p" \|\| relkind == "v" { 0 }` |
| heap, composite | `buildUserPGClassRowForComposite` | hardcoded `0` (correct) |

This is the recurring failure mode recorded as "sibling code paths must stay in
sync": the *heap* builder — the row a real PG 18.3 attached to a goopg cluster
reads — already had (most of) the right answer, and the *virtual* builder — the
row goopg's own SQL sees — had none of it. The two render the same relation, so
goopg's introspection and a streamed catalog disagreed about whether a view has
a data file.

The heap builder's version was also incomplete: its `relkind == "p" || relkind
== "v"` check predates foreign tables and partitioned indexes, and there was no
single place a new relkind would be forced through.

## The fix

A shared rule in the catalog package, mirroring the macro one-for-one, plus a
cell renderer so the row literals keep their single-token column width (the
convention already used for `relOfType` / `idxTablespace` in the same function —
a multi-token expression there would re-align ~50 unrelated comment columns and
churn the gofmt baseline):

```go
// internal/catalog/catalog.go
func RelkindHasStorage(relkind string) bool {
    switch relkind {
    case "r", "i", "S", "t", "m":
        return true
    default:
        return false
    }
}

func RelfilenodeForRelkind(relkind string, oid uint32) string { … }
```

All three divergent builders now route through it. The heap builder's ad-hoc
check is deleted rather than duplicated, so the rule has exactly one definition.

`initdb` already encoded the same convention independently
(`internal/initdb/relcache_init.go:770`, `internal/initdb/initdb.go:6072`: "RelKind='v'
specially: relam=0, relfilenode=0") — the runtime virtual builder was the sole
outlier, which is why the bug survived so long.

### Not changed

`reltablespace` has an analogous upstream rule (`RELKIND_HAS_TABLESPACE`,
`pg_class.h:219`, which additionally excludes sequences) and goopg emits
`t.Tablespace` unconditionally. It is not fixed here because no goopg surface
can set a tablespace on a storage-less relation — there is no `CREATE VIEW …
TABLESPACE` — so the field is already 0 for every kind the rule would zero. Not
ledgered as a defect for that reason; if `ALTER … SET TABLESPACE` ever reaches
those kinds, the rule belongs next to `RelkindHasStorage`.

## Verification

**Regress A/B, 13 cases, working tree vs a HEAD worktree** (`create_view`,
`create_table`, `alter_table`, `rules`, `dependency`, `inherit`, `matview`,
`foreign_data`, `sequence`, `indexing`, `create_index`, `psql`, `sanity_check`),
diff files compared byte-for-byte post-header:

- **10 byte-identical** — zero regressions.
- `sanity_check` **129 → 21** lines. (The baseline reads 129 rather than the
  77 measured standalone because this run loads all 13 cases' objects first, so
  more views exist to be listed. Both sides of the A/B saw the identical load.)
  The second query is now byte-identical to `expected/sanity_check.out`.
- `alter_table` **3800 → 3798**, an independent confirmation. That case builds
  `at_partitioned` and snapshots `relfilenode` into a temp table, then reports
  each relation's storage as `own`/`none`. The partitioned table's row now reads
  `none` and matches expected exactly; the parent index (`relkind = 'I'`) also
  moved `own` → `none`, still differing only on unrelated naming/`obj_description`
  columns.
- `create_index` — same line count (3340) but not byte-identical: the differing
  bytes are a **Go pointer address** printed into `pg_get_indexdef` output
  (`WHERE (c1::text > &{105 0xcf77f334c00 C})`). It varies between any two runs
  and is unrelated to this change. Filed as its own ledger row.

**Unit regression:** `internal/executor/pg_class_relfilenode_storage_test.go` —
`TestPgClassRelfilenodeZeroForStorageLessRelkinds` drives real DDL (table, index,
view, partitioned table, partition child, partitioned index) and asserts the
property over the whole virtual `pg_class` enumeration, with a non-vacuity guard
so a truncated enumeration cannot pass silently; `TestRelkindHasStorageMatchesUpstreamMacro`
pins the rule to the macro over the full relkind alphabet. Confirmed to fail on a
reverted builder (`rf_part`/`rf_view` flagged) rather than passing vacuously.

## Remainder — why the case is PARKED, not closed

The 21 residual diff lines are entirely the case's **first** query, a single
independent root cause filed as **M0134-0164a**: goopg's `pg_index` virtual
builder (`PGIndexRowsForDBOid`) enumerates `c.AllIndexes(dbOid)` — user indexes
only — so no bootstrap catalog index is described in `pg_index` at all. goopg
does maintain the physical files (`pg_class_oid_index` 2662, `pg_type_oid_index`
2703, see `internal/executor/sys_catalog_index_insert.go`), and the virtual
pg_class row for `pg_class` already advertises `relhasindex = 't'`, so the
catalog is internally inconsistent today. Surfacing those rows is not a
one-liner: it decides what a real PG standby reading goopg's catalog will try to
descend, which is the territory of the PG-standby catalog-consumption work and
its own blockers. It is a separate slice, not a smaller version of this one.
