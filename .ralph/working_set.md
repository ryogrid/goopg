Task: M0110-0003 — pg_amcheck. Loop #63: ported the heap relation-walking
driver (the verify_heapam() SRF outer loop). Committed. Engine fully complete.

What landed (new files only, zero contaminated files touched):
  verify_heapam_relation.go — VerifyHeapRelation(src PageSource, nblocks,
  HeapRelOptions{StartBlock,EndBlock *int64, Rel RelDesc, XidStatus}) →
  []HeapRelReport{Blkno,Offset,Msg}. Ports verify_heapam.c:367-405,480-501:
  empty-rel exit (nblocks==0→nil), startblock/endblock resolve (nil=NULL→
  0/nblocks-1) + upstream range errors, block loop calling verifyHeapPage,
  tagging each finding w/ blkno. REUSES the existing B-tree PageSource seam
  (heap+btree now symmetric). relkind/relam guard + relation open + toast walk
  intentionally left to the SRF executor (S3) — catalog/goopg-storage coupled.
  verify_heapam_relation_test.go — heapMapSource builder + 9 tests.

Gates loop #63: gofmt clean; go vet ./internal/amcheck clean; go build ./... OK;
go test -race ./internal/amcheck PASS (all, incl 9 new); ralph-state-guard OK.
No TPC-H gate (no planner/executor/codec touched — pure internal/amcheck engine).

ENGINE IS NOW FULLY COMPLETE: heap per-page + heap relation-walk + btree all
tiers + heapallindexed + bloom. No more engine work exists.

CONTAMINATION (confirmed external, do NOT git add -A / do NOT commit): separate
LIVE claude session pid 2177381 (parent 1330899, alive 22h+) holds uncommitted
gen-column WIP — modified internal/{catalog,parser,analyzer,executor,planner,
mvcc/subxact_visibility}.go + server/dispatch.go + cmd/goopg/main_test.go,
untracked gen_override_test.go x2, .claude/worktrees/*, stray ./postgres marker.
WIP mtimes FROZEN at 2026-06-13 14:28 (catalog.go = 270-line gen-column churn).
Commit ONLY my files.

Next step (M0110-0003 remaining — ALL blocked on a clean tree): execute SQL
surface Slice S1 (CREATE EXTENSION amcheck parse→dispatch→executor + pg_extension
row) then S2 (probe query answerable + seed pg_proc rows) — that pair promotes
AC-002 (002_nonesuch). S3 (verify_heapam SRF) is now a THIN adapter over
VerifyHeapRelation. Full execute-ready plan in docs/design/0110-0008. The surface
edits exactly the contaminated files, so it must wait until pid 2177381 commits/
stashes its WIP. No non-contaminating engine work remains; if the tree is still
dirty next loop, the honest move is BLOCKED (external session must resolve).
