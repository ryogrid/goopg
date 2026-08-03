(idle — nothing in flight)

M0125-0047 is CLOSED (loop #40, 2026-08-03), committed and pushed.
The comma-FROM join order was decided by Go's map-iteration
randomiser: `pickNextByEdge` ranked candidates while ranging over
`edges[j]` (a map) with a STRICT `<` on row count, so a tie kept
whichever candidate the map yielded first. Fix = compare FROM indices
last (a total order). Design
`docs/design/0125-0047-joinorder-tiebreak-determinism.md`, evidence
`analysis/m0125-0047/`.

NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). As of this loop it selects **`M0125-0013` (bookkeeping half)** —
the LAST of the three M0125 items M0127 waits on. It is a documentation
contradiction about Q47's 8.4x runtime, not engine work, but it **NEEDS
A QUIET HOST** (`pgrep -af run-nightly.sh` first). M0127 opens after it.

Carry-over facts a next loop should not re-derive:

- The plan-snapshot nondeterminism floor the last several loops worried
  about is now MEASURED AT ZERO for the join-order passes: 3 restarts x
  96 SF0.5 EXPLAINs byte-identical pairwise. before-vs-after is also
  96/96 identical, so **no plan snapshot needs re-pinning** and no
  earlier A/B is invalidated by this commit.
- The 10-restart probe (`analysis/m0125-0047/probe-q85-restarts.sh`) is
  UNDERPOWERED — the flip rate is ~10%, so 10 clean restarts happen by
  chance ~35% of the time. Do not cite "N restarts agreed" as a
  determinism gate without stating its power. The in-process unit test
  is the strong instrument: Go re-randomises map order on every
  `range`, so 200 iterations sample it 200x at zero restart cost.
- Determinism is NOT proven planner-wide — only the join-order passes
  were audited (`smallestUnused`, `orderByConnectivity` and the bushy DP
  were already deterministic). `TestPlanQ85IsDeterministic` is the
  harness shape to generalise to a corpus; M0127-P5.4's plan-shape
  ratchet is its consumer. Ledger row 2026-08-03.
- `planShapeString` (predp_test.go) CANNOT see alias-order defects: it
  renders scans as `x.Table.Name` and self-join aliases share one
  `*catalog.Table`. Use the reflective fingerprint in
  joinorder_determinism_test.go instead.
- One SF0.5 EXPLAIN capture arm costs 2m43s (96 queries);
  `analysis/m0125-0047/capture-plans.sh <arm> <binary>` is the driver.

Gates run this loop: full `internal/planner` green; 4 new determinism
guards proved to FAIL against the pre-fix body first; units precommit
PASS; `tpch-spotcheck.sh` RESULT=PASS (Q12=2 / 22.5 s, Q13=35 / 11.4 s);
SF0.5 EXPLAIN A/B 96/96 byte-identical across 3 fixed-binary restarts
AND before-vs-after; 10-restart Q85 probe 1/10 flip pre-fix, 0/10
post-fix; pgbench smoke via the commit hook; `make ralph-state-guard` OK.

In-flight: none.
