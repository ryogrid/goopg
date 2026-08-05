(idle — nothing in flight)

Last loop: **M0127-P5.9-f CLOSED**. Facts the next loop must NOT re-derive:

1. **P5.9-f was TWO defects, both now fixed and pinned.** (a)
   `buildBindingsPosMap`'s `collect` descended into a join-input `*Aggregate`
   while `applyJoinTreePosMap` has always stopped there — with the flag ON the
   searched outer side records no entries, so the decorrelated HashAggregate's
   lineitem CLONE became the only `lineitem` entry (offset 25) and the residual
   `l_quantity/4` became `/29`. Fixed as an opaque leaf. (b) With the remap
   correctly declining, `reresolveJoinByName` stopped running and Q17 returned
   **0 rows**: `unnestSubquery` built the splice's `RightKey` at the
   inner-relative `0` while `Predicate`/`LeftKey` used merged coordinates.
   Fixed at `outerWidth`. Do NOT "simplify" either back.
2. **The reproducer is ~1 s, not the 28 s SF1 arm** — a 3000-row lineitem /
   200-row part fixture on a throwaway 5533 cluster; recipe in
   `analysis/leftdeep-joins/p59f/README.md`. Note the fixture trap recorded
   there: `l_quantity` must NOT be a function of `l_partkey` or nothing matches
   and the fixture cannot fail.
3. **P5.9's full bar re-run is UNBLOCKED and is M0127's next action.** Run 1's
   remaining clause-1 counts (Q2 0 rows; Q7/Q8/Q9 `42883`; Q5/Q10 timing) were
   all attributed to the rotation P5.9-c fixed — **re-measure before
   re-diagnosing any of them.** Protocol: `NO_BUILD=1 PGSHAPED=0|1
   scripts/tpch-acceptance-arm.sh <name> <out>` holding ONE binary across arms,
   then `tmp/tpch-acceptance-runner -diff <off> <on>` (clause 1 is values, not
   row counts).
4. Two ledger rows filed. The generalisations deliberately NOT done: the
   twin-walker node-set agreement (`collect` vs `applyJoinTreePosMap` — third
   divergence found) and an audit of the nine `&Join{}` construction sites for
   keys that depend on `reresolveJoinByName`'s by-name repair.
5. Incidental, unrelated discovery filed as **M0119-0010**: `char(N)` typmods
   are not restored per column on catalog reload (same INSERT succeeds before a
   restart, `ERROR: value too long for type character(1)` after, while `\d`
   still reports the right types). Three-statement repro in the same README.

Files: `internal/planner/bushy.go`, `internal/planner/unnest.go`,
`internal/planner/joinsearchunnest_test.go` (new).
Docs: leftdeep-joins/09 §5.21 (new), bundle README status, IMPLEMENTATION-TODO
P5.9-f, fix_plan P5.9-f + parent + M0119-0010, 2 ledger rows,
`analysis/leftdeep-joins/p59f/`.

Gates run: UNITS PASS; SPOT PASS (Q12 rows=2, Q13 rows=35, 28.9 s); **DS05 sweep
PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plan shapes 99/99 identical**
(both fixes change flag-OFF planning, so this was required, not optional);
Q17 arms ON/OFF on one binary → `tpch-runner -diff` **VERDICT: PASS**;
pgbench smoke via the commit hook; `make ralph-state-guard` (self-repaired).

Nightly triage 20260805-014309: unchanged run, both items already filed under
M-NIGHTLY, left unchecked per the banner.

Next step: **M0127-P5.9** — the full S5 acceptance bar re-run + flag flip.

In-flight: none.
