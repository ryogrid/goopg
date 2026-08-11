(idle — nothing in flight)

Loop #133 ROOT-CAUSED **M0131-S30.3** (diagnosis only — no engine change yet).
Committed docs + tracker updates; design `docs/design/0131-0021-s303-replay-page-identity-divergence.md`.

**Deterministic offline repro is PRESERVED**: `cp -a /tmp/s30_3_repro/data /tmp/try`
then start goopg on `/tmp/try` → refuses to start in ~35 s with
`wal replay: replay record 826236 lsn[154662577,154662656]: xlog heap-update add
new tuple: storage: not enough free space in page`. (Copy it somewhere durable if
/tmp is at risk; 251 MB.)

**Root cause (new, supersedes "the non-HOT replay arm is wrong"):** the record is
`HOT_UPDATE rel 1663/5/16407 (pgbench_tellers) blk 130, old_off 1, new_off 2` —
goopg's decode and real `pg_waldump` agree byte-for-byte. That page holds 185
line pointers, ALL LP_NORMAL, 28 bytes free, `pd_lsn=123846600` = exactly the last
record touching it (idx 707555, a DELETE), and NO record of any kind touches it for
the next ~120k records. `new_off: 2` is impossible there: `PageAddHeapTuple`
(`internal/storage/heap.go:537`) always appends at count+1 (never reuses a free
slot) and `VacuumHeapPageBySlots` never shrinks the LP array. So the runtime's page
had ONE line pointer — freshly `PageInit`-ed. It is a **page-identity** bug, not a
free-space/missing-prune bug; replay is faithful (pre- and post-prefix-replay page
images match).

**Ruled out — do not re-test:** missing prune records (they exist; rmid 9 = Heap2
`PRUNE_ON_ACCESS`, 161 on this rel — and note rmid 11 is **Btree**, not Heap2);
"a prune explains new_off=2" (it cannot); unlogged `XLOG_SMGR_TRUNCATE` in the
window (all 7 sit at idx ≤ 504370); a goopg decode bug; replay corrupting the page.

Next step: instrument `tryApplyHOTUpdate` (`internal/executor/operators_storage.go:3555`)
to log `{rel, blk, PageLinePointerCount, newSlot, pd_lsn}` at emit time plus a
tag/content consistency check at `storage.Pool.Pin`, then run the 30 s pgbench load —
NO crash needed; the divergence forms while running normally. Suspects: buffer-pool
tag/content aliasing; `IsNew`-driven silent `PageInit` past a shortened file.

Nightly triage: `ci/logs/action-items.md` still run `20260811-014635` (AI-…-001..012),
all already filed under M-NIGHTLY; nothing new.

Gates run: units suite PASS (warm cache); `make ralph-state-guard` OK after self-repair;
pgbench smoke via the commit hook. tpch-spotcheck not required (docs/tracker only).

Note: an unowned goopg server is listening on 127.0.0.1:5533 (pid 1510790, `goopg-sub`) —
not started by this loop; use 5536+ for throwaway servers until it is reaped.

In-flight: none.
