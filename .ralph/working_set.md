(idle — nothing in flight)

Last loop: **`M0125-0002` STEP 0 landed** — the walker inventory, re-derived from
source and permanently gate-pinned. Banner's next-selection set was
{`-0002`, `-0005`, `-0003` stage 3}; stage 2's TIMED arm was NOT takeable
(`run-nightly.sh` PID 3541516 still held the host, load 5.0/9.9/12.4), and stage 3
shadows stage 2's measurement, so `-0002` was selected — whose own documented
STEP 0 is "re-derive the inventory FIRST or the task closes against a list that
was never right".

Fix: `internal/planner/exprwalk_inventory_test.go` — a go/ast census of every
type switch over an `Expr` in package `planner`, pinned as `exprSwitchInventory`.
No production code changed, so plan shape cannot move.

## Facts the next loop should NOT re-derive

- **The RC-1a class is 50 walkers, not 7 / 9 / 14.** 64 sites total: 2 exprwalk
  primitives, 50 recursive-and-incomplete (2–25 of 32 arms), 12 non-recursive
  classifiers. The seven were picked by MHJ/local-filter blast radius — sound
  for scoping a conversion, useless for sizing a class. `-0002` covers 8/50.
- **`remapByPosMap` is already complete (18 arms + 14 childless leaves = 32)**,
  so commit 1 of `-0002` really is the no-op re-base (from -0001's record).
- **Two identity fail-opens that COLLIDE, not no-op** → filed `M0125-0024`:
  `planExprContentKey`'s `default:` is `fmt.Sprintf("%T", e)` (type name alone ⇒
  distinct exprs of one unenumerated type share an aggregate STATE-SHARING key,
  M0097-0032's shipped-wrong-answer shape); `exprEqual` falls back to `%T%v`
  text compare ("pointer-safe only for primitives"), and the pair disagrees on
  whether `*ColumnRef.SourceTableIdx` participates.
- Each `-0002` conversion now closes by DELETING its pin; arm counts are
  comments, never assertions (pinning them makes band-aid arms look like work).
- A new `.go` file needs `gofmt -w` on THAT FILE ONLY (map-literal alignment);
  the go1.25/1.26 caveat is about pre-existing files.

## NEXT (banner order)

1. `M0125-0002` **commit 1**: `remapByPosMap` re-base onto `cloneExprRefs` +
   the missing `default:` (veto). Only commit that carries an empty-diff
   expectation; 0125-0001 D6's 26 remap pins already guard it.
2. `M0125-0003` stage 2's TIMED arm the moment the host is quiet (before
   stage 3, which shadows it). Then `-0005` / stage 3 / `M0125-0024`.
Owed independently, now six commits deep: one full 99-query SF0.5 gate run.

Gates run: `go build ./...` + `go vet ./internal/planner/` clean; units suite
PASS (planner 0.279s, rest cached); census gate proved to FAIL both directions
(unpinned probe walker; renamed pin) then restored green;
`make ralph-state-guard` (repaired stale completed marker); pgbench smoke via
the commit hook.

In-flight: none.
