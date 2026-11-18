# M0149-0001h1a19 — built-in rows in the SQL-visible catalogs

Several goopg `pg_catalog` relations are virtual views over the in-memory
catalog. Many were written to serve only user-created objects, so their
built-in rows were invisible to SQL:

- regress cases that cross-check the catalogs (opr\_sanity, type\_sanity,
  oidjoins);
- psql's `\df` / `\do`;
- client tools that look up built-ins by name.

This is distinct from the on-disk bootstrap image that a PG node reads
(M0149-0001g, owner-held).

## Pattern

Serve every PG 18.3 initdb row, then the user rows, with the values taken
from PG itself:

| catalog | source of the built-in rows | landed |
|---|---|---|
| pg\_type | `scripts/gen-pg-type-support-procs.py` (support / collation / default columns) | h1a15 |
| pg\_proc | `scripts/gen-pg-proc-view-rows.py` → `pg_proc_view_rows_generated.go` (3413 rows) | h1a18 |
| pg\_operator | the existing `PGOperatorAllEntries` seed (799 rows), `seedOperatorViewRows` | h1a19 |
| pg\_cast, pg\_opfamily, pg\_opclass, pg\_amop, pg\_amproc | `scripts/gen-pg-catalog-seed-rows.py` → `internal/catalog/pg_catalog_seed_rows_generated.go` (235 / 146 / 177 / 945 / 714 rows) | h1a20 |
| pg\_conversion, pg\_range | same generator (128 / 6 rows; pg\_range keyed by rngtypid) | h1a22 |

Each landing is checked with a column-wise diff of the view against a
scratch PG 18.3 cluster. The generators capture from a throwaway
`initdb -U postgres`.

regproc-typed columns hold numeric OIDs. `regOutShared` renders them,
following regprocout: an overloaded built-in name is shown as
`pg_catalog.name` (h1a19).

## Still empty or partial (measured 2026-11-17, goopg vs PG; h1a20 removed pg\_cast and the four opclass catalogs)

| catalog | goopg rows | PG rows |
|---|---|---|
| pg\_ts\_config | 0 | 30 |
| pg\_ts\_dict | 1 | 30 |
| pg\_collation | 7 | 860 |
| pg\_index | 0 | 329 |
| pg\_constraint | 0 | 194 |
| pg\_rewrite | 0 | 147 |
| pg\_class | 11 | 415 |
| pg\_attribute | 3161 | 5085 |
| pg\_depend | 1725 | 2800 |

`format_type` and `regtypeout` used to render many built-in OIDs
\(pseudo-types, range types, jsonb\) as `???` or a bare number. Since h1a21,
the same generator also captures `format_type\(oid, NULL\)` for the 473
pg\_catalog types into `builtinFormatTypeNames`:

- `catalog.BuiltinFormatTypeName` is the first lookup in
  `oidToBuiltinTypeName`, so every `::regtype` display path goes through it;
- it is also `formatTypeOID`'s fallback for an untypmodded unknown OID.

Both functions now match the oracle for every pg\_catalog type.

The generic generator (`gen-pg-catalog-seed-rows.py`) takes a table name
and its view column order, so another catalog is one more `TABLES` entry.
Test helpers that inspect user rows skip the seed through
`catalog.SeedPg*RowCount()`.

Seeding these views made catalog queries that used to return nothing run
for real. That surfaced two executor bugs, both fixed in h1a22:

- `oidvector` / `int2vector` subscripts indexed the text form's characters
  (`proargtypes[0]` of `23 23 2275` was `2`). The planner now stamps the
  element type (oid / int2), and the executor splits the value 0-based.
- regtypein did not know most built-in names (`'cstring'::regtype` raised
  42704). It now falls back to the reverse of `builtinFormatTypeNames`.

## Function signature texts (h1a23)

The same generator captures what PG 18.3 prints for every built-in function:

- `pg_get_function_result` (NULL for a procedure);
- `pg_get_function_arguments`;
- `pg_get_function_identity_arguments`.

They are stored in `builtinFunctionSignatures`. The three executor arms use
them after the user-routine lookup misses, so psql's `\df` shows a
built-in's result and argument types. A column-wise diff against the oracle
is empty for all 3413 built-ins.

## Function definitions and internal symbols (h1a24)

- **Function definitions.** `builtinFunctionDefs` holds
  `pg_get_functiondef` for every non-aggregate built-in. The executor uses it
  when the OID is not a user routine, which backs psql's `\sf`. A
  column-wise diff against the oracle is empty.
- **Internal symbols.** `builtinInternalFunctionSymbols` is the prosrc of
  every LANGUAGE internal built-in. `CREATE FUNCTION ... LANGUAGE internal
  AS '<sym>'` validates against it, as `fmgr_internal_function` does, so
  `dsin` (for sin) is accepted and the SQL name `sin` is rejected. Earlier it
  validated the proname instead.

## pg\_class.reltype (h1a34)

- **System relations (slice 1).** `initdb` registers PG 18.3's relation →
  rowtype map (`pgClassRowtypeOIDs`, from the captured initdb pg\_type rows)
  through `catalog.SetBuiltinRelTypes` at package init. The virtual
  pg\_class renders `reltype` from `catalog.BuiltinRelType`: pg\_class is 83,
  pg\_type 71, pg\_largeobject 10025, and an index or sequence is 0. It used to
  be 0 everywhere.
- **User relations (slice 2).** `CreateTable` and `CreateView` reserve
  OIDs as `heap_create_with_catalog` does: relation N, array type N\+1
  (`AssignTypeArrayOid`), rowtype N\+2 (`AddNewRelationType`), stored as
  `Table.RowTypeOID`. A sequence uses `CreateSequenceRelation` and takes one
  OID (PG 18 sequences have no rowtype).
  - `syncTableToCatalogHeap` writes the rowtype pg\_type row (typtype 'c',
    typrelid, record I/O, the relation's namespace and owner) and its `_name`
    array row before the pg\_class row whose reltype points at them, so a PG
    node's typcache finds them. `deleteCatalogRowsForOID` stamps both rows dead
    (typrelid / typelem match), so DROP and the ALTER re\-sync paths keep one
    copy.
  - The reload reads reltype from the heap pg\_class row back into
    `RowTypeOID` and advances the OID counter past it.
  - `'tbl'::regtype` / `'tbl[]'::regtype` resolve through search\_path to the
    rowtype and its array (`relationRowtypeForName`); regtypeout and
    format\_type render them (`LookupTableByRowTypeOID`).
  - Not yet: ALTER TABLE RENAME / SET SCHEMA / OWNER do not update the rowtype
    rows, TOAST relations and view rules take no OIDs from the counter (so
    later user OIDs still drift from PG's), and pg\_typeof of a whole\-row
    reference is text (filed M0149-0001h1a36).
- **Composite relations and visibility.** `pg_table_is_visible` answers for a
  standalone composite type's relation (relkind 'c') by its schema; the h1a32
  port returned NULL for it, which hid every composite type from psql `\d`
  \(regress alter\_table 717→775\); fixed in h1a34 \(now 697\).
- **Known gap.** goopg's virtual pg\_class lists only 11 system relations,
  where PG lists 415 (filed M0149-0001h1a35).

## System relations in pg\_class (h1a35)

`scripts/gen-pg-catalog-seed-rows.py` captures every pg\_class row a PG 18.3
initdb holds below OID 16384 (415: catalogs, their indexes and TOAST tables,
system views) as `seedPgClassRows`, in `PGClassRowsForDBOid`'s 34\-column order
\(booleans `t` / `f`; NULL relacl / reloptions / relpartbound as `""`\). The
builder appends every captured row it did not produce itself, and for the few
system relations goopg models live it replaces the bootstrap facsimile with the
captured row \(relhasindex, relisshared, an index's namespace and relnatts were
wrong\), keeping a non\-empty live relacl so a GRANT on a system catalog still
shows. A 17\-column diff of `pg_class where oid < 16384` against the oracle is
empty.

Listing a relation no longer implies goopg can open it. pg\_amcheck walks
pg\_class, so `verify_heapam` and `bt_index_check` now receive OIDs with no goopg
heap or index behind them; `catalog.IsCapturedSystemRelation` identifies those
and both functions report no findings, where PG reads the real relation.

Not covered: relpages / reltuples / relfrozenxid of the captured rows are the
initdb's, not goopg's own, and pg\_attribute / pg\_index still lack most system
relations' rows \(psql `\d pg_namespace` lists no columns\).

### Column label of a cast to `"char"`

The grammar folds quoted `"char"` and the keyword `CHAR` into the type name
`char`; only the keyword gets a synthesised length\-1 typmod. Both column
namers use that difference: `parser.figureColnameInternal` \(no typmods\) and
the planner's `targetMeta` \(`Typmod == 0`\) answer `char`, and the keyword stays
`bpchar`, as in PG's FigureColname. The typmod synthesis misreads
schema\-qualified names, so `pg_catalog."char"` is still character\(1\) \(filed
M0149\-0001h1a37\).
