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

Each landing is checked with a column-wise diff of the view against a
scratch PG 18.3 cluster. The generators capture from a throwaway
`initdb -U postgres`.

regproc-typed columns hold numeric OIDs. `regOutShared` renders them,
following regprocout: an overloaded built-in name is shown as
`pg_catalog.name` (h1a19).

## Still empty or partial (measured 2026-11-17, goopg vs PG)

| catalog | goopg rows | PG rows |
|---|---|---|
| pg\_amop | 0 | 945 |
| pg\_amproc | 0 | 714 |
| pg\_opclass | 0 | 177 |
| pg\_opfamily | 0 | 146 |
| pg\_cast | 0 | 235 |
| pg\_range | 0 | 6 |
| pg\_conversion | 0 | 128 |
| pg\_ts\_config | 0 | 30 |
| pg\_ts\_dict | 1 | 30 |
| pg\_collation | 7 | 860 |
| pg\_index | 0 | 329 |
| pg\_constraint | 0 | 194 |
| pg\_rewrite | 0 | 147 |
| pg\_class | 11 | 415 |
| pg\_attribute | 3161 | 5085 |
| pg\_depend | 1725 | 2800 |

`format_type` and `regtypeout` render many built-in OIDs (pseudo-types,
range types) as `???` or a bare number. They need the full pg\_type seed,
which lives in the initdb package and is not reachable from the executor.
