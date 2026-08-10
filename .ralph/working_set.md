Task: M-NIGHTLY AI-20260810-011258-003 (TestE2E_PGStandbyFullCycle) — blocker #11
FIXED and committed; blockers #10/#12 root-caused and filed. Item stays OPEN.

KEY RE-TRIAGE (do not repeat last loop's plan): blocker #9 ("goopg cannot
replay PG's RM_BTREE records") does NOT reproduce at HEAD. An ApplyRecord trace
over the whole reverse-replay stream shows the promoted PG emits ZERO rmid=11
records; the id=5 INSERT is only RM_HEAP XLOG_HEAP_INSERT (FPI) + RM_XACT
COMMIT, both replayed cleanly, and the heap row IS present on the reverse
standby. What returns 0 is the *index-scan* form of the harness query.

Real chain (fix_plan items 10/11/12 under AI-20260810-011258-003):
- #10 `pg_class.relhasindex` is hardcoded false in `buildUserPGClassRow`
  (`internal/executor/pg18_user_catalog_rows.go`). PG's ExecInitModifyTable
  gates ExecOpenIndices on it, and plancat.c's get_relation_info gates
  RelationGetIndexList on it — so blocker #8's 2678 work is never reached.
  Probed live: relhasindex=false while indisvalid/ready/live are all true.
- #12 (GATES #10 and #9) goopg's USER btree files are a goopg-private format
  (`internal/access/btree`: SizeOfBTPageOpaque=272, btreeVersion=4) vs PG's
  16-byte BTPageOpaqueData → `_bt_checkpage` errors `index "s10_t_val_idx"
  contains corrupted page at block 0` (XX002). Measured: flipping #10 makes
  the E2E fail EARLIER, in Phase B. That is why #10 was reverted.
- #11 FIXED this loop.

Landed (#11 — an index relation had no pg_attribute rows of its own):
- `buildUserPGAttributeRowsForIndex` + `setPGAttributeCol`
  (`internal/executor/pg18_user_catalog_rows.go`): one row per index attribute
  (key cols then INCLUDE, attnum 1..indnatts) per upstream
  ConstructTupleDescriptor (catalog/index.c) — heap column physical type copied
  verbatim, all relation-level flags reset; expression key → `pg_expression_N`
  with a `text` type placeholder (ledgered).
- `syncIndexToCatalogHeap` (`internal/executor/operators_ddl.go`) writes them
  + the `pg_attribute_relid_attnum_index` (2659) entries.
- Guards: `internal/executor/pg_attribute_index_rows_test.go`.

Next step: blocker #12 is a milestone-sized epic (convert
`internal/access/btree` on-disk layout to upstream nbtree: BTMetaPageData,
16-byte BTPageOpaqueData, high keys, posting lists; writers bulkload.go /
btree.go / btree_vacuum.go / posting.go; then PG-faithful btree_redo per
nbtxlog.c). Per the Current Priority banner, re-read it before selecting —
do NOT start #12 inside an M-NIGHTLY triage loop without a milestone.

Gates run: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(executor cached-green, initdb 62.7 s), `go build ./...` clean, new guards PASS,
`TestE2E_PGStandbyFullCycle` fails at the SAME Phase-D point as before this loop
(no regression; Phases A–C unchanged), commit-hook pgbench smoke — see status.

Ledger: 3 new rows (#11 landed + expression-type deferral; #10; #12).

In-flight: none.
