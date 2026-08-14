(83rd slice landed and committed — M0119-0006 continues)

**This loop (2026-08-14):** closed the expression-key btree type-validation gap
(commit `8210f676`). `CREATE INDEX ON t ((box_col))` / `((int4range_col))`
silently built a varchar-ordered index before; now `createBTreeIndex`'s
expression-key branch applies the SAME `isSupportedBTreeKeyType` + enum check as
the named-column branch, rejecting both with 0A000. Resolves the expr type via
`planner.ResolveIndexPredicate` + `planner.ExprResultType` (the build path's own
pair); gates only when both resolve, so float/enum/text expression indexes are
untouched. Tests: `TestExpressionIndexKeyRejectsBoxAndInt4Range` +
`TestExpressionIndexKeyStillAllowsFloatEnumText` (mutation-witnessed).

**Reframe discovered (important for the next loop):** the prior "box/int4range
key encodings" remaining-scope was a MISATTRIBUTION. box has NO btree opclass in
PG 18.3 (`pg_opclass.dat`: gist/spgist/brin only, no `box_cmp`) — goopg's
rejection is correct; no box btree encoder should ever be added. int4range IS
btree-legal in PG (`range_ops` default, binary-coercible-to-anyrange) but goopg
has NO range value model (no KindRange, no `range_in`, constructor returns NULL),
so an order-faithful key is blocked on that multi-slice subsystem. The
"005_opclass_damage's wider fixtures use box/int4range/int4[]" line in ledger rows
958/960/961/962 is actually 003_check.pl (box=GiST, int4range=SPGiST, int4[]=GIN,
never btree). `int4[]` was already closed by the array-key-decode row. All recorded
in a new 2026-08-14 ledger row.

**Remaining M0119-0006:** only the whole-database (unscoped) pg_amcheck run —
blocked on unrelated feature work (index AMs hash/gist/gin/spgist/brin, STORAGE
EXTERNAL TOAST corruption, box/int4range/int4[] column types as HEAP types in
003_check.pl, multi-DB orchestration). Plus the reg* broad rows (1307, 1340, 1343,
1344, 1347, 1351, + off-path/dbOid) which are catalog-representation/session-state
changes, not narrow rendering fixes.

**Next step:** the narrow M0119-0006 scope is now drained — both remaining items
are blocked on big feature work. Pick ONE of: (a) investigate the reg* rows for a
genuinely narrow one (the off-path/dbOid row's resume is
`internal/server/logicalwalsender.go:69` — thread the slot search_path + dbOid);
(b) tackle the whole-db amcheck's first blocker (verify_heapam round-tripping
goopg's system-catalog relkinds — the narrowest sub-block, before index AMs);
(c) re-read the banner: M0119 is still top priority (M0132/133/131/130 done), M0122
is below.

**Gates run:** executor/planner/btree package tests PASS; pre-commit units suite
PASS; `scripts/tpch-spotcheck.sh` Q12=2/Q13=35 PASS; pre-commit pgbench smoke PASS
(0 failed, 3 workloads).

**NIGHTLY:** nothing new to file (no `## AI-` subjects without an open task; last
checked this loop via the prior loop's confirmation).
