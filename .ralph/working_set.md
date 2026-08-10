Task: M0130-S11.2a (nbtree page-shape primitives) — DONE and committed.

S11.2 was split this loop: the primitives (additive, independently testable)
landed as S11.2a; the on-disk flip is now S11.2b and is the next task.

Landed:
- `internal/storage/linepointer.go` — `PageReserveLinePointer` (the `pd_lower`
  bump in `_bt_blnewpage`) and `PageDeleteLinePointerAt` (`_bt_slideleft`).
  goopg had neither: `PageAddItemRaw` always allocates payload and
  `PageRemoveHeapTuple` blanks a slot in place instead of sliding the array.
- `internal/access/btree/pgpage.go` — `P_HIKEY`/`P_FIRSTKEY`, `PGFirstDataKey`
  (`P_FIRSTDATAKEY`), the `pgXxx(p, dataSlot)` accessor wrappers, `PGHighKeyRaw`
  / `pgSetHighKeyRaw` / `pgPromoteToNonRightmost`, `pgReserveHiKeySlot` /
  `pgSlideLeft`, `pgSibling`/`legacySibling`, `pgFlags`.
- Tests: `pgpage_test.go` (8), `linepointer_test.go` (3). The `P_FIRSTDATAKEY`
  bias is mutation-verified (forcing it to 1 fails 3 tests).
- Design doc §S11.2 now fixes the flip mechanism; fix_plan S11.2a[x]/S11.2b[ ];
  1 ledger row.

Key facts for the next loop (do not re-derive):
- Upstream has NO "has high key" flag — `P_FIRSTDATAKEY` keys off `P_RIGHTMOST`
  and 0x0008 is `BTP_META`. So `BTHasHighKey` is DELETED in the flip, and
  `HasHighKey()` becomes `!IsRightmost()`. Deleting the constant is the trick
  that makes the compiler enumerate all ~25 real high-key sites.
- Sibling link and high key must be written in the same critical section: the
  wrappers read the rightmost bit off the page, so between the two writes the
  page's own accessors disagree about where its data starts.
- Bulk load: reserve `P_HIKEY` at page creation, fill from `P_FIRSTKEY`, and
  slide it away if the page ends up rightmost (nbtsort.c lines ~627, ~677-700).
- Call-site inventory for the flip: 45 `storage.PageXxx(` calls in
  `internal/access/btree/*.go` + `internal/amcheck/*.go`; ~25 non-comment
  `HighKey` sites; 65 `InvalidBlockNumber` sites. External `ParseOpaque`
  consumers: `internal/amcheck/{verify_nbtree,verify_nbtree_unique,
  heapallindexed_relation}.go`, `internal/executor/operators_bt_index_check.go`.

Next step: M0130-S11.2b — the flip, in ONE commit. Start by deleting
`BTHasHighKey` and the `HighKey` field from `BTPageOpaque` in btree.go and let
the build errors drive the edit; commit message must say every existing index
needs REINDEX. Re-read the fix_plan banner first.

Gates run: `go build ./...` clean; `go vet ./internal/storage/
./internal/access/btree/` clean; `go test ./internal/access/btree/
./internal/storage/` PASS; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (initdb 59.7 s, wal 8.3 s, rest
cached-green); `make ralph-state-guard` OK; commit-hook pgbench smoke — see
status.

In-flight: none.
