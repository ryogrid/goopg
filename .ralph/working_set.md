Task: M0110-0003 (pg_amcheck port) — SQL surface still HARD-BLOCKED by the
foreign gen-column WIP. The amcheck ENGINE keeps progressing: this loop landed
the heap **xmax numeric-bounds tier** (committed). MVCC tier is now nearly
exhausted — only the multixact-member path remains, and goopg has no on-disk
multixact horizon to compare against (corruption-only territory).

LANDED THIS LOOP (loop #24, 2026-06-14):
- internal/amcheck/verify_heapam.go: new `checkXmaxBounds` + call site in
  verifyHeapPage (right after checkXminBounds, same `rel.NextXid != 0` gate).
  Ports verify_heapam.c:check_tuple_visibility's plain-XID xmax sanity check
  (lines 1466-1496). Gates skip multi/invalid/lock-only/zero/special xids. Reuses
  existing RelDesc horizons; clog-independent. Package doc + deferred list updated.
- internal/amcheck/verify_heapam_xmaxbounds_test.go: 12 tests (all PASS).
- docs/design/0110-0005: "Heap xmax numeric-bounds tier" section + header line +
  deferred-list entry (xmin/xmax bounds now DONE; only multixact deferred).
- .ralph/fix_plan.md (loop #24 note), deferral_ledger.md (productive-loop line).
Key symbols: checkXmaxBounds, rawXmax, readInfomask, storage.IsHeapTupleLockOnly,
heapXmaxIsMulti, RelDesc{NextXid,OldestXid,RelFrozenXid}. Upstream:
verify_heapam.c check_tuple_visibility (1112), plain-xid xmax (1466), get_xid_status (2111).

Gates run: `gofmt -l` clean; `go vet ./internal/amcheck` clean;
`go test ./internal/amcheck` PASS (12 new xmax tests verified verbose).
make ralph-state-guard consistent (self-healed prev-loop marker, expected).
(No TPC-H gate — amcheck is its own package, no executor/planner code touched.)

STATE: foreign gen-column WIP STILL frozen across internal/{parser,planner,
executor,catalog,analyzer,mvcc}/ + server/dispatch.go + untracked gen_override
test files. Owning session `claude --resume ec98936f`. DO NOT touch/stash/commit
it — a HUMAN must clear it.

Next step (while tree stays blocked): the MVCC numeric-bounds tier is complete;
the remaining engine slices are (a) the `heapEntries`/`heapallindexed` producer
(heap scan + index-tuple formation to drive bt_index_check's heap-vs-index
cross-check), and (b) any not-yet-ported B-tree check. Once the tree is CLEAN:
wire SQL surface slice S1 from docs/design/0110-0008 (CREATE EXTENSION amcheck +
pg_extension row), then S2, then port 002_nonesuch.pl.
