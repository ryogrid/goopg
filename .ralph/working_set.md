Task: M0110-0003 (pg_amcheck) — amcheck verify engines in `internal/amcheck`.
Engine-first/wire-later. Loop #61 ported the **heapallindexed fingerprint+probe
core** — the LAST B-tree verification tier. The B-tree engine is now
LOGIC-COMPLETE: every tier `bt_check_every_level` performs is ported. What
remains is the heap-scan plumbing + the SQL surface — both blocked on a CLEAN
tree.

Landed loop #61 (all NEW/additive code — ZERO contaminated files):
  - internal/amcheck/heapallindexed.go: `VerifyBtreeHeapAllIndexed(indexLeafEntries,
    heapEntries []btree.LeafEntry, indexName, tableName string, seed uint64)
    []BtreeReport`. Pure fn: bloomCreate(len(index), 64MB, seed) → fingerprint
    every index leaf entry → probe every heap entry → one report per
    bloomLacksElement with verbatim upstream msg `heap tuple (b,o) from table
    "T" lacks matching index tuple within index "I"`. Sound via no-false-negatives.
    `fingerprintLeafEntry` (TID.Block BE:4 ++ TID.Offset:2 ++ key bytes) drives
    BOTH phases — the load-bearing sibling-path invariant. Divergences: no
    bt_normalize_tuple (goopg doesn't TOAST index keys → leaf & heap-formed key
    bytes already one canonical EncodeXxxKey form); seed is a param (caller
    randomizes, tests pin). heapAllIndexedWorkMemKB=64MB const (wire-later GUC hook).
  - internal/access/btree/btree.go: new exported `LeafEntry{Key,TID}` +
    `PageLeafEntries(p)` — canonical leaf reader that EXPANDS posting items to one
    entry per heap TID (contrast PageItemKeys which collapses to one key). Beside
    PageItemKeys/PageDownlinks. btree.go was NOT contaminated.
  - Tests: heapallindexed_test.go (6: NoFalseNegatives load-bearing @n=100k,
    DetectsMissingHeapTuple exact msg+block, DistinguishesByTID, EmptyIndex,
    EmptyHeap, SharedKeyDistinctTIDs) + posting_test.go TestPageLeafEntries
    (plain + 3-TID posting → 4 entries).
  - docs/design/0110-0007-amcheck-heapallindexed.md + README row; fix_plan loop-#61
    PROGRESS; deferral_ledger loop-#61 line.

Gates run: go test ./internal/amcheck ./internal/access/btree PASS (7 new + existing);
go build OK; gofmt -l clean; go vet ./internal/amcheck clean. make ralph-state-guard
(run before status block).

Next step (BLOCKED on a clean tree): the SQL surface is now the ONLY remaining
amcheck work — CREATE EXTENSION amcheck + verify_heapam(regclass) SRF +
bt_index_check(regclass [, heapallindexed]) wired on the VerifyHeap*/VerifyBtree*
engines, with the heapallindexed path (a) walking the leaf level via
btree.PageLeafEntries and (b) running a snapshot heap scan that re-forms each
tuple's index tuple via the index TupleDesc, feeding both slices to
VerifyBtreeHeapAllIndexed. Then port 002_nonesuch.pl (promotes AC-002). Also:
hash unification (substitute shared Jenkins for bloomHash64 once it can leave
internal/executor) — distribution-only, no contract change.

⚠ TREE NOTE (STILL TRUE, STATIC since 2026-06-13 14:28 — now ~2 days): a SEPARATE
manual session's uncommitted gen-column WIP spans internal/{executor,planner,
catalog,analyzer,parser,mvcc}/ + server/dispatch.go + cmd/goopg/main_test.go +
untracked gen_override test files + postgres/ + validate-ralph-state. NOT ralph's
— stage only your own files (git add <explicit paths>), never `git add -A`. The
amcheck SQL surface is BLOCKED on this clearing — it must touch parser (CREATE
EXTENSION / SRF), planner, executor, catalog, all contaminated. STRONGLY consider
surfacing to the user for a stash/commit decision: the B-tree engine is now
logic-complete and amcheck cannot progress past it without a clean tree.

Other OPEN tasks (all blocked on big features): M0095-0003 (WAL streaming -X
stream), M0110-0001 (pg_dump 002+ catalog parity), M0110-0002 (pg_waldump 002 /
index AMs hash/gin/gist/spgist/brin).
