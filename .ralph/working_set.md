Task: M0110-0003 (pg_amcheck) — loop #14. COMPLETE for the index file-removal
(missing-main-relation-fork) corruption tier of 003_check.pl. Ready to commit.

=== WHAT LANDED (this loop) ===
goopg now detects a removed btree INDEX main fork as corruption, matching
upstream bt_index_check_callback's smgrexists(MAIN_FORKNUM) guard
(verify_nbtree.c:318). Root cause it fixes: goopg smgr opens files with
os.O_CREATE, so Pool.NBlocks on a removed fork RECREATED it empty → engine
reported the index "clean" (silent false negative).
Files:
- internal/storage/smgr.go: + Manager.Exists(rel) — pure os.Stat(relPath),
  NOT via relFile (which O_CREATEs). Faithful smgrexists.
- internal/storage/bufpool.go: + Pool.Exists delegate.
- internal/executor/operators_bt_index_check.go: evalBtIndexCheck calls
  ctx.Pool.Exists(rel) BEFORE NBlocks; absent → ExecError XX002
  (ERRCODE_INDEX_CORRUPTED) `index "%s" lacks a main relation fork`. Covers
  bt_index_check + bt_index_parent_check.
- internal/executor/operators_bt_index_check_test.go: + TestBtIndexCheck_
  DetectsMissingRelationFork (drops fork via DropRelation, asserts msg).
- internal/testport/pgamcheck003_missingfork_test.go (NEW): e2e through the
  real pg_amcheck binary, stop→unlink→restart, exit 2 + verbatim report, and
  asserts the fork is NOT recreated on restart. PASS 7.3s.
Docs: CSV AC-003 rationale + md regenerated; design 0110-0003 appended
("Missing-main-relation-fork tier"); fix_plan + deferral ledger updated.
Gates: go build ./... clean; full internal/testport pg_amcheck suite PASS
(13.8s); internal/executor + internal/storage PASS; go vet testport clean.
NOTE: gofmt -l flags internal/storage/bufpool.go for PRE-EXISTING drift
(statePin alignment L34-40 + double blank L722) — NOT my edit; left untouched.

=== NEXT STEP (resume) ===
AC-003 stays `defer`. Remaining (all larger / unrelated): heap-table
file-removal (`could not open file ".*": No such file or directory` — needs
smgr to surface a typed missing-file error/relpath; its O_CREATE open also
recreates the file), hash/gist/gin/brin/spgist index AMs, box/int4range/int4[]
types, STORAGE EXTERNAL TOAST corruption, multi-DB orchestration; and
005_opclass_damage (CREATE OPERATOR CLASS + pg_amproc parity). Other open
milestones: M0095-0003 recvlogical (logical decoding, large); M0110-0001
pg_dump 002 (catalog parity); M0110-0002 pg_waldump 002 (PG-format heap WAL
FPI); M0117-0006/7/8 (Effort-L CLOG memory-model, defer to full-gate sessions).
