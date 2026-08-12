(idle — nothing in flight)

M0131-S21b PART 3 LANDED (loop #166) — and S21b is CLOSED.
`XLOG_BTREE_DELETE` (0x70) + `XLOG_BTREE_REUSE_PAGE` (0xD0) as a named no-op.

Files: new `internal/access/btree/pgdelete.go` (`PostingUpdate`,
`SizeOfBtreeUpdate`, `ReplayDeletePage`, `ReplayPostingUpdates`,
`updatePostingRaw`), `internal/wal/{recovery.go,pg_xlog_decode.go}` (opcodes
0x70/0xD0, two dispatch arms, `replayDecodedXLogBtreeDelete`,
`decodeXLogBtreeDeletePayload`, `sizeOfXLogBtreeDeleteData`), new
`internal/wal/btree_delete_pg_test.go`, `internal/wal/reader_fail_closed_test.go`
(S16.3 probe 0xD0 → 0xF0), design `docs/design/0131-0015-*` (§"S21b part 3" +
matrix rows), `docs/design/README.md`, `.ralph/fix_plan.md`, one
`.ralph/deferral_ledger.md` row.

Three things to remember:
1. **Updates apply BEFORE deletions.** Both offset arrays are in the
   PRE-deletion coordinate space; deleting first shifts every later offset
   down. (The revert proves it: the update then lands on a plain tuple.)
2. **A single surviving TID is NOT a one-entry posting** — `_bt_update_posting`
   collapses it to a plain non-pivot tuple (alt-TID off, TID in `t_tid`, size
   back to `keysize`). Re-forming a posting panics `PGBTPostingRaw`.
3. **Sibling path:** `xl_btree_vacuum` shares the payload AND the page work; its
   `nupdated > 0` refusal died in the same slice. Both now share
   `decodeXLogBtreeDeletePayload` + `ReplayDeletePage`.

RM_BTREE's opcode space is now complete — every nbtxlog.h value has a named arm,
so the S16.3 fallback is reachable only via an undefined info value (0xF0).

7 guards, proven fail-when-broken by 3 scripted reverts (order swap → 2 FAIL,
no collapse → 1 FAIL, update decode dropped → 4 FAIL).

Gates: `internal/wal` + `internal/access/btree` + `internal/storage` PASS,
`-race` on the Btree/Posting/Dedup/Delete subset PASS, UNITS precommit PASS
(warm cache), pgbench smoke via the commit hook, `make ralph-state-guard` OK
(auto-repaired the previous loop's completed marker).

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
`## AI-` items already filed under M-NIGHTLY, nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): S21b is closed — take the
next M0131 item, **S22** (CLOG replay opcode dispatch + commit-record
`subxacts[]` parsing; `internal/wal` second pass, see the fix_plan entry).

In-flight: none.
