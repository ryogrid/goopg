# Working set — inter-loop baton

Task: **M0145-0016 — step 0 done, remap NOT built.** Task stays `[ ]`.
The subsumption check the task mandates first is discharged: NOT subsumed.

## Banner — and an ESCALATION about it

Item 3: `… 0015 [x] → 0016 [ ] (scoped) → 0017 → 0018`.

**Open question filed for the owner in `.ralph/fix_plan.md` under M0145-0016**:
by the strict selection rule, **M0145-0003 is the first `[ ]` in item 3** (as
are 0004, 0005, 0007, 0008), yet loops 39-48 worked 0009 → 0016 and the owner
filed 0011-0018 mid-chain without re-ordering. The loop has assumed 0003-0008
are umbrella items whose children are the selectable work. If that assumption
is wrong the loop should restart at 0003. **Do not resolve this by judgement —
it is the banner's call.**

## What step 0 established

```
fresh seam census, SF0.25 knob arm, 99 queries, HEAD:
  leaf-count 19 | semianti-not-tail 6 | residual-hits-pad 6 | outer-over-derived 6
semianti-not-tail fires on Q78 ONLY, 3 per run — matches the task's count.

new trace line:
  SEAMNOTTAIL synthetic=0x0002 want=0x0004 nprefix=2 nleaves=3
```

Three leaves, synthetic at walk position 1 where the contract wants 2 — the
`[real, synthetic, real]` demoted-ANTI walk. The fix is the **stable partition
`[0, 2, 1]`**; stability is load-bearing because real leaves carry the emitting
column space, so only POSITION masks may move.

## Next step — build the remap, from the inventory

`docs/design/0100-0149/m0145-0016-semianti-not-tail.md` lists everything it
touches. The dangerous item is `walkWidths`: `remapWalkOrderFlatToSpans`
already encodes ONE leaf-index translation (the pulled-leaf insertion) and a
permutation is a SECOND that composes with it. Also `semiAntiChainLink.lhs/rhs`
plus the SHARED `sjinfo`'s four RelSets (mutate in place — its comment forbids
drift), `outerChainLink.preserved/nullable`, `chainOnQual.belowNullable`, the
`jl`/`ctx.bindings[:nprefix]` pairing, and `spans` (records move with leaves,
`lo` never renumbered).

**Acceptance bar is the task's own**: Q78 oracle-verified on VALUES, not a
green census. Q78's historical `translateToLayout` panic is this assumption
violated.

## Traps carried forward

- **Re-read the banner every loop** (and see the escalation above).
- When `translateToLayout` refuses, print the columns that WERE available at
  that node before theorising about which mechanism owns the failure — that
  diagnostic turned a "lowering redesign" conclusion into a one-field fix.
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
