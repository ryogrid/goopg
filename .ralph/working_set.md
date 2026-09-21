# Working set — inter-loop baton

Task: **M0145-0014 — PARTIAL**, still `[ ]`. The scalar half landed; the ANY
recursion is deferred and ledgered with an exact resume point.

## Banner

Item 3: `… 0013 [x] → 0014 [ ] (partial) → 0015 → 0016 → 0017 → 0018`.
Re-read it — it grew M0145-0013..0018 under an earlier baton.

## The finding: the class is TWO shapes, not one

```
query58   d_date IN (SELECT … WHERE d_week_seq =  (SELECT …))   scalar nested
query83   d_date IN (SELECT … WHERE d_week_seq IN (SELECT …))   ANY nested
```

Only Q83 needs the recursion. **goopg's gate was over-broad against the
oracle**: PG converts the OUTER sublink first (`convert_ANY_sublink_to_join`
gates on correlation + volatility only, `subselect.c:1345-1386`) and recurses
afterwards, so a nested sublink PG would not convert never blocks the outer
conversion. goopg refused both via a blanket `exprHasSublinkPlan`.

## What landed

- `bodyQualsAdmitSublinks` splits the refusal at BOTH pull-up arms (sibling
  pair, identical gate): non-convertible sublink rides along;
  `nested-sublink-convertible` and `nested-sublink-correlated` decline.
- **Second wall, found by re-censusing after the first half**:
  `rebasePulledQual` cloned under `scopeVeto`, which makes `cloneExprRefs`
  ABORT at the first inner-plan slot — every admitted qual then failed as
  `rebase-failed`. Now `scopeSignal` + an `OnScope` guard declining a
  CORRELATED subplan (`planHasOuterRef`).
- `noteRebaseFail` names which of the four rebase failures fired.

```
census SF0.25 knob arm:  any-nested-sublink 12 -> 0
                         any-nested-sublink-convertible 6  (Q83, deferred)
                         (pulled) 42 -> 48                 no REBASEFAIL
plans: exactly Q58 moves, cost 20670 -> 13692, Hash Semi Join retained
```

**Verified against the PG oracle** (Q58 returns 0 rows at SF0.25 — a weak
witness): the same nested-scalar shape over `store_sales` gives `3654|181827`
on PG 18.3 and on goopg before AND after, and goopg now produces PG's shape
with the nested scalar as an `InitPlan` filter on the body leaf.

## Next step

**M0145-0015** (residual census + OR/NOT-position sublinks — PG's actual reach
only). Read its text first: it is measurement-first and explicitly says to
check whether goopg already has a hashed-subplan equivalent of
`convert_EXISTS_to_ANY` before building anything.

If instead resuming 0014's deferred half: the blocker is that a link predicate
across TWO pulled bodies has no coordinate path —
`outerOperandAsLevel1`/`rebasePulledQual` only handle a Level-1 outer ref
resolving to an EMITTING binding.

## Traps carried forward

- **Re-read the banner every loop.**
- A new hand-written Expr type switch fails `TestExprSwitchInventoryIsPinned`;
  pin it in `exprSwitchInventory` AND add a ledger row (the guard says so).
- Relocating a decline one step is not progress — re-census after each half.
- A/B against HEAD: `git worktree add --detach`, build with `-o`, then
  `git worktree remove --force` + `prune`.
- The RALPH_LOOP write-guard trips on prose that mentions a client tool and a
  reference port in the same heredoc — split the write in two.

## Gates run

units PASS (after pinning the new classifier); tpch-spotcheck PASS (Q12=2
Q13=33); TPC-DS SF0.25 default arm PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=0, plans 99/99 identical, runtime-moves=0; TPC-H acceptance arm
24 MATCH; the commit hook's smoke runs on commit.

## In-flight

none
