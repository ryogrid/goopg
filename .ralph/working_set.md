# Working set — inter-loop baton

Task: **M0145-0018 — `[!]` NO-GO.** The firewall was NOT relaxed. The task's
own precondition caught a C-04a-class regression the owner's GO predates.

## Banner

Item 3: `… 0017 [x] → 0018 [!]`. With 0018 blocked, **item 3 has no selectable
`[ ]` left under the standing assumption**, so the next loop should move to
**item 4 (`M0141-S2a-fix2r`)** — BUT re-read the banner first, and see the
escalation below, which now matters more.

## TWO escalations, both needing the owner

1. **M0145-0018 is a no-go and the blocker is the COST MODEL.** Details below.
2. **The loop-48 ordering question is still unanswered.** By the strict
   selection rule **M0145-0003 is the first `[ ]` in item 3** (as are 0004,
   0005, 0007, 0008); loops 39-51 worked 0009 → 0018 on the assumption those
   are umbrella items. With 0018 now `[!]`, this question decides whether the
   loop returns to 0003 or moves to item 4. **It is the banner's call.**

## The no-go, measured

```
current fire set (SF0.25 knob arm): outer-over-derived 3/run — Q77 (2), Q78 (1)

SF0.25  ON vs OFF — CLEAN, reproduces loop 39 exactly
  only Q77/Q78 move | Q77 788->851 ms | Q78 3869->3931 ms | values identical
  Q78 join kinds IDENTICAL (3 Hash Anti, 2 Hash, 2 Hash Left) — no NL election

SF1     ON vs OFF — CATASTROPHIC
  Q77  5680 -> 5760 ms  (unchanged, ck 9bd1900a34ce55c5)
  Q78  29002 ms -> DID NOT FINISH in 1800 s
       Nested Loop Left Join (cost=5494.86..1147565.07 rows=5731)
       all three equi-conditions demoted to a Join Filter,
       10317-row outer over a cs CTE scan — the C-04a shape verbatim
```

**Why it differs from loop 40's SF1 evidence** (Q78 stayed a hash join then):
M0145-0013, -0014 and -0016 all landed in between, each letting more of Q78's
problem into the DP. The search now has a join-order choice it did not have and
takes it badly. Re-using the old numbers would have landed a 60x-plus SF1
regression behind a GREEN SF0.25 gate.

**The blocker is the cost model**, not statistics and not admission: the
estimates are already honest (M0145-0011 measured that the DECLINE is what
manufactured the epsilons), and the search still prefers a nested loop it
prices at 1.1M. Owner options in the design doc — keep the firewall as a
documented cost-model backstop; narrow it to the SHAPE (veto an NL path whose
inner is a derived input, rather than declining the whole problem); or fix NL
pricing for a derived inner and re-run this verification.

## Next step

Await the owner on both escalations. If work must continue meanwhile, item 4
(`M0141-S2a-fix2r`) is the next banner entry after item 3.

## Traps carried forward

- **Re-verify preconditions on the CURRENT tree.** This loop is the case in
  point: the same A/B, same scale, opposite verdict, eleven loops apart.
- `timeout N psql` kills only the client — the server keeps executing. Stop the
  server and confirm the port is free before moving on.
- Port 5560 is held by a PEER's server; do not touch it. Use 557x.
- A seam change is NOT pipeline-scoped — `tryPGShapedJoinSearch` is shared.

## Gates run

units PASS. No production code changed this loop (the relaxation was not
landed), so the planner corpus gates were not re-run. The commit hook's smoke
runs on commit.

## In-flight

none
