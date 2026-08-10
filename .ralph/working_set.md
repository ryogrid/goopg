Task: M0130-S11.4 slice 3b-2c-ii-B2-b-ii (the redo path's descriptor) — DONE,
committed + pushed. **All B2-c prerequisites are now closed; B2-c (the flip,
REINDEX-required) is the next slice.**

Landed (no on-disk PAGE change; `pgIndexTupleKeys` still false — but the native
WAL record format DID change):
- `btree.ApplyInsertRecordAt(page, raw, offnum)` replaces
  `(IndexFormat).ApplyInsertRecord`: one `PageInsertItemRawAt` at the recorded
  PHYSICAL offset, upstream `btree_xlog_insert`. No parse, no comparison ⇒ no
  format in redo.
- Offset emitted for real: `storage.LogBtreeInsertFunc` +offnum, the 3
  btree.go emit sites use the new `pgPhysOffnum(page, lineIdx)`,
  `EncodeBtreeInsertPG` stops hard-coding 0 (closes the A5 parity gap for a
  real-PG standby). Native `btreeInsertHeaderSize` 14 → 16; `offnum == 0`
  (pre-slice record) is a hard error — **a pre-slice WAL stream is not
  replayable; re-initdb**.
- `ReplayRemoveParentDownlink` is format-free (free function again): survivors
  re-added verbatim, `len(raw) > SizeOfIndexTupleData` = "still has key attrs",
  `BTreeTupleGetDownLink` = child, `PGBTPivotRaw(nil, child)` for the demoted
  leftmost.
- `internal/wal/recovery.go:redoBlobIndexFormat` DELETED.

Key facts for the next loop (do not re-derive):
- Minus-infinity pivot bytes are IDENTICAL in blob and tuple format (no key
  bytes to encode) — that identity is what makes the parent-downlink limb
  format-free, and it is pinned by `TestMinusInfinityPivotIsFormatIndependent`.
- `pgPhysOffnum` reading `btpo_next` AFTER the insert is safe: an insert never
  touches the sibling link, so `P_FIRSTDATAKEY` is stable.
- Guard file `internal/access/btree/replay_offnum_test.go` keeps the RETIRED
  by-key body as `oldByKeyReplay` and asserts it DISAGREES with the writer on a
  tuple-format page (int4 -1 = 0xffffffff sorts after +1 bytewise).
- Still open (ledger rows): numeric/uuid heap images are TEXT varlenas so
  `buildPGIndexKeyDesc` refuses them; `bt_child_highkey_check` unported;
  upstream INSERT_POST/_META variants unemitted (S11.5).

Next step: M0130-S11.4 slice 3b-2c-ii-B2-c — the flip. `encodeCompositeBTreeKey`
/ `encodeIndexKeyFromCols` / `encodeArbiterKey` → `pgIndexTupleKey` under
`ctx.pgIndexKeyDesc(idx)` (search keys included), `pgIndexTupleKeys` on, explicit
dual-format decision for indexes the resolver refuses. Gates: tpch-spotcheck +
**the TPC-DS SF0.5 gate (mandatory for B2-c)**, re-pin after a REINDEX. Re-read
the fix_plan banner first (M-NIGHTLY filing unconditional; all six
`AI-20260810-011258-*` items already filed and left unchecked per the banner).

Gates run: `go build ./...` + `go vet` clean; `go test` PASS for
./internal/access/btree ./internal/wal ./internal/storage ./internal/initdb
./internal/executor; crash-recovery subset (`TestCrash*`, initdb) PASS;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=35); commit-hook pgbench
smoke PASS. NOT run: TPC-DS SF0.5 gate (no query-execution change this slice).

In-flight: none.
