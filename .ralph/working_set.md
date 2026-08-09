(idle — nothing in flight)

Last loop: M-NIGHTLY AI-20260810-011258-006 (pgbench TPC-B 40001 storm).
LOCALISED, not fixed — the fix_plan item stays unchecked.

Landed: WFG edge provenance behind `GOOPG_DEBUG_WFG=1` (off by default, one
bool read on the register path when off) in
`internal/executor/operators_storage.go`: `wfgDebug`, `wfgEdgeInfo`,
`wfgDebugTarget` + `wfgNoteTarget` (called at the two hot `epqWait` sites),
`wfgCallSite`, `dumpWFGCycle`. Records per waiting XID the edge age, the
`epqWait` call site, and the tuple blocked on (rel/blk/slot, xmin, xmax,
t_ctid, superseded flag, infomask); dumps the whole cycle on detection.
Driver: `analysis/wfg-tpcb-repro.sh` (untracked analysis area).

Evidence (2–12 false cycles per 60–120 s at s=10–20, c=100):
- every cycle is a 2-cycle, edges µs–ms old ⇒ nothing stale/leaked; the
  detector is correct, its INPUTS are wrong;
- both participants wait on the same relation, almost always the same block,
  at DIFFERENT slots — two versions in one hot page, not one contended tuple;
- the waited-on version is usually `superseded=true` (t_ctid → successor).
  goopg blocks on the xmax of whatever version the scan landed on; upstream
  re-enters EvalPlanQual on the chain HEAD (EvalPlanQualFetch / heap_lock_tuple
  t_ctid walk), so it always blocks on the current holder.
Two extra discoveries, both wrong-answer class, ledgered separately: waiters
have usually already stamped a version of their own in the same page (one
UPDATE may apply `bbalance + :delta` twice — no gate asserts the TPC-B
balance invariant), and some waited-on versions carry xmax OLDER than their
own xmin (impossible stamp; M0090-0002's race is not fully closed).

Next step: at both hot `epqWait` sites, walk t_ctid to the chain head before
computing xmax / registering the WFG edge (`epqFollowHOT` walks, but only
after the wait). Gate: `SCALE=10 T=120 REPO_ROOT=$PWD bash
analysis/wfg-tpcb-repro.sh` → 0 `WFG deadlock` lines, plus the isolation
suite (the wait target changes for every UPDATE/DELETE/MERGE conflict path).

Gates run: `go test ./internal/executor/` PASS (5.8 s); units precommit PASS
(cached); 4× pgbench repro runs (s=10/20, c=100, T=60–120); commit-hook
pgbench smoke; `make ralph-state-guard` OK (auto-repaired progress marker).

NEXT LOOP (state, not authority — re-read the `## Current Priority` banner).
Ledger: 2 rows. Design: follow-up in
`docs/design/0099-0003-deadlock-safe-conflict-waiting.md` (+ README row).

In-flight: none.
