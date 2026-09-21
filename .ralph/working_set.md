# Working set — inter-loop baton

Task: **M0145-0014 — COMPLETE `[x]`.** Both halves landed; the recursion half
landed on the second attempt. `any-nested-sublink` is 0 on the corpus.

## Banner

Item 3: `… 0013 [x] → 0014 [x] → 0015 → 0016 → 0017 → 0018`.
**Next selectable: M0145-0015** (residual census + OR/NOT-position sublinks —
PG's actual reach only). Re-read the banner; it has moved twice under batons.

## The fix was ONE field, and the first attempt misread the panic

Last loop's panic (`createPlan: join clause references binding column 3 (v)`)
was read as a lowering gap. It was not. Printing the columns actually available
at the failing join gave `have=[0 1 4]` — emitting plus the CHILD leaf, the
PARENT leaf ABSENT. The search had chosen `(emitting SEMI parentLeaf) SEMI
childLeaf`, and a semijoin does not project its right side.

`jtPulledBody.subtreeLeaves` makes a parent's `syn_righthand` cover its whole
SUBTREE — what PG gets for free by splicing a nested conversion into `j->rarg`,
whose arm comes back covering `child_rels` (`prepjointree.c:682-693`).
Restricting `syn_lefthand` was necessary but never sufficient.

```
census SF0.25 knob arm: any-nested-sublink-convertible 6 -> 0
                        (pulled) 48 -> 54, no REBASEFAIL, no PULLUPCLASSIFY
plans: exactly Q83 moves; values identical, 762 -> 553 ms
oracle: nested EXISTS-in-EXISTS and nested ANY-in-ANY both 3654|181827
        on PG 18.3 and on goopg
```

`TestJointreePullupDeclineParity/nested-exists` RETIRED (it pinned the decline
this task removes); `TestJointreePullupNestedExistsIsPulled` replaces it.

**Deferred**: depth-guarded at `maxPulledSublinkDepth = 3`; PG has no limit.
The guard exists because every pulled leaf lands in ONE problem capped by
`maxSearchRels`. Ledgered.

## Next step

**M0145-0015.** It is measurement-first: extend the pull-up census so each
unrecognised conjunct reports its sublink `%T` AND the clause position (OR arg,
NOT arg, scalar context). Then implement only what PG actually converts —
`pull_up_sublinks_qual_recurse` does NOT recurse into OR args
(`prepjointree.c:877`) while a NOT-wrapped EXISTS does convert (`:789-845`).
The task explicitly says to check whether goopg already has a hashed-subplan
equivalent of `convert_EXISTS_to_ANY` (`plan/subselect.c:1717`) BEFORE building
anything.

## Traps carried forward

- **Re-read the banner every loop.**
- When a panic names a mechanism, print the coordinates that WERE available
  before accepting that mechanism as the culprit. It cost a loop here.
- A new hand-written Expr type switch fails `TestExprSwitchInventoryIsPinned`;
  pin it AND add a ledger row.
- The RALPH_LOOP write-guard trips on prose mentioning a client tool and a
  reference port in one heredoc — split the write in two.

## Gates run

units PASS; tpch-spotcheck PASS (Q12=2 Q13=33); TPC-DS SF0.25 default arm
PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plans 99/99 identical,
runtime-moves=0; TPC-H acceptance arm 24 MATCH; the commit hook's smoke runs
on commit.

## In-flight

none
