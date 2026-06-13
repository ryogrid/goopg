Task: M0110-0003 (pg_amcheck) — extended the standalone `internal/amcheck`
verify_heapam() engine with the two infomask-only `check_tuple_header`
invariants. Engine-first/wire-later continues; SQL surface still deferred to a
clean tree.

Landed this loop (all in NEW files — zero contaminated files touched):
  - internal/amcheck/verify_heapam.go: added `multixact should not be marked
    committed` (HEAP_XMAX_COMMITTED|HEAP_XMAX_IS_MULTI) + `tuple has been HOT
    updated, but xmax is 0` checks (upstream-verbatim msgs). Helpers rawXmax /
    isHotUpdated / xminInvalid. Local const heapXmaxIsMulti=0x1000.
  - internal/amcheck/verify_heapam_test.go: 4 new tests (2 corruption + 2
    false-positive guards). 15 total PASS (0.003s).
  - docs/design/0110-0005-verify-heapam-engine.md extended (status still
    "accepted (partial)"; README already indexes it).
  - fix_plan.md loop-#52 PROGRESS note + deferral_ledger.md line.

KEY divergence (load-bearing): goopg packs HEAP_HOT_UPDATED/HEAP_ONLY_TUPLE into
t_infomask (NOT t_infomask2 like upstream) — see storage/prune.go + heap_update.
The HOT check reads goopg's t_infomask. On-disk header offsets: xmax[4:8],
infomask2[18:20], infomask[20:22], hoff[22] (storage/heap.go MarshalBinary).

NOT ported (deferred): the heap-only-but-not-updated invariant — goopg never
sets HEAP_UPDATED (reuses 0x2000 for HeapKeysUpdated in t_infomask2), so a
verbatim port false-positives on every healthy HOT successor. Resume = port once
goopg stamps HEAP_UPDATED.

Gates run: go test ./internal/amcheck (15 PASS), gofmt -l clean, go vet clean,
go build ./... EXIT 0, make ralph-state-guard OK (had to realign progress.json
in_progress + timestamp within 2m of status.json — same stale "completed"
desync the previous loop hit).

Next step (DEFERRED — needs a CLEAN tree): the SQL surface — (1) CREATE
EXTENSION amcheck (parser + pg_extension row + pg_proc registration of
verify_heapam/bt_index_check/bt_index_parent_check), (2) the
verify_heapam(regclass,…) SRF walking a relation's blocks through VerifyHeapPage
emitting one row per Report — then port 002_nonesuch.pl (its only checked
relation is a clean pg_catalog.pg_class, so tier-1 suffices for exit-0).

⚠ TREE NOTE (still true): a SEPARATE manual session has uncommitted WIP across
internal/{executor,planner,catalog,analyzer,parser,mvcc}/ + server/dispatch.go +
untracked gen_override test files + postgres/ + validate-ralph-state. NOT
ralph's — stage only your own files (git add <paths>), never `git add -A`.

Other OPEN tasks (all blocked on big features): M0095-0003 (WAL streaming -X
stream), M0110-0001 (pg_dump 002+ catalog parity), M0110-0002 (pg_waldump 002 /
index AMs hash/gin/gist/spgist/brin).
