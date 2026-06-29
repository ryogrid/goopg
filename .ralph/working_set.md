(idle — nothing in flight)

Last loop (#33): M0119-0004 **per-column operator class round-trip in pg_dump**
(DU-002 slice 312) — LANDED. Design `0119-0004-index-column-opclass-roundtrip.md`.

A B-tree index column may declare a non-default opclass (`CREATE INDEX … (a
text_pattern_ops)`); PG carries it via pg_index.indclass and re-emits it in
pg_get_indexdef_worker (get_opclass_name) after the column/COLLATE, before
ASC/DESC, suppressing the type default. goopg's parseIndexColumnList CONSUMED the
bare opclass ident but DISCARDED it; catalog.Index had no per-column opclass field
and BuildIndexDef emitted none → `(a text_pattern_ops)` dumped as `(a)` (semantic
widen to default opclass on restore).

Threaded (parallel to ColDescending/ColNullsFirst):
- ast.go: new IndexColOrder.OpClass (parser captures the discarded name).
- ddl.go parseIndexColumnList: assign colOpClass → order.OpClass.
- catalog.go: Index.ColOpClasses []string; BuildIndexDef emits ` <opclass>` after
  col, before DESC/NULLS, gated on non-empty entry.
- operators_ddl.go: new indexHasOpClass guard ORs into the store-metadata gate;
  copies ColOrders[i].OpClass → idx.ColOpClasses.

Records only EXPLICIT opclass (non-empty ⇒ non-default); explicit-default edge
(user writes `text_ops`, PG drops) accepted, same as partition-key opclass slice
300. Dump-fidelity only (goopg always builds default-comparison btree).

Gates: DU-002 slice 312 in TestPort_PgDumpConnectionSetup PASS vs real pg_dump
18.3 (5.1s); new units TestBuildIndexDefColOpClass + TestParseCreateIndexColOpClass
PASS; parser+catalog+executor suites PASS; `go build ./...` clean; pgbench smoke =
pre-commit hook.

NEXT loop — next pg_dump getter-battery gap. Strong sibling candidate: per-column
COLLATE on an index column/expression (`CREATE INDEX … (a COLLATE "C")`) —
BuildIndexDef emits NO COLLATE clause yet (parser already consumes+discards COLLATE
the same way it did the opclass). Other M0119: M0119-0002 (CLOG store swap Part B
full-gate) / M0119-0005 (pg_waldump) / M0119-0006 (pg_amcheck). Extended-protocol
commit-time deferral is architecturally entangled (auto-commit-per-statement).
