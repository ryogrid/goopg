(idle — nothing in flight)

Last landed: DU-002 slice 126 (loop #90) — a multi-column UNIQUE constraint whose
key order DIFFERS from the table's column order now dumps byte-identically.
`CREATE TABLE public.uniqm (a integer, b integer, c text, UNIQUE (b, a))` →
`ADD CONSTRAINT uniqm_b_a_key UNIQUE (b, a)`. **No production change** — a
regression guard: goopg stores index key columns in declared order
(catalog.Index.Columns), and both the deparse (buildConstraintDefString,
internal/executor/expr.go) and the auto-name generator
(internal/executor/operators_ddl.go:1294, `<table>_<col1>_<col2>_key`) consume
that slice, so both the `(b, a)` order and the `uniqm_b_a_key` name fall out
correctly. The constraint-backed UNIQUE/PK path was previously covered only by a
single-column UNIQUE (foo_code_key) and a declaration-order multi-column PK
(bar_pkey (a,b)) — neither tested the multi-column `_key` name join NOR a
non-table-order key list. Verified byte-identical vs real pg_dump 18.3 (reference
/tmp/du126_pgdata).

Files: internal/testport/pgdump_connsetup_test.go (uniqm fixture + positive assert
+ 2 negative guards rejecting uniqm_a_b_key / `UNIQUE (a, b)`),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 126), .ralph/fix_plan.md.
Committed + pushed.

Next direction (slice 127): a table+VIEW dependency-ordering case (verify
topological emission ORDER, not just presence), OR a multi-column CHECK constraint
(`CHECK (a < b)` referencing two columns), OR a UNIQUE constraint with an INCLUDE
column.
