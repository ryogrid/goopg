Task: M0110-0003 (pg_amcheck) — amcheck verify engines in `internal/amcheck`.
Engine-first/wire-later. Loop #56 added the **second B-tree tier**
(`VerifyBtreeItemOrder`), the page-local item-order + high-key invariants. SQL
surface still deferred (needs a CLEAN tree).

Landed loop #56 (all NEW code in new funcs + purely-additive exports):
  - internal/amcheck/verify_nbtree.go: new `VerifyBtreeItemOrder(p, blkno,
    indexName)` ports bt_target_page_check's two page-local key invariants
    (verify_nbtree.c:1565-1642): item-order (strictly ascending →
    `item order invariant violated for index "%s"`) and high-key (leaf `<=`,
    internal `<` → `high key invariant violated for index "%s"`). Verbatim msgs,
    returns 0/1 finding (first violation conclusive), meta+deleted pages → nil.
  - internal/access/btree/btree.go (UNCONTAMINATED, additive): new exported
    `PageItemKeys(p) ([][]byte, error)` — one separator key per physical line
    pointer, collapsing posting-list items to their single shared key. Single
    source of truth for the inline item layout (v3→v4 drift hazard).
  - internal/amcheck/verify_nbtree_test.go: 10 tests + new `makeItemsPage`
    builder (sets pd_special/pd_upper before adding items so item data doesn't
    clobber the opaque; self-checks via real readers).
  - internal/access/btree/posting_test.go: `TestPageItemKeys` (regular + 3-TID
    posting → 2 keys).
  - docs/design/0110-0005 + README index extended; fix_plan loop-#56 PROGRESS +
    deferral_ledger line.

KEY divergences handled (faithful port): high key in opaque area (no P_HIKEY slot
to skip; rightmost = Next==InvalidBlockNumber); internal leftmost neg-infinity
downlink has EMPTY key → strictly less than all → satisfies both invariants with
no special case; CompareKeys = bytes.Compare on order-preserving keys.

Gates run: go test ./internal/amcheck (PASS), go test ./internal/access/btree
(PASS), verbose run of all 11 new tests PASS, gofmt -l my files clean, go vet
clean, go build ./internal/amcheck ./internal/access/btree clean,
make ralph-state-guard (run before status block).

Next step (DEFERRED — needs CLEAN tree): the SQL surface — CREATE EXTENSION
amcheck + verify_heapam(regclass) SRF + bt_index_check(regclass) wired on
VerifyHeapPageWithRel / VerifyBtreePage / VerifyBtreeItemOrder — then port
002_nonesuch.pl. Next NEW-file btree tier while tree stays dirty: the goopg
MaxIndexTuplesPerPage-equivalent item-count ceiling (needs goopg tuple-size
accounting).

⚠ TREE NOTE (STILL TRUE, STATIC since 2026-06-13 14:28 — now >1 day): a SEPARATE
manual session's uncommitted gen-column WIP spans
internal/{executor,planner,catalog,analyzer,parser,mvcc}/ + server/dispatch.go +
cmd/goopg/main_test.go + untracked gen_override test files + postgres/ +
validate-ralph-state. NOT ralph's — stage only your own files (git add <paths>),
never `git add -A`. Static mtime suggests it may be abandoned; if it blocks for
many more loops, consider surfacing to the user for a stash/commit decision.

Other OPEN tasks (all blocked on big features): M0095-0003 (WAL streaming -X
stream), M0110-0001 (pg_dump 002+ catalog parity), M0110-0002 (pg_waldump 002 /
index AMs hash/gin/gist/spgist/brin).
