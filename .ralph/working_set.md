(idle — nothing in flight)

Last loop (#34): M0119-0004 **per-column COLLATE round-trip in pg_dump**
(DU-002 slice 313) — LANDED. Design `0119-0004-index-column-collation-roundtrip.md`.

Sibling of slice 312 (opclass). A B-tree index column may declare a non-default
collation (`CREATE INDEX … (a COLLATE "C")`); PG carries it via
pg_index.indcollation and re-emits it in pg_get_indexdef_worker
(generate_collation_name) AFTER the column and BEFORE the opclass, quoting the
collname (`C`→`"C"`), suppressing the type default. goopg's parseIndexColumnList
CONSUMED the COLLATE clause but DISCARDED the name; catalog.Index had no
per-column collation field and BuildIndexDef emitted none → `(a COLLATE "C")`
dumped as `(a)` (semantic widen to default collation on restore).

Threaded (parallel to OpClass slice 312):
- ast.go: new IndexColOrder.Collation.
- ddl.go parseIndexColumnList: parseCollationName() (was `_ = p.advance()`) →
  order.Collation. Captured before opclass (grammar order).
- catalog.go: Index.ColCollations []string; BuildIndexDef emits ` COLLATE <q>`
  after col, before opclass, via new quoteCollationIdent (quote_identifier mirror:
  `C`/`POSIX` quoted, `ucs_basic` bare).
- operators_ddl.go: new indexHasCollation guard ORs into store-metadata gate;
  copies ColOrders[i].Collation → idx.ColCollations.

Records only EXPLICIT collation (non-empty ⇒ non-default); explicit-default edge
accepted (same as opclass 312 / column COLLATE 188). Dump-fidelity only.

Gates: DU-002 slice 313 in TestPort_PgDumpConnectionSetup PASS vs real pg_dump
18.3 (4.7s); new units TestBuildIndexDefColCollation + TestParseCreateIndexColCollation
PASS; parser+catalog+executor suites PASS; `go build ./...` clean; pgbench smoke =
pre-commit hook.

NEXT loop — next pg_dump getter-battery gap. Candidate: per-column COLLATE on an
index EXPRESSION (this slice covered plain key columns; an expression column's
collation rides the same ColCollations slot but the parser COLLATE capture is on
the bare-column branch — verify the expression branch). Other M0119: M0119-0002
(CLOG store swap Part B full-gate) / M0119-0005 (pg_waldump) / M0119-0006
(pg_amcheck). Extended-protocol commit-time deferral is architecturally entangled.
