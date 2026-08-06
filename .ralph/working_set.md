(idle — nothing in flight)

Last loop: **M-NIGHTLY `regress/suite-wedge` — ROOT CAUSE FOUND AND FIXED.**
The wedge is a page content latch **stranded by a recovered panic**.

Found from a LIVE SPECIMEN, not a re-run: an orphaned regress-suite goopg
server (PPID 1, data dir already deleted, ignoring SIGTERM) whose goroutine
dump showed the **checkpointer blocked 10 min** on a slot's shared content
latch that **no live goroutine held**. Its own log — `/tmp/goopg_cluster_debug/
regress_suite.log`, which survives the test TempDir cleanup — named the owner
at the same minute: recovered backend panic `storage: not enough free space in
page` in `insertItemSorted` ← `insertIntoBlock` ← `Insert` ←
`maintainUniqueIndexesForInsert`. `insertIntoBlock` releases its `pinW` latch
by explicit `unpinW`, never a defer, so a panic strands it; `internal/server`
recovers every backend panic (server.go:~799) so the owner goroutine vanishes.

Explains the whole signature: statement hangs past `statement_timeout` (mutex
waits observe no deadline), server otherwise fast (one poisoned page), wedge
case MOVES, and shutdown `FlushAll` wants the same latch ⇒ **the orphan crawl
is the same defect**. PG's `LWLockReleaseAll()` prevents this class; goopg has
no equivalent.

Landed: local idempotent `wlatch` holder (`internal/access/btree/
latch_release.go`) + `defer held.release()` in `insertIntoBlock` (11 normal
exits converted); `sibSlot`'s 2 hand-written Unlock+Unpin pairs → `unpinW`.
A registry-on-`*BTree` design was FALSIFIED first (`*BTree` is NOT
goroutine-private — `TestConcurrentSearchAfterInserts` deadlocked; callers also
latch outside entry points).

Files: `internal/access/btree/{latch_release.go,latch_release_test.go,btree.go}`;
`docs/design/root-0040-btree-stranded-latch-release.md` + README index;
fix_plan (wedge item resolution + NEW trigger item); 2 ledger rows;
`analysis/m0127-s7-regress/orphan-3051493/goroutines.txt` (the evidence).

Gates run: new guards PASS, non-vacuity PROVEN (removing only the defer makes
`TestStrandedLatchReleasedOnPanic` HANG — the wedge in miniature); full
`internal/access/btree` PASS 2.5 s; units gate PASS; `tpch-spotcheck.sh` PASS
(Q12=2, Q13=35); `TestPort_RegressSuite` PASS 689 s; pgbench smoke via hook.

NEXT LOOP (banner: M0124 closed → M0125 closed → **M0127** → M-NIGHTLY →
M0123). Run `make nightly-batch` to CONFIRM: a clean cycle with no
`suite-wedge` item closes both the wedge item and M0127's S7 bar. If a wedge
still appears, the probe landed two loops ago prints `pg_stat_activity` + the
long-blocked goroutine stacks into `testport/go-test.log` — read those, and
also check `/tmp/goopg_cluster_debug/<cluster>.log` for a recovered panic at
an UNPROTECTED latch site (descent RLocks, split rightSlot/sibSlot, or the
non-btree `Slot.Lock()` sites in internal/executor — all still exposed, ledger
row). The separate NEW trigger item (`pageHasSpaceFor` vs `insertItemSorted`
disagree about page space) is a correctness task, not a gate blocker.

In-flight: none.
