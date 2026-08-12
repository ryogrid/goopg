(idle — nothing in flight)

M0131-S25 LANDED (loop #173) — the index-AM boundary.

Files: new `internal/wal/index_am_refusal.go` (`indexAMName`, `indexAMOpcode`,
`indexAMUnsupportedError`, `preflightIndexAMRecords`), `internal/wal/xlog_record.go`
(RmgrHash 12 / RmgrGin 13 / RmgrGist 14 — 12..14 had NO names before),
`internal/wal/recovery.go` (five-rmid arm + the pre-flight call in
`ReplayRecordsFrom`), new `internal/wal/index_am_refusal_pg_test.go`,
`internal/wal/reader_fail_closed_test.go` (both S16.1 lists now cover all five
AMs), design `0131-0015` §S25 + acceptance item 14, README index,
fix_plan S25 checked, 2 ledger rows.

Worth carrying:
- No FPI shortcut can ever work for these five, and that is the reusable fact:
  `REGBUF_WILL_INIT` blocks structurally carry no image (23 sites across the
  five AMs), and GiST's `gistRedoClearFollowRight` (gistxlog.c:47-54) mutates
  the page on `BLK_RESTORED` too because the NSN is not in the image. So when
  GiST redo is eventually written, "image restored, done" is a WRONG index, not
  a shortcut. Ledger row 2 carries the assertion to write then.
- BRIN's 0x80 is `XLOG_BRIN_INIT_PAGE`, a FLAG — mask with `XLOG_BRIN_OPMASK`
  (0x70) before naming the opcode. Same trap shape as RM_HEAP2's 0x70 mask.
- Measured, correcting the plan's estimates: hash 1158/13, GIN 813/9,
  GiST 695/6 (plan said 400), SP-GiST 1009/8, BRIN 367/6.
- Pre-flight vs per-record is about the DIRECTORY, not the message: the
  per-record arm applies the prefix first, leaving a half-advanced $PGDATA the
  operator has to take back to PostgreSQL. The test proves the "nothing
  written" half non-vacuously by running the same prefix alone first.

Gates: UNITS precommit PASS (all cached/ok), `internal/wal` full package PASS
(6.2s), `-race` on the touched wal tests PASS, `internal/initdb` + 
`internal/storage` PASS, `TestE2E_GoopgCrashStartOnPGDataDir` +
`TestE2E_GoopgColdStartOnPGDataDir` PASS (`-count=1`), pgbench smoke via the
commit hook, `make ralph-state-guard` OK. Fail-when-broken proven by scripted
revert on BOTH directions (drop the BRIN mask → 0xa0 reported; drop the
pre-flight call → Applied=1).

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
`## AI-` items already filed under M-NIGHTLY, nothing new.

Next loop (banner = M-NIGHTLY filing, then M0131): S24 (MultiXact) and S29 are
the large open ones; the two S21g ledger rows (new_xmax, ALL_VISIBLE_CLEARED)
are still small and unclaimed.

In-flight: none.
