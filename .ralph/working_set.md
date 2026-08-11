(idle — nothing in flight)

M0131-S21b PART 1 LANDED (loop #163) — the three btree opcodes with no new
page primitive: `INSERT_UPPER` 0x10, `INSERT_META` 0x20, `META_CLEANUP` 0xE0.

Files: `internal/wal/{recovery.go,pg_xlog_decode.go}`, new
`internal/wal/btree_insert_upper_pg_test.go`, design
`docs/design/0131-0015-pg-wal-opcode-coverage.md` (§"S21b part 1" + matrix rows
0x10/0x20/0xE0 → H), `docs/design/README.md` index line, `.ralph/fix_plan.md`
S21 note, one `.ralph/deferral_ledger.md` row.

Key symbols: `replayDecodedXLogBtreeInsert(mgr, r, xlog, isleaf, ismeta)`, new
shared `replayDecodedXLogBtreeRestoreMeta(…, blockID, what)`, the three new
`case`s in `replayDecodedXLogRecord`'s `RmgrBtree` switch.

What landed: before this loop all three fell into RM_BTREE's `default:` and,
since S16.3, REFUSED the start unless every mutated block carried an FPI — and
a real PG index of >1 level emits INSERT_UPPER on essentially every root-ward
split. The replay function grew upstream's own arguments (`btree_xlog_insert`
is one function instantiated four ways) plus block 1's
`_bt_clear_incomplete_split` (REFUSED when absent, not skipped: `_bt_insertonpg`
registers `cbuf` unconditionally on the `!isleaf` path) and block 2's WILL_INIT
metapage. The block-0 image branch must NOT return early — upstream's
BLK_RESTORED skips block 0's mutation only and still reaches
`_bt_restore_meta`. `_bt_restore_meta` is now shared and parameterised by block
id (2 for INSERT_META/NEWROOT, **0** for META_CLEANUP); hard-coding it would
silently rebuild the wrong page.

Correction recorded in the doc + ledger: `REUSE_PAGE` 0xD0 was NOT recognised
in S21a-1 (that slice's six no-ops are all HEAP/HEAP2/XACT/STANDBY) — the
S21a-2 closing note was wrong; it still hits the `default:` refusal.

5 guards, ALL proven fail-when-broken by 4 scripted reverts (dropped child limb
→ 2 FAIL; early return on a block-0 image → 1 FAIL; META_CLEANUP on block 2 →
1 FAIL; all three dispatch arms removed → 5 FAIL).

Gates: `internal/wal` PASS + `-race` (Btree subset) PASS, `internal/storage` +
`internal/access/btree` PASS, UNITS precommit PASS, pgbench smoke via the
commit hook. `make ralph-state-guard` PASS.

Nightly triage: `ci/logs/action-items.md` still on run `20260812-005501`
(unchanged since loop #160); all 4 `## AI-` items already filed under
M-NIGHTLY, nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): **S21b part 2** —
`INSERT_POST` 0x50 + `DEDUP` 0x60. Both need posting-list WRITERS
(`_bt_swap_posting` / `_bt_form_posting`, `nbtdedup.c`); goopg's
`internal/access/btree/posting.go` only PARSES posting tuples today, so the new
primitive lands there with its own page-layout guards. Then part 3: `DELETE`
0x70 (the `xl_btree_update` item-array rewrite it shares with DEDUP via
`btree_xlog_updates`, `nbtxlog.c:557-597`) + `REUSE_PAGE` 0xD0 as a recognised
no-op.

In-flight: none.
