Task: M0110-0003 (pg_amcheck) — amcheck SQL surface. Engine-first/wire-later.
**STILL HARD-BLOCKED on a clean tree (since loop #62). Loop #64 landed the wiring
PLAN as a design doc — the only safe new-file work left.**

WHAT LANDED loop #64 (documentation only, no code/engine change):
  docs/design/0110-0008-amcheck-sql-surface-plan.md (+ README index row).
  Execute-ready plan so the unblocking loop stops re-deriving scope (#62/#63 each
  re-read 002_nonesuch.pl from scratch). Captures:
  - The 3 exact queries pg_amcheck.c issues (verbatim, with line refs):
    1. install probe `amcheck_sql` (L173): SELECT n.nspname, x.extversion FROM
       pg_catalog.pg_extension x JOIN pg_catalog.pg_namespace n ON
       x.extnamespace=n.oid WHERE x.extname='amcheck'.
    2. verify_heapam SRF (L843): SETOF (blkno i8, offnum i8, attnum i4, msg text);
       args relation/on_error_stop/check_toast/skip/startblock/endblock.
    3. bt_index_check / bt_index_parent_check (L887): RETURNS void, corruption
       raised as errors.
  - CATALOG GAP: pg_namespace EXISTS (catalog.go:1895); pg_extension MISSING — the
    one new catalog relation needed.
  - SCOPE REFINEMENT loop #63 missed: 002_nonesuch is ~all client-side pattern
    resolution + the install probe; it does NOT run real verify_heapam corruption
    (that's 004/005). So Slices S1+S2 ALONE promote AC-002 — SRFs need only EXIST
    in pg_proc to type-check, not execute.
  - 5 committable slices: S1 CREATE EXTENSION DDL → S2 pg_extension+probe(AC-002)
    → S3 verify_heapam SRF → S4 bt_index_check SRFs → S5 port 002–005 TAP tests.

WHY STILL BLOCKED (unchanged from #63): the surface edits parser/ast.go+ddl.go,
server/dispatch.go, executor/operators_ddl.go, planner/plan.go+planner.go,
catalog/catalog.go — all carry a separate manual session's uncommitted M0100-0010
gen-column WIP (`WITH OPTIONS GENERATED ALWAYS AS`), static since 2026-06-13 14:28.
Confirmed foreign this loop by reading the diffs. Do NOT git add -A / commit it.
`go build ./...` PASSES with the WIP present (block is the do-not-commit rule only).

Committed engine (healthy, all new files): bloomfilter.go, heapallindexed.go,
verify_heapam.go, verify_nbtree.go (+tests). Last engine commit 62e67c03.

HUMAN ACTION REQUIRED to unblock: stash/commit the foreign gen-column WIP.

Next step (once tree clean): execute Slice S1 in 0110-0008 — parse/dispatch/execute
`CREATE EXTENSION amcheck` + seed a pg_extension row; then S2 promotes AC-002.

Other OPEN tasks (also blocked on big features): M0095-0003 (pg_basebackup -X
stream), M0110-0001 (pg_dump → catalog parity), M0110-0002 (pg_waldump FPI).

Gates run loop #64: go build ./... PASS; go test ./internal/amcheck PASS;
make ralph-state-guard (run before status block).
