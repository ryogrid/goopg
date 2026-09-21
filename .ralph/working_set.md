# Working set — inter-loop baton

Task: **M0145-0016 — DONE `[x]`.** The leaf reorder landed;
`semianti-not-tail` is 0 on the corpus and Q78 is oracle-verified.

## Banner — the escalation from loop 48 is STILL UNANSWERED

Item 3: `… 0016 [x] → 0017 → 0018`. **Next selectable: M0145-0017**
(never-reach census — sublinks outside top-level WHERE conjuncts).

The ordering question filed under M0145-0016 has had no owner response: by the
strict selection rule **M0145-0003 is the first `[ ]` in item 3** (as are 0004,
0005, 0007, 0008), yet loops 39-49 worked 0009 → 0016. The loop continues on
the standing assumption that 0003-0008 are umbrella items whose children are
the selectable work. **Still the banner's call, not the loop's.**

## What landed, and the one correction worth carrying

```
seam census SF0.25 knob arm:  semianti-not-tail 6 -> 0
  every other class unchanged; semianti-not-tail-with-pulled / -perm-desync 0
Q78 oracle-verified: 15 rows ck 4a8a89aae2584676 on PG 18.3 AND goopg both ways
                     4041 -> 3855 ms; elects parallel Gather + Finalize HashAgg
```

- **It was a small change** because the walk→problem index translation lives in
  ONE place: `remapWalkOrderFlatToSpans`' `problemIndex` closure, which already
  encoded the pulled-splice shift. The permutation composes there.
- **Column-neutrality is why that is safe**: `buildLeafSpans` assigns real
  spans in position order and synthetic spans out-of-band after them, so a
  STABLE partition preserves both relative orders — every `lo` unchanged, only
  the index holding it moves.
- **CORRECTION: this is NOT knob-arm-only.** I carried that assumption in and
  the gate caught it — `tryPGShapedJoinSearch` is the shared seam, so the
  DEFAULT arm moves too. SF0.25 reports `same=98 changed=1`, sole mover Q78,
  `PASS 15 rows ck=c06cf981a7819a37` against the git-tracked oracle,
  verdict-changes=none, runtime-moves=0.
- Cost-model note: Q78's estimated cost ROSE (Limit 4724 → 8184) while measured
  time fell — same estimate/measurement disagreement M0145-0013 recorded.
- Scope bound kept: partition-AND-pulled-leaves declines as
  `semianti-not-tail-with-pulled` (0 corpus fires). Ledgered with its resume
  point (three-way split).

## Next step

**M0145-0017** — never-reach census: sublinks outside top-level WHERE
conjuncts. Read the task text first; like 0015 it is likely measurement-first,
and 0015's `sublinkConjunctSite` (`<kind>@<position>`) is the instrument to
extend rather than rebuild.

## Traps carried forward

- **Re-read the banner every loop** (and see the unanswered escalation).
- A seam change is NOT pipeline-scoped — `tryPGShapedJoinSearch` is shared.
  Check the default-arm gate before claiming knob-arm-only.
- When `translateToLayout` refuses, print the columns that WERE available at
  that node before theorising about which mechanism owns the failure.
- A new hand-written Expr type switch fails `TestExprSwitchInventoryIsPinned`;
  pin it AND add a ledger row.

## Gates run

units PASS; tpch-spotcheck PASS (Q12=2 Q13=33); TPC-DS SF0.25 default arm
PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plans same=98 changed=1
(Q78, ck-verified), verdict-changes=none, runtime-moves=0; TPC-H acceptance arm
24 MATCH; the commit hook's smoke runs on commit.

## In-flight

none
