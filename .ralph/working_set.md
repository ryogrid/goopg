(idle — nothing in flight)

Loop #136 worked **M0131-S30.3 step (a)** — the probe fired and S30.3 is
**ROOT-CAUSED** (diagnosis committed; the FIX is the next loop's task).

**Root cause: two live buffer slots for one block.** `Pool.pinNewXID`
(`internal/storage/bufpool.go`) releases `pinMu` across `mgr.Extend`; the moment
Extend returns the block is inside `nblocks`, so another backend Pins it, misses
the bufmap and loads it into a SECOND slot — while the extender publishes its own
slot valid+dirty+pinned BEFORE `bmInsert`, and on `bmInsert` failure + failed
`tryPinSlot` **falls through keeping its own publication** (un-mapped dirty
duplicate). That stale near-empty page is a normal eviction candidate, overwrites
the real 185-tuple block, and every later HOT update through it emits
`new_off = 2, 3, 4 …` — exactly the S30.3 record.

**NEXT STEP (the fix slice):** publish the bufmap entry with `ioInflight` set
BEFORE releasing `pinMu` for the extend write (mirror `pinLoad`'s
publish-then-IO order so a concurrent Pin waits on the slot semaphore), and
delete the "fall through: keep our publication" branch — a loser must un-publish
(clear tag/state, `releaseVictimSlot`) and retry the lookup.
Gate: `bash analysis/pageident_probe.sh` ×3+ with zero
`PAGEIDENT-DUPSLOT`/`PAGEIDENT-EMIT-REGRESS` (it fires in ~3 of 4 runs today at
`LOADSEC=60 CLIENTS=24`), then `RUNS=3 bash analysis/crashprobe30.sh`.

**Repro is now cheap** — no crash, no preserved cluster: 60 s pgbench under
`GOOPG_PAGEIDENT_PROBE=1`. Probe additions this loop (all temporary, remove when
S30.3 closes): `PageIdentityAssertEmit`, `PageIdentityAssertCount`,
`PageIdentityReportDupSlot`, `pageIdentStack` (stack on first 4 regressions),
`Pool.probeAssertSlotIsMapped`, observe at every `MarkDirty*` with the slot tag.

Still open in M0131-S30: S30.1 / S30.2 / S30.4 (all untouched). Ledger row
2026-08-11 (HOT arm's unlogged xmax on a failed `PageSetHeapTupleCmax`) open.

Nightly triage: `ci/logs/action-items.md` still run `20260811-014635`
(AI-…-001..012), all already filed under M-NIGHTLY; nothing new.

Gates run: units suite PASS; `make ralph-state-guard` OK (auto-repaired progress
marker); pgbench smoke via the commit hook.

In-flight: none.
