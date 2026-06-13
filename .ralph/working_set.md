Task: M0110-0003 (pg_amcheck) — amcheck verify engines in `internal/amcheck`.
Engine-first/wire-later. Loop #57 added the **third B-tree tier**: the
item-count ceiling (palloc_btree_page's maxoffset>MaxIndexTuplesPerPage).
SQL surface still deferred (needs a CLEAN tree).

Landed loop #57 (all NEW amcheck code + ONE additive btree.go const):
  - internal/access/btree/btree.go: new exported `MaxItemsPerPage` const
    (=680), beside `itemPrefixSize`. goopg analogue of PG's
    MaxIndexTuplesPerPage = (BlockSize - SizeOfPageHeaderData) / (4 + itemPrefixSize)
    = 8168/12. goopg items are UNALIGNED (pageHasSpaceFor reserves exactly
    itemIDSize+itemPrefixSize+len(key)), so no MAXALIGN term. Single source of
    truth — engine never re-derives the inline item layout.
  - internal/amcheck/verify_nbtree.go: `VerifyBtreePage` now does the
    item-count check AFTER the leaf/internal level checks (upstream order,
    verify_nbtree.c:3396-3402). count>MaxItemsPerPage → verbatim msg
    `Number of items on block %u of index "%s" exceeds MaxIndexTuplesPerPage (%u)`
    (goopg constant). Damaged pd_lower (PageLinePointerCount error) → damaged-page
    finding, no panic. File-header comment updated (ceiling no longer "deferred").
  - internal/amcheck/verify_nbtree_test.go: new `makeCountPage(t,count)` builder
    (bumps pd_lower to claim count without materialising bodies — a count > ceiling
    can't physically fit, so corrupt pd_lower is the only way it arises) + `strings`
    import. 5 tests: TestBtreeMaxItemsPerPageValue (pins 680), at-ceiling clean,
    over-ceiling exact msg, damaged pd_lower, deleted-page suppression.
  - docs/design/0110-0005 ("B-tree item-count ceiling tier") + README index;
    fix_plan loop-#57 PROGRESS + deferral_ledger line.

KEY divergences handled: bound ignores opaque area (like upstream, extra
headroom → no false positives); deleted pages SKIP the count check (goopg returns
earlier; deleted pages hold no live items; avoids reading type-punned fields)
whereas upstream checks them — documented in-code.

Gates run: go test ./internal/amcheck (PASS) + ./internal/access/btree (PASS),
verbose run of all 5 new tests PASS, gofmt clean, go vet clean, go build clean,
make ralph-state-guard (run before status block).

Next step (DEFERRED — needs CLEAN tree): the SQL surface — CREATE EXTENSION
amcheck + verify_heapam(regclass) SRF + bt_index_check(regclass) wired on
VerifyHeapPageWithRel / VerifyBtreePage / VerifyBtreeItemOrder — then port
002_nonesuch.pl. Next NEW-file btree tier while tree stays dirty: the
cross-page/cross-level tiers (downlink/sibling-link agreement, root-descent via
bt_index_parent_check) — these need multi-page traversal state, so they want a
small relation-walking driver (can take an already-opened page source as a
param to stay new-file/additive and defer the catalog lookup to the SQL surface).

⚠ TREE NOTE (STILL TRUE, STATIC since 2026-06-13 14:28 — now >14h): a SEPARATE
manual session's uncommitted gen-column WIP spans
internal/{executor,planner,catalog,analyzer,parser,mvcc}/ + server/dispatch.go +
cmd/goopg/main_test.go + untracked gen_override test files + postgres/ +
validate-ralph-state. NOT ralph's — stage only your own files (git add <paths>),
never `git add -A`. Static mtime suggests it may be abandoned; with the engine
now quite complete and only the SQL surface (gated on this) + cross-page tiers
left, consider surfacing to the user for a stash/commit decision next loop.

Other OPEN tasks (all blocked on big features): M0095-0003 (WAL streaming -X
stream), M0110-0001 (pg_dump 002+ catalog parity), M0110-0002 (pg_waldump 002 /
index AMs hash/gin/gist/spgist/brin).
