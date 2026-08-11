(idle — nothing in flight)

Loop #135 worked **M0131-S30.5** — CLOSED and committed.

**Fix:** `PageRemoveHeapTuple` (`internal/storage/heap.go`) is now an exact undo of
`PageAddHeapTuple`: when the removed slot is the LAST line pointer and its body sits
exactly at `pd_upper`, `pd_upper` is raised back over the body (MAXALIGNed) in
addition to the existing `pd_lower` shrink. Rationale that neither filed candidate
had: the orphan-cleanup add is ALSO unlogged, so the add/remove pair needs no WAL
record — it needs to be a page no-op, and it was not (it leaked one tuple's free
space per occurrence out of a page the WAL says still has it). Interior removals
unchanged. Guards: `TestPageRemoveHeapTupleUndoesAppend` (verified FAILS pre-fix,
`pd_upper=8056 want=8096`) / `TestPageRemoveHeapTupleInteriorSlotKeepsUpper`.

**Still open in M0131-S30 (next candidates, in order):**
- **S30.3** — the big one (`185 -> 1` line pointers). Prime suspects 1 (buffer
  tag/content aliasing) and 2 (`IsNew` PageInit past a shortened file) are REFUTED,
  do not re-test. Probe exists: `internal/storage/pageident_probe.go`
  (`GOOPG_PAGEIDENT_PROBE=1`, driver `analysis/pageident_probe.sh`); heap-only
  filter is `pd_special == BlockSize` (without it btree splits give 336 benign
  hits). Remaining candidates: (a) assert at `markHeapHotUpdateDirty` that the
  emitted `new_off` equals BOTH `newSlot` and `PageLinePointerCount(page)` — catch
  an emit-time inconsistency; (b) if clean, walk block 130's records from the START
  of the stream (divergence may begin far before record 826236).
- S30.1 / S30.2 / S30.4 (WAL-tail-vs-segment-padding, WARN-that-still-starts,
  no-checkpoint) — all untouched.
- Ledger row 2026-08-11: same arm still leaves an unlogged OLD-slot mutation when
  `PageSetHeapTupleCmax` fails after a successful multixact stamp.

Repro for S30.3 still preserved: `cp -a /tmp/s30_3_repro/data /tmp/try` → goopg
refuses to start in ~35 s (251 MB; copy somewhere durable if /tmp is at risk).

Nightly triage: `ci/logs/action-items.md` still run `20260811-014635`
(AI-…-001..012), all already filed under M-NIGHTLY; nothing new.

Gates run: units suite PASS; storage package tests PASS (new guards verified to fail
pre-fix); `make ralph-state-guard` OK; pgbench smoke via the commit hook.

In-flight: none.
