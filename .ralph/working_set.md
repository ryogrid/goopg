Task: M0130-S11.1 (PG nbtree format layer) — DONE and committed.

What changed the plan this loop: M-NIGHTLY AI-20260810-011258-003's remaining
blockers (#10 relhasindex, #12 private btree page format) are a milestone-sized
on-disk-format conversion, not triage work. They were promoted out of the
M-NIGHTLY item into a new **M0130 Theme D (M0130-S11.1 .. S11.6)** with a design
doc, and S11.1 landed.

Landed:
- `internal/access/btree/pgformat.go` — upstream 16-byte `BTPageOpaqueData` +
  48-byte `BTMetaPageData` codecs, `BTP_*` flags, `P_NONE`, `InitPGBTPage`
  (`_bt_pageinit`), `InitPGMetaPage` (`_bt_initmetapage`), `CheckPGBTPage`
  (Go `_bt_checkpage`). Additive — legacy 272-byte layout in `btree.go` untouched.
- `internal/access/btree/pgformat_test.go` — 7 guards. Both padding clears
  (alignment hole at struct offset 28, 7-byte tail after `btm_allequalimage`)
  mutation-verified.
- `docs/design/0130-0011-nbtree-pg-on-disk-format.md` (draft) + README row.
- fix_plan: Theme D slices; AI-003 annotated "CLOSED BY S11.6". Ledger: 1 row.

Key facts for the next loop (do not re-derive):
- Layout verified twice: a `sizeof`/`offsetof` probe compiled with
  `gcc -Ipostgres/src/include`, and a byte golden from a metapage a real PG 18.3
  wrote — block 0 of an empty catalog index under
  `bench/tpch/runtime/pgdata/base/1` (135 such files; find them by
  `pd_special == 8176 && magic == 0x53162`). No PG server needed.
- Already PG-correct in goopg and must NOT be "fixed": `BTREE_MAGIC` 0x053162,
  `BTREE_VERSION` 4, the 24-byte page header, and the line-pointer array
  (`storage.PageInsertItemRawAt`).
- Trap: `InitPGMetaPage` zeroes the page via `storage.InitPage`, so a
  stale-padding test written against it is vacuous — call `WritePGMetaPage`
  directly on a dirty buffer.

Next step: M0130-S11.2 (page shape) — switch `readOpaque`/`writeOpaque`/
`ParseOpaque` in `internal/access/btree/btree.go` to `Read/WritePGOpaque`, move
the HighKey out of the opaque to a `P_HIKEY` item at offset 1, and translate
`InvalidBlockNumber`→`P_NONE` plus the flag bits in the SAME edit. Sibling
readers that must move in lockstep: `internal/amcheck`, `replay.go`; other
writers: `bulkload.go`, `btree_vacuum.go`. Breaks every existing index on disk
(REINDEX) — say so in the commit message. Re-read the fix_plan banner first.

Gates run: `go build ./...` clean; `go vet ./internal/access/btree/` clean;
`go test ./internal/access/btree/` PASS (2.3 s);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(initdb 60.2 s, wal 8.3 s, rest cached-green); `make ralph-state-guard` OK
(auto-repaired the stale completed marker); commit-hook pgbench smoke — see status.

In-flight: none.
