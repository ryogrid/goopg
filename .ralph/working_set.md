Task: M0110-0003 (pg_amcheck) — loop #15. COMPLETE for the heap-table
file-removal corruption tier of 003_check.pl. Committed.

=== WHAT LANDED (this loop) ===
goopg now detects a removed HEAP main fork as corruption, the companion to
loop #14's index fork. Mirrors upstream verify_heapam opening the relation's
main fork (RelationGetNumberOfBlocks → mdnblocks fails for a removed file).
Root cause it fixes: goopg smgr opens files with os.O_CREATE, so Pool.NBlocks
on a removed heap fork RECREATED it empty → VerifyHeapRelation reported the
table "clean" (silent false negative).
Files:
- internal/storage/smgr.go: + Manager.RelPath(rel) — data-dir-relative
  base/<db>/<relfile> path (forward slashes), faithful to upstream relpath().
- internal/storage/bufpool.go: + Pool.RelPath delegate.
- internal/executor/operators_verify_heapam.go: verifyHeapamOp.Open calls
  ctx.Pool.Exists(rel) BEFORE NBlocks; absent → ExecError 58030
  (ERRCODE_IO_ERROR, what errcode_for_file_access yields for ENOENT)
  `could not open file "%s": No such file or directory` via Pool.RelPath.
- internal/executor/operators_verify_heapam_test.go: + TestVerifyHeapam_
  DetectsMissingRelationFile (drops fork via DropRelation, asserts msg).
- internal/testport/pgamcheck003_missingheap_test.go (NEW): e2e through the
  real pg_amcheck, stop→unlink→restart, exit 2 + verbatim report, asserts the
  fork is NOT recreated on restart. PASS 7.0s.
Docs: CSV AC-003 rationale + md regenerated; design 0110-0003 appended
("Missing-heap-relation-file tier"); fix_plan + deferral ledger updated.
Gates: go build ./... clean; full internal/testport pg_amcheck suite PASS
(20.4s); internal/executor + internal/storage PASS; go vet clean.
NOTE: gofmt -l still flags internal/storage/bufpool.go for PRE-EXISTING drift
(statePin alignment L34-40 + double blanks L728/L1333) — NOT my edit; my
RelPath hunk is clean (confirmed via gofmt -d). Left untouched per loop #14.

=== NEXT STEP (resume) ===
AC-003 stays `defer`. Both file-removal cases done; remaining 003_check tiers
are unsupported-feature/corruption: hash/gist/gin/brin/spgist index AMs,
box/int4range/int4[] types, STORAGE EXTERNAL TOAST corruption, page-overwrite
mechanics for unsupported relkinds, multi-DB orchestration; and
005_opclass_damage (CREATE OPERATOR CLASS + pg_amproc parity). Other open
milestones: M0095-0003 recvlogical (030, logical decoding, large); M0110-0001
pg_dump 002; M0110-0002 pg_waldump 002 (PG-format heap WAL FPI); M0117-0006/7/8
(Effort-L CLOG memory-model, defer to full-gate sessions).
