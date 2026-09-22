(idle — nothing in flight)

# Loop #89 — M0145-0022 LANDED (join-method election channel, report-only)

M0145-0004's residual stays BLOCKED (loop #88), so per the banner — which says
the harness additions 0021/0022/0023 "may run any time" — this loop took
**M0145-0022** (`Parent: none`, so no lineage issue). Movement: none.
Design: `docs/design/0100-0149/m0145-0022-join-method-election-channel.md`.

## Half the task already existed — checking first IS the finding
The wall-clock threshold report has been in `scripts/tpcds-sweep-diff.py` all
along: `STATUS-DELTA … runtime-moves=N … total-delta=±X%`, >=2.0x per query,
5 s floor, TIMEOUT readings excluded. Only the election half was missing.

## What landed
`scripts/tpcds-plan-diff.py`: counts join nodes per family (nestloop/hash/
merge) over already-changed queries, prints only the DELTA, names a move
**into** Nested Loop a suspect (C-04a's signature). Report-only — the exit
status stays `--strict`'s alone.
```
# join-method: Q6 hash+4 nestloop-4
=== JOIN-METHOD-ELECTION: moved=4 into-nestloop=0 ===
```

## It RETRO-VALIDATES M0145-0019a
Over the capture pair bracketing the LIMIT-fraction gate: `moved=4
into-nestloop=0` (Q6/Q8/Q43/Q44 each `hash+N nestloop-N`) — the exact
direction that fix should produce, confirmed independently of its reasoning.
Against an 11-day-old baseline: `moved=43 into-nestloop=25` (not always zero).

## ⚠ TRAP — the first test draft passed VACUOUSLY
Fixtures used `=== Q1`; captures write `===== Q1 =====`. The tool parsed ZERO
blocks and 3 of 7 tests were green for no reason. Same off-by-two as the awk
range in the set-op common-type work. Fixture writer now carries a comment.

## Gates
Real SF0.25 sweep with the channel in: `PASS=96 MISMATCH=0 CKMISMATCH=0
ERROR=0 TIMEOUT=0`, `PLAN-SHAPE same=99 changed=0`, gate-stamp PASS; channel
correctly silent when nothing moved. `tpcds-plan-diff-test.py` 7/7,
non-vacuity checked. state guard OK; pgbench smoke via the hook.
No `internal/`/`cmd/` diff, so no engine gate stamps are implicated.

## Next loop
**M0145-0023** (flow-convergence instrument) or **M0145-0021** (SF1 fire-set
gate template) — both `may run any time` per the banner. M0145-0005 is the
banner's next chain item but it is an EPIC (retires Phase A/B, the pinned
spine, `runJoinSearchBelowPinned`) — slice it before starting.

## Owner escalations — FIVE open, unchanged
1. partition_aggregate inventory row. 2. template1 collision (A vs B).
3. M0145-0018 (option re-take + criterion-1 waive). 4. M0145-0012 behind
M0145-0020a. 5. lineage baseline re-pin + permission for the one-line hoist
trace in `addAppendRelPartialPaths` (C1 impl task) — the M0145-0004 unblock.
