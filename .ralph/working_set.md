(idle — nothing in flight)

M0127-P1.3 is CLOSED (loop #47, 2026-08-03). S1 is CLOSED end to end:
P0.1 + P0.2 + P0.3 + P1.1 + P1.2 (code) + P1.3 (the exit measurement).

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P2.1` (`planner.Join.HashKeys
[]JoinKeyPair`: search/pushdown fills all equality conjuncts, residual
keeps non-equijoin only, EXPLAIN key-list rendering, plan-snapshot
re-baseline in the SAME commit). P2.1/P2.2 are a SIBLING PAIR (planner
keys ↔ executor key encode) — Rule #2. Bar: UNITS + SPOT + DS05 + PLAN
(snapshot diff reviewed).**

Carry-over facts a next loop should not re-derive:

- **P1.3 result:** clause (2) of the 09 §2 bar met outright — NO query is
  slower under S1 than pre-S1 HEAD (max 1.00); total 619.26 s → 360.82 s
  (0.58×; 0.73× of R0's 493.31 s). Clause (1): Q7 passes (0.74× R0);
  Q3/Q10/Q18 miss (1.98/2.51/1.26× R0), **attributed class (b) plan
  shape, inherited** — the two arms' EXPLAIN is BYTE-IDENTICAL, and
  pre-S1 HEAD alone is already 4.08/3.89/2.92× R0. Cause: MHJ
  de-selection between R0's HEAD and pre-S1 HEAD (R0 snapshot has 9
  `Multi-Way Hash Join` nodes; both arms today have zero). **Q3/Q10/Q18
  are now P5's named regression witnesses** — do NOT try to fix them
  before P5.3a/P5.6/P5.7. Evidence + 5 raw artefacts:
  `analysis/leftdeep-joins/2026-08-03-s1-ab.txt`.
- **R0 is a pinned historical number from HEAD `0459be86`, not a
  contemporaneous arm.** Its per-query times and the MHJ-era plans live in
  `analysis/cost-driven-second-try-200731/evidence/r0-baseline.txt` and
  `plan_snapshots/m0126-base.txt`. 09 §3 item 2 already allows S5 to use
  "the better of R0 and a contemporaneous integer-arm run".
- **Reusable A/B harness** (kept): `tmp/m0127-s1-ab-arm.sh <arm> <bin>
  <out>` and `tmp/m0127-explain-arm.sh` — fresh capped server on 65433,
  R0 protocol, 600 s per query. One arm ≈ 6–11 min. Build a pre-HEAD
  binary from a detached worktree (`git worktree add --detach`), never by
  checking out in place.
- **Arm order matters less than feared:** the warm pre-S1 replicate
  reproduced the cold one within 7.1 %. Still run the control.
- **`make race-gate` is GREEN** since P1.2 (`buildEnvInFlight` is a local
  now). Do NOT re-file it as a known-red baseline.
- **`VirtualSlot.Row()` returns a POOLED row** — worker-side retention
  needs `MaterializeForTransfer`.
- **Do NOT `git stash`** in this tree (9 unrelated entries).
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is NEVER
  modified — that includes its `IMPLEMENTATION-TODO.md`. Tracking =
  `docs/design/0127-pg-shaped-join-search.md` §6 + fix_plan checkbox +
  README index status.

Gates run this loop: the measurement itself (3 full TPC-H SF1 sweeps,
22/22 complete each, row counts identical across arms and to R0) + 2
EXPLAIN snapshots; pgbench smoke via the commit hook; `make
ralph-state-guard` OK (auto-repaired the previous loop's stale
completed marker). No code landed, so no UNITS/SPOT/DS05 were required.

In-flight: none.
