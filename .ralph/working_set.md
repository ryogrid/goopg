Task: M0110-0003 (pg_amcheck) — amcheck verify engines in `internal/amcheck`.
Engine-first/wire-later. Loop #58 added the first **cross-page** B-tree tier:
`VerifyBtreeLevelSiblingLinks` (sibling-link agreement / per-level uniformity /
circular chain). SQL surface still deferred (needs a CLEAN tree).

Landed loop #58 (all NEW amcheck code — zero contaminated files touched):
  - internal/amcheck/verify_nbtree.go: new `PageSource func(BlockNumber)(Page,error)`
    seam + `VerifyBtreeLevelSiblingLinks(src, leftmost, indexName)`. Walks one
    level left-to-right via BTPageOpaque.Next from `leftmost`, checking
    (verify_nbtree.c:650-790, bt_check_level_from_leftmost):
      1. sibling-link agreement: page.Prev == arrived-from block; leftmost exempt
         (upstream's `leftcurrent != P_NONE` gate). Msg `left link/right link pair
         in index "%s" not in agreement` (:1193).
      2. per-level btpo_level uniformity (msg :774 "leftmost down link for level…").
      3. circular chain: visited-set (subsumes upstream immediate check + bounds
         longer cycles). Msg `circular link chain found in block %u of index "%s"`.
      4. deleted page reached via sibling → `downlink or sibling link points to
         deleted block in index "%s"` (:676).
    P_NONE = storage.InvalidBlockNumber; meta leftmost = damaged-page finding;
    src error = damaged-page finding (no panic). Returns 0 or 1 (first conclusive).
    File-header comment updated (cross-page tier now ported, not deferred).
  - internal/amcheck/verify_nbtree_test.go: `makeLinkedPage` (explicit prev/next)
    + `mapSource` adapter + `fmt`/`none` const; 10 tests (clean 3-page, back-link
    mismatch, leftmost-prev exempt, level mismatch, two-page cycle, self-loop,
    deleted-reachable, dangling right link, meta-leftmost, single-page). PASS.
  - docs/design/0110-0005 ("B-tree cross-page sibling-link tier") + README index;
    fix_plan loop-#58 PROGRESS + deferral_ledger line.

Gates run: go test ./internal/amcheck ./internal/access/btree PASS; verbose run
of all 10 new tests PASS; gofmt -l clean; go vet clean. make ralph-state-guard
(run before status block).

Next step (engine-first, while tree stays dirty): the downlink-to-child /
cross-level descent tiers (bt_index_parent_check) — compare a child page's pivot
keys against the parent downlink; needs BOTH parent + child pages + the key
comparator across levels, so extend the PageSource-based driver (root-descent).
DEFERRED until a CLEAN tree: the SQL surface — CREATE EXTENSION amcheck +
verify_heapam(regclass) SRF + bt_index_check(regclass) wired on
VerifyHeapPageWithRel / VerifyBtreePage / VerifyBtreeItemOrder /
VerifyBtreeLevelSiblingLinks — then port 002_nonesuch.pl (promotes AC-002).

⚠ TREE NOTE (STILL TRUE, STATIC since 2026-06-13 14:28 — now >24h): a SEPARATE
manual session's uncommitted gen-column WIP spans
internal/{executor,planner,catalog,analyzer,parser,mvcc}/ + server/dispatch.go +
cmd/goopg/main_test.go + untracked gen_override test files + postgres/ +
validate-ralph-state. NOT ralph's — stage only your own files (git add <paths>),
never `git add -A`. Engine now near-complete (only cross-level + SQL surface
left, both gated on this). Strongly consider surfacing to the user for a
stash/commit decision next loop — the SQL surface cannot land until it clears.

Other OPEN tasks (all blocked on big features): M0095-0003 (WAL streaming -X
stream), M0110-0001 (pg_dump 002+ catalog parity), M0110-0002 (pg_waldump 002 /
index AMs hash/gin/gist/spgist/brin).
