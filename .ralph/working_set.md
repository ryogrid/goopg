Task: M0110-0003 (pg_amcheck) — extended the standalone `internal/amcheck`
verify_heapam() engine with the **page-structural HOT-chain (update-chain)
tier**. Engine-first/wire-later continues; SQL surface still deferred to a clean
tree.

Landed loop #53 (all in NEW files — zero contaminated files touched):
  - internal/amcheck/verify_heapam.go: `VerifyHeapPage` now takes `blkno
    storage.BlockNumber`; builds per-offset `lpEntry` (valid/lpOff/successor)
    in pass 1, then `checkUpdateChains` (pass 2) emits the 5 page-structural
    HOT-chain messages (redirect→non-heap-only; redirect & normal chain
    intersection; non-heap-only/heap-only update mismatch ×2). Helpers
    readInfomask / isHeapOnly. Tagged switch on redirect-target flags.
  - internal/amcheck/verify_heapam_test.go: +8 tests (setCTID/setXmin/
    makeRedirect helpers; 5 corruption + 3 false-positive guards incl.
    cross-block CTID). 23 total PASS (0.003s). All 15 prior calls updated to
    `VerifyHeapPage(p, 0)`.
  - docs/design/0110-0005-verify-heapam-engine.md: new HOT-chain section + API
    sig + deferred/testing updates (README already indexes it).
  - fix_plan.md loop-#53 PROGRESS note + deferral_ledger.md line.

KEY divergences (load-bearing): goopg t_ctid.block is a plain uint32 at
off+12..16 (NOT PG's bi_hi/bi_lo BlockIdData split); offset at off+16..18.
HEAP_HOT_UPDATED/HEAP_ONLY_TUPLE live in t_infomask (not t_infomask2). Normal
chain link forms only on curr_xmax==next_xmin && !=0 (non-multi update xid);
multi xmax skipped (goopg has no on-disk multixact).

NOT ported (deferred): clog-dependent HOT-chain checks (xmin commit-status
across a link verify_heapam.c:768/:790; root-of-chain-but-heap-only :828) — all
need per-tuple XID_COMMITTED/ABORTED/IN_PROGRESS i.e. clog. Also still: the
heap-only-but-not-updated header invariant (needs HEAP_UPDATED stamping) and the
MVCC/attribute tier.

Gates run: go test ./internal/amcheck (23 PASS), gofmt -l clean, go vet clean,
go build ./internal/amcheck clean, make ralph-state-guard (pending).

Next step (DEFERRED — needs a CLEAN tree): the SQL surface — (1) CREATE
EXTENSION amcheck (parser + pg_extension row + pg_proc registration of
verify_heapam/bt_index_check/bt_index_parent_check), (2) the
verify_heapam(regclass,…) SRF walking a relation's blocks through VerifyHeapPage
(pass blkno per block) emitting one row per Report — then port 002_nonesuch.pl
(its only checked relation is a clean pg_catalog.pg_class, so tier-1 suffices).

⚠ TREE NOTE (still true): a SEPARATE manual session has uncommitted WIP across
internal/{executor,planner,catalog,analyzer,parser,mvcc}/ + server/dispatch.go +
cmd/goopg/main_test.go + untracked gen_override test files + postgres/ +
validate-ralph-state. NOT ralph's — stage only your own files (git add <paths>),
never `git add -A`.

Other OPEN tasks (all blocked on big features): M0095-0003 (WAL streaming -X
stream), M0110-0001 (pg_dump 002+ catalog parity), M0110-0002 (pg_waldump 002 /
index AMs hash/gin/gist/spgist/brin).
