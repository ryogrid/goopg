# Working set — inter-loop baton

Task: **M0145-0013 — DONE `[x]`** (seam admission for pulled `*CTEScan`
leaves), knob arm only. Landed with the pricing residual reported, not hidden.

## BANNER MOVED under the previous baton — check it every loop

Commit `24d45abcd` (owner) extended item 3 with **M0145-0013 … 0018**, so the
previous baton's "item 3 is exhausted, go to item 4" was stale. Item 3's order
now ends `… 0012 [!] → 0013 [x] → 0014 → 0015 → 0016 → 0017 → 0018`.
**Next selectable: M0145-0014** (nested-sublink pull-up recursion,
`any-nested-sublink`, census 12 fires; PG recurses via
`pull_up_sublinks_qual_recurse`, `prepjointree.c:682-693`/`:736-747`/`:836-845`).

## What landed

Both consumer sites of the bare-`*SeqScan` invariant now route through
`seamLeafBinding` (admission) + `seamLeafRelInfo` (pricing routed on
`b.table`), so it is held in ONE place instead of three.

```
seam census, SF0.25 knob arm, vs a pre-change binary from a worktree at HEAD
  pulled-leaf-not-scan  19 -> 0      <- the target
  residual-hits-pad      0 -> 4      <- the NEW wall
  leaf-count 15/15  semianti-not-tail 6/6  outer-over-derived 6/6
  pull-up census unchanged

correctness: exactly Q14/Q23/Q95 move, all three VALUE-IDENTICAL
inertness:   with GOOPG_PULLUP_CTE_LEAF off, 0/100 plans move
```

**`*CTEScan` binds with `table == nil` deliberately** — `leafIsDerivedInput`
reads it, which is what holds the `outer-over-derived` firewall in force until
M0145-0018. `TestSeamLeafBindingAdmission` pins it against a future
"helpful" synthesis of a catalog.Table.

## The residual to carry

The unlocked plans are **mispriced**. Against the honest `leaf-off` knob-arm
baseline: Q23 −14%, Q14 +25%, **Q95 3.0x slower** (3004 → 9148 ms, estimated
cost 70693 → 1232685). Values identical, so pricing not correctness. This is
the first thing M0145-0018's "fresh E1 re-verification keeps the relaxed plans
clean" precondition will trip on. Ledgered.

Also carried: 4 new `residual-hits-pad` fires (unexamined — a different
invariant), and the `pulled`-suppression window is unchanged, not discharged.

## Next step

**M0145-0014.** Read the task text first: it names PG's recursion sites
exactly, and the correct-decline rule (a nested SCALAR sublink stays declined).
Expected movement `any-nested-sublink` 12 → 0 on the pull-up census. Knob arm.

## Traps carried forward

- **Re-read the banner every loop** — it grew M0145-0013..0018 under us.
- A/B against a pre-change binary: `git worktree add --detach /tmp/<x> HEAD`
  then build with `-o`; remove the worktree afterwards (`git worktree remove
  --force` + `prune`) — the repo already carries 11 stale ones.
- Flags are read once at process START — an A/B needs two server runs.
- Value gates cannot see a cardinality or pricing regression; check plan shape
  and the clock.

## Gates run

units PASS; tpch-spotcheck PASS (Q12=2 Q13=33); TPC-DS SF0.25 default arm
PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plans 99/99 identical,
runtime-moves=0; TPC-H acceptance arm 24 MATCH; pgbench smoke via hook.

## In-flight

none
