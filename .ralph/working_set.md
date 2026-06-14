Task: M0110-0003 (pg_amcheck) — amcheck verify engines in `internal/amcheck`.
Engine-first/wire-later. Loop #59 added the **cross-level** B-tree tier:
`VerifyBtreeParentDownlinks` (bt_child_check per-downlink checks). SQL surface
still deferred (needs a CLEAN tree). B-tree engine is now near-complete: only
`heapallindexed` (heap scan + TupleDesc) + the SQL surface remain.

Landed loop #59 (all NEW/additive amcheck+btree code — zero contaminated files):
  - internal/amcheck/verify_nbtree.go: new `VerifyBtreeParentDownlinks(src
    PageSource, parentBlk, indexName)`. For an internal parent, follows every
    downlink to its child (bt_child_check, verify_nbtree.c:2393-2543) and checks:
      1. downlink-to-deleted → `downlink to deleted page found in index "%s"` (:2494)
      2. child level == parent.Level-1 → `downlink points to block in index "%s"
         whose level is not one level down` (:2655)
      3. down-link lower bound: every real child key >= separator K_i →
         `down-link lower bound invariant violated for index "%s"` (:2535)
    goopg-faithful divergences (false-positive-free): (a) INCLUSIVE bound
    (CompareKeys>=0) NOT upstream strict `<` — findChildBlock routes to rightmost
    item <= key so child covers [K_i, K_{i+1}); (b) skip internal child's empty
    neg-inf item 0 (offset_is_negative_infinity). Leaf/deleted parent + metapage →
    nil; read errors → damaged-page finding (no panic). File-header comment updated.
  - internal/access/btree/btree.go: new exported `Downlink{Key,Child}` +
    `PageDownlinks(p) ([]Downlink,error)` (additive accessor, single source of
    truth like PageItemKeys). btree.go is CLEAN (not a contaminated file).
  - internal/amcheck/verify_nbtree_test.go: `makeInternalPage`+`btDownlinkRaw`+`dl`
    helpers; 10 tests (clean, lower-bound viol, downlink-to-deleted, level-not-
    one-down, neg-inf-skip clean, internal-child real-key-below-bound, leaf parent
    nil, meta nil, damaged parent, dangling child). PASS.
  - docs/design/0110-0005 ("B-tree cross-level downlink tier") + README index;
    fix_plan loop-#59 PROGRESS + deferral_ledger line.

Gates run: go test ./internal/amcheck ./internal/access/btree PASS; verbose run of
all 10 new tests PASS; gofmt -l clean; go vet clean. make ralph-state-guard (run
before status block).

Next step: the B-tree engine is essentially done at the page-bytes level.
Remaining work is BLOCKED on a clean tree — the SQL surface: CREATE EXTENSION
amcheck + verify_heapam(regclass) SRF + bt_index_check(regclass) wired on
VerifyHeapPageWithRel / VerifyBtreePage / VerifyBtreeItemOrder /
VerifyBtreeLevelSiblingLinks / VerifyBtreeParentDownlinks, then port
002_nonesuch.pl (promotes AC-002). The only remaining engine tier, `heapallindexed`
(bloom-filter fingerprint vs a fresh heap scan), needs the heap relation + index
TupleDesc — also effectively SQL-surface-coupled.

⚠ TREE NOTE (STILL TRUE, STATIC since 2026-06-13 14:28 — now >1.5 days): a SEPARATE
manual session's uncommitted gen-column WIP spans internal/{executor,planner,
catalog,analyzer,parser,mvcc}/ + server/dispatch.go + cmd/goopg/main_test.go +
untracked gen_override test files + postgres/ + validate-ralph-state. NOT ralph's
— stage only your own files (git add <paths>), never `git add -A`. The amcheck
engine work is now BLOCKED on this clearing (both remaining slices need the SQL
surface). STRONGLY consider surfacing to the user for a stash/commit decision —
amcheck cannot meaningfully progress further without a clean tree.

Other OPEN tasks (all blocked on big features): M0095-0003 (WAL streaming -X
stream), M0110-0001 (pg_dump 002+ catalog parity), M0110-0002 (pg_waldump 002 /
index AMs hash/gin/gist/spgist/brin).
