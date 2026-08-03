(idle — nothing in flight)

M0127-P2.1 is CLOSED (loop #48, 2026-08-03). S2 is OPEN.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P2.2` (executor composite keys: all-int64
fixed-width pack; mixed → concatenated `datumKey`; DELETE
`reselectDegenerateHashKeys` + its planner pass in the SAME commit; add a
Q78-class degeneracy regression test). P2.1/P2.2 are the SIBLING PAIR —
Rule #2. Bar: UNITS + SPOT + DS05 + SIBLING (planner keys ↔ executor key
encode).**

Carry-over facts a next loop should not re-derive:

- **P2.1 landed the planner half only, and deliberately inert.**
  `planner.Join.HashKeys []JoinKeyPair` (`internal/planner/join_hash_keys.go`)
  publishes every usable equi-pair; `HashKeys[0]` IS `(LeftKey, RightKey)`
  **by pointer**; extras are cloned ColumnRefs. Filled by ONE late pass
  `fillJoinHashKeys(node)` at the very tail of `Plan()` (after
  `lowerSubPlanParams`), because six earlier passes rewrite key/predicate
  exprs in place. Empty list ⇒ every consumer falls back to the single pair.
- **`Join.Predicate` still carries the equi-conjuncts.** `(*Join).Residual()`
  is the non-equijoin PROJECTION. P2.2 must (a) key on the whole list and
  (b) switch `joinPredicateMatchSlot`'s input from `o.plan.Predicate` to
  `o.plan.Residual()` — in that order, in one commit. Ledger row
  `2026-08-03 M0127-P2.1` is discharged by that.
- **EXPLAIN now emits `Hash Cond:` / `Merge Cond:`** (`formatJoinKeyCond`,
  `internal/executor/operators_explain.go`). New plan baseline
  `plan_snapshots/m0127-p21-hashkeys.txt`; it is byte-identical to
  `m0125-0002-c7-after` once those lines are filtered out.
- **A new hand-written Expr type switch fails `TestExprSwitchInventoryIsPinned`**
  (M0125-0001 RC-1a gate). Use `walkExprRefs(e, scopeDescend, exprVisitor{...})`
  for sublink descent.
- **DS05 post-TIMEOUT restart hazard is still live** — the sweep dies after
  Q72 on a `systemd-run` scope-name collision. Recovery: `systemctl --user
  reset-failed`, then `QUERIES="$(seq 73 99)" … sweep` as a subset probe.
- **Do NOT `git stash`** in this tree (9 unrelated entries).
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is NEVER modified.
  Tracking = `docs/design/0127-pg-shaped-join-search.md` §6 + fix_plan
  checkbox + README index status.

Gates run this loop: UNITS PASS; SPOT PASS (Q12=2 / Q13=35, 15.6 s);
DS05 MISMATCH=0 / CKMISMATCH=0 / ERROR=0 across all 99; PLAN re-baselined
with zero plan-shape change; SMOKE via the commit hook; `make
ralph-state-guard` OK.

In-flight: none.
