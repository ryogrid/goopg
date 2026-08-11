(idle — nothing in flight)

Loop #137 CLOSED **M0131-S30.3** (fix + guard + docs committed).

**Fix:** `Pool.pinNewXID` (`internal/storage/bufpool.go`) no longer keeps its own
valid+dirty publication when it loses the post-`Extend` `bmInsert` race. The
loser un-publishes (`s.tag = BufferTag{}` + `releaseVictimSlot`) and re-acquires
via `Pool.Pin` — unlike `tryPinSlot` it waits out the winner's `ioInflight`
read instead of refusing it, and that refusal was the exact trigger. The filed
"publish the tag before Extend" half was NOT possible: the block number does not
exist until `Extend` returns. Step-1's early publication is unreachable
(`pinMu` held, not in bufmap, `claimVictim` skips it as pinned), so removing the
fall-through was sufficient.

**Guard:** `TestPinNewLosingExtenderDoesNotKeepDuplicateSlot`
(`internal/storage/bufpool_extendrace_test.go`) — racing `Pin` launched from
`OnExtendDone`, parked in its read via `OnPinWait`. Verified it FAILS on stashed
pre-fix bufpool.go and passes after, incl. `-race`.

**Gates:** pageident probe ×3 at `LOADSEC=60 CLIENTS=24` → ZERO `PAGEIDENT-*`
(was ~3 of 4 runs); units suite PASS; storage/executor PASS; ralph-state-guard
OK (auto-repaired progress marker); pgbench smoke via commit hook.
`RUNS=3 bash analysis/crashprobe30.sh` still FAILs — expected, that is the
S30.1/S30.2 WAL-tail-discard signature (`docs/design/0131-0020`), NOT S30.3.
Run 2 lost 21334 rows; runs 1/3 kept all rows but broke the
sum(abalance)==sum(delta) atomicity invariant.

**NEXT LOOP (per the M0131 banner):** M0131-S30.1 / S30.2 — crash recovery
discards the WAL tail. Start from `docs/design/0131-0020`. S30.4 also untouched.
Ledger rows open: 2026-08-11 no relation-extension lock (+ probe removal when
S30 closes); 2026-08-11 HOT arm's unlogged xmax on a failed
`PageSetHeapTupleCmax`.

Nightly triage: `ci/logs/action-items.md` still run `20260811-014635`
(AI-…-001..012), all already filed under M-NIGHTLY; nothing new.

In-flight: none.
