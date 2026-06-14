Task: M0110-0003 — pg_amcheck. Loop #62: closed the HEAP engine's last
update-chain tier (clog-dependent HOT-chain checks). Committed.

What landed (new code in internal/amcheck/, zero contaminated files touched):
  verify_heapam.go — new entry point VerifyHeapPageWithXminStatus(p, blkno, rel,
  xidStatus XidStatusFunc) + XidCommitStatus enum (Unknown/Committed/InProgress/
  Aborted/Current). Ports verify_heapam.c:759-833: (1) in-progress→committed
  xmin, (2) aborted→in-progress/committed xmin, (3) heap-only-root-of-chain.
  Decoupling seam = injected XidStatusFunc (keeps engine off contaminated mvcc);
  bootstrap(1)/frozen(2 or both-hint-bits) resolve committed w/o callback.
  Page-bytes-only paths pass nil → checks disabled, output byte-identical.
  + resolveXminStatus / headerXmin helpers; lpEntry gains xminStatus/xminStatusOK.
  verify_heapam_test.go — mapXidStatus helper + 10 tests (3 cross-link positive,
  heap-only-root positive, 6 FP guards incl nil-callback regression + frozen).

Heap engine is now LOGIC-COMPLETE (parity w/ B-tree side, done loop #61).

Gates loop #62: gofmt clean; go vet clean; go build ./... OK; go test -race
./internal/amcheck ./internal/access/btree ./internal/storage PASS; ralph-state-guard OK.

CONTAMINATION (confirmed external, do NOT git add -A / do NOT commit): a SEPARATE
LIVE claude session (pid 2177381, parent 1330899) holds uncommitted gen-column
WIP — modified internal/{catalog,parser,analyzer,executor,planner,mvcc/subxact_
visibility}.go + server/dispatch.go + cmd/goopg/main_test.go, untracked
gen_override_test.go x2, .claude/worktrees/*, stray ./postgres marker. WIP
mtimes 2026-06-13 14:19-14:28. Commit ONLY my files (verify_heapam.go +_test.go,
docs/design/0110-0005*.md, docs/design/README.md, .ralph/fix_plan.md, working_set.md).

Next step (M0110-0003 remaining — ALL blocked on a clean tree): the SQL surface
— CREATE EXTENSION amcheck (parser ddl.go) + pg_extension/pg_proc rows
(catalog.go) + verify_heapam/bt_index_check SRFs (planner/executor) — edits
exactly the contaminated files, so it must wait until pid 2177381's WIP commits.
That SQL surface is the slice that promotes AC-002 (002_nonesuch). Also pending:
heapallindexed heap scan + index-tuple formation (needs index TupleDesc, catalog
coupling). Engine itself (heap + btree) is logic-complete — no more engine tiers.
