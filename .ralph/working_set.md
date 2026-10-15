Task: M0122-0010 — Concurrency: buffer pool & btree locking. Slice 6 (gap 8) landed in 32fe610c7 (pushed); the task
  stays [ ] for gap 5.
  - Splits and vacuum's page unlink take no tree-wide lock. Vacuum re-finds the parent by downlink after latching
    left/target/right (latchParentOf = _bt_getstackbuf inside _bt_lock_subtree_parent).
  - Only finishSplit (no production caller, latches parent before child) still calls lockStructural.
  - Stress tests: per-handle-mutex knob removed; odd rounds share one handle; no-lock 0/15, -race clean.
  - TestMultiWriterStress_M0055_Phase_C now uses a 256-slot pool (32 concurrent splits exhausted 32 slots).
  - Live: 2339 page unlinks alongside 2335 splits; index = heap before and after kill -9 recovery.
S9: exhausted at c3dcb0f5f (routing-20261014j) — STALE as of 2026-10-15: owner reloaded the SF0.25
  corpus (fillfactor-rebuilt indexes) AND realigned the TPC-H :65433 corpus to PG :65432's row set
  (fresh TPC-H arm captured at routing-realign-20261015: 12/22 match, 10 honest divergences;
  Q3/Q5/Q7/Q13/Q16/Q18/Q20/Q21/Q2/Q9 are the remaining records; Q5 also times out >900s executing its
  divergent plan). S9(1) corpus-freshness clause → re-capture before next replenishment pass.
ESCALATION (S2, awaiting owner), all filed without Parent: read-only txns not enforced; boolean predicate columns
  typed text; rolled-back DROP of partition/inheritance/CASCADE loses the table (catalog half); CREATE TABLE in a
  rolled-back savepoint survives.
HELD: the GUC registry task and every other M0122-0008 child (root [!] since 2026-10-12). Do not select them.
NEXT LOOP:
  1. Re-read the banner (S9 fresh unless new planner/executor commits).
  2. Item 10 → M0122-0010 gap 5: finish crash-left incomplete splits on contact.
     - PG: _bt_moveright (forupdate) / _bt_stepright / _bt_getstackbuf call _bt_finish_split when a page carries
       INCOMPLETE_SPLIT (nbtsearch.c, nbtinsert.c).
     - Rebuild finishSplit on getStackBuf with the left page held (child before parent), drop the metapage
       fallback, call it from the insert descent's move-right and from getStackBuf; then remove lockStructural,
       sharedSplitMu and Pool.BTreeStructuralLock (+ TestOpenedHandlesShareStructuralLock / stripe tests).
     - Test: leave a page with BTIncompleteSplit and no downlink (simulate a crash), then insert into/through it and
       require the downlink to appear and the flag to clear; kill -9 mid-split probe if feasible.
  - S9 two-strike count = 1 (0194).
Files: internal/access/nbtree/{btree.go (finishSplit, insertIntoBlock, getStackBuf, moveRight paths),
  btree_vacuum.go (latchParentOf)}, design 0055-0002 §4.2 slice 6, README row 251.
Key symbols: finishSplit, getStackBuf, insertUpperFinishChild, lockStructural, latchParentOf, acquireUnlinkPins.
Hypothesis/Findings:
  - Parent moves under vacuum are rare in the stress test (deletion yield ~550/10 rounds before and after re-find).
  - Free list is per handle (PG uses the shared FSM) — ledgered residual.
  - Next date is 2026-10-15 (fix_plan/ledger rows use it).
Next step: re-read the banner; then read finishSplit and the descent's move-right to plan gap 5.
Top-residual: n/a (census exhausted at c3dcb0f5f, still fresh)
Skip-rationale: n/a
Gates run: nbtree x3 (+ -race x3) PASS, stress no-lock 0/15, 21 isolation ports PASS, units rc=0, tpch-spotcheck PASS
  (Q12=2 Q13=33), sf025 PASS=99, pgbench smoke (hook), live kill -9 probe PASS.
In-flight: none.
Owner items: place the four S2 defects; decide the XLogInsert/XLogFlush rename (recommend won't-do); re-open or
  close M0122-0008; place M0122-0009a/b/c and the two filed follow-ups; the COSTTIE flips (Q4 SF0.25, Q52 SF1);
  the plan-shaping GUC defaults. (DONE 2026-10-15: SF0.25 reload for 0178 slice 2 — gate cleared; TPC-H
  corpus realign to PG row set — anchors re-pinned, OWNER DECISIONS 2026-10-15 (b).)
