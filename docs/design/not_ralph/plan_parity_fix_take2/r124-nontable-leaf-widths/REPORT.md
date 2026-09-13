# R124 result — the mixed-pair bucket goes 42,679 → 0, but the round changes NO plan and NO cost anywhere. The confound was largely a LABEL. Plus: R120's pairing hypothesis is refuted.

Review BLOCKed the first draft on two findings and both stand. The
corrected result is weaker and more interesting than what I first wrote.

**What is true:** the cut fires on all 32 target rels, the MIXED bucket
goes to zero, values hold, and nothing regresses.

**What I got wrong:** I claimed this made TPC-DS's null *readable* where
R122's was confounded. It does not. R124's code change produces **zero
plan and zero cost movement on either corpus** — the TPC-DS captures are
identical to R122's modulo a header line and the K18 tempfile names. The
null is being read off **the same bytes R122 produced**, so nothing about
the evidence base actually changed.

**Bonus result, and the most valuable thing here:** running R124's
narrowing together with R120's HashAgg width-currency arm — the pairing
R120's own report said was the missing piece — reproduces R120's result
*exactly*. The pairing hypothesis is **refuted** (§7).

## 1. The cut

`relNarrowedWidths` may now narrow a rel with **no per-column map**, when
`AvgVarBytes == 0` — i.e. when there is no statistic to lose.

On a level-1 search rel `AvgVarBytes` and `ColVarBytes` are assigned
together and only under the table guard (`joinsearch.go:403-411`), so a
non-table leaf arrives with both nil/0 and its **un-narrowed fallback is
already `ncols = full, avgVar = 0`**. Narrowing `ncols` while carrying a
literal `avgVar = 0` hands the model the same variable-payload figure it
already used and corrects only the column count: `EntryBytes` goes
`48*full + 24` → `48*kept + 24`. **No estimate is invented.**

The `AvgVarBytes != 0` guard stands: a relation-wide figure with no
per-column map means the statistic exists and cannot be attributed, so
the original fail-HIGH decline is correct there. 6 new pins (30 total).

## 2. Results

| # | bar | result | verdict |
|---|---|---|---|
| P0 | OFF bit-identical, both corpora | TPC-H byte-identical | **PASS** |
| P1 | arm (c) 32→0; REL-NARROW 590→622; guard never fires | **arm (c) = 0**; **REL-NARROW = 622** (590 `via=table` + **32 `via=r124-nontable`**); **zero guard hits** | **PASS, exactly** |
| P2 | full bucket vector: MIXED → near zero, denominator conserved, BOTH-UNNARROWED must NOT rise | **MIXED 42,679 → 0**; BOTH-UNNARROWED 8,588 → **8,345** (fell); JOIN-NARROW 73,349 → 116,271; **116,271 + 0 + 8,345 = 124,616**, the identical denominator | **PASS** |
| P3 | values unchanged | SF0.25 sweep stamped `GOOPG_NARROW_COST_INPUTS=1`: **PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3** | **PASS** |
| P4 | no regression; R122's gains hold | TPC-H **match 6, `join-method` 9, `scan-type` 8** — unchanged; TPC-DS match 2, no category rises | **PASS** |
| P5 | TPC-DS categories become readable; report either way | They did not move — but the "readable" half is **NOT established**: R124 moved no cost, so this is R122's evidence re-read, not new evidence (§3) | **FAIL as stated** |

Suites green (30 pins), `go vet` clean.

**The mixed-currency BUCKET is gone: 34.2% → 0.0%.** But see §3 before
reading that as a modelling improvement: for 22 of the 32 rels the two
sides of those comparisons already carried identical numbers, so what
was eliminated there is a census **label**, not a currency mismatch.

## 3. The cut fires — and changes nothing downstream (review B1)

The census is exactly as predicted, but the plans are not:

```
ds-r122on  vs ds-r124on   → 4 lines: the "# R122 ON→# R124 ON" header + 3 K18 tempfile names
ds-r122off vs ds-r124off  → the same 4 lines
r124on     vs r122on      → byte-identical (TPC-H)
```

**Not one cost number moved**, in 99 TPC-DS queries plus 22 TPC-H
queries. So "R122 could not read this… now it does" — the first draft's
central epistemic claim — is **withdrawn**. The categories in §2 are the
same numbers R122 already had.

### Why: measured, not guessed

The reviewer named two candidate explanations. Measuring `kept` vs
`full` over the 32 rels settles it — it is mostly the first, but not
entirely:

| | rels | note |
|---|---|---|
| `kept == full` — **numerically inert** | **22** | the cut only replaces the `NCols` zero-sentinel with the same count; `EntryBytes` is unchanged |
| `kept < full` — **genuinely narrower** | **10** | CTEScan 9→8, 8→7, 3→1; SetOp 4→3; and a **Project 144 → 28** |

So for 22 of 32, the "mixed currency" R122 and R123 spent two rounds
chasing was a **labelling artefact**: both sides of those comparisons
already carried identical numbers, and only one side carried the
sentinel that made the census call it mixed.

For the other 10 it was real narrowing — including one rel shedding 116
columns — and **that** is the part still unexplained: a 144 → 28
reduction that changes no cost anywhere. The honest statement is that
R124 removes a real labelling defect from the census and delivers real
narrowing on 10 rels whose effect does not reach any selected plan on
this corpus. Why it does not reach is **not established here**, and a
successor should not assume it is because narrowing "does not matter" —
it may be that those 10 rels sit in single-relation search problems with
no join above them.

## 4. TPC-DS plans DO change under the flag — and width DOES move join order (review B2)

Two corrections, both to claims that were convenient and wrong.

**(a) Three TPC-DS plans change SHAPE between the arms**, and the first
draft did not disclose it — R122 was forced to make exactly this
disclosure and I omitted it one round later:

- **Q6** — join order swaps (`customer_address ⋈ customer` →
  `date_dim ⋈ store_sales`), and the Memoize cache key moves from
  `ss_sold_date_sk` to `c_current_addr_sk`.
- **Q64** — the `income_band ib1` nested-loop spine is re-ordered
  (84 changed lines).
- **Q75** — `Hash Left Join` → `Nested Loop` over a `Hash Right Join`:
  a **join-method** change.
- Q95 — cost-only (a Hash Join `199292.16` → `53780.16`).

A shape change that leaves the category set untouched — because the
query already carried that category — is still a plan change, and the
category counter is structurally incapable of seeing it. (These are the
FLAG's effect, i.e. R122's Slice B; R124 itself adds none of them, §3.)

**(b) "`join-order` and `parallelism` are not width-driven" was false.**
`joinpathsparallel.go:210-213` consumes the width triple directly
(`outerWidth: pathWidth(o)`, `outerCols: pathNCols(o)`,
`outerAvgVarBytes: pathAvgVarBytes(o)`), and (a) shows width changes
moving join order and join method on real TPC-DS queries.

The defensible claim is weaker and is the one the evidence supports:
**width changes do move TPC-DS join order and join method — they just do
not move them toward PG.**

## 4a. Is the chain worth finishing for TPC-DS?

**Still no, but for the corrected reason.** Not "narrowing cannot touch
these categories" — it can, and does. Rather: with the chain complete
and the confound bucket at zero, TPC-DS gains **zero categories and zero
matches**, while its dominant divergences (`join-order` 90,
`parallelism` 86) remain where they were. Narrowing reshuffles TPC-DS
plans without moving them toward PG.

Recommendation: **stop extending this chain for TPC-DS.** In particular
do not build the per-column width basis for CTE/SetOp outputs that R123
floated — §3 shows 22 of 32 rels were already at full-width parity and
the 10 real narrowings moved nothing, so the larger version of that work
has no measured upside here.

## 5. Known, accepted divergence (review N1)

Narrowing these rels means their `avgVar = 0` now propagates into join
sums via R122's Slice B, where the rel previously declined. For a build
side `{CTEScan, t}` the executor's `buildAvgVarBytes` declines to the
whole-relation sum for the unattributable CTE column, while the planner
now publishes `0 + kept-bytes(t)` — **below** what the executor charges.

Before this round the join path declined too, so planner and executor
agreed; they no longer do. This is **cost-only** — these `Path` fields
never reach the executor, which sizes from `plan.AvgVarBytes` over
**rel** fields (`createplanjoin.go:572` → `operators_join_agg.go:796`) —
and it is **knowingly accepted, pinned** by
`TestR124AcceptedAvgVarDivergenceOnNonTableChild`, not silently
introduced. It is R120's defect in reverse reached by a different door,
and P3/P4 are what bound it.

**The pin was rewritten on review (N3).** Its first version asserted
`0 + 30 == 30` — just `narrowJoinWidths`' rule-2 sum, already covered
elsewhere; it built no non-table leaf and never called
`buildAvgVarBytes`, so it pinned nothing about the divergence it was
named for. It now calls `buildAvgVarBytes` directly, asserts that it
declines to the whole-relation sum for an unattributable column, and
asserts the strict inequality `planner < executor` that IS the
divergence — so if the gap ever closes, the pin fails and the report
must be updated.

The underlying under-statement (`avgVar = 0` for a leaf that really
emits text) is pre-existing on both arms for these rels and is **not**
fixed here.

## 6. Disposition

**Keep default-off.** The promotion question is now fully informed and
narrow: TPC-H gains two closed categories with zero regression and
byte-identical values; TPC-DS gains nothing and loses nothing. Promotion
is therefore a judgement about whether a TPC-H-only gain justifies
shipping a planner-cost change that also alters TPC-DS plan *costs*
(§3's categories are identical, but the underlying numbers moved), and
it should be taken deliberately rather than as a side effect of this
round.

R121's dead-code gate stays discharged (R122 landed; Slices B and D both
depend on Slice A).

## 7. R120's pairing hypothesis is REFUTED (review note 5)

R120 shipped its HashAgg width-currency arm as a "NEGATIVE result —
**pair with ncols narrowing**", and its promote-or-delete decision was
re-pointed (R120 REPORT §10) to a later round on exactly that reasoning.
R124 is that ncols narrowing for these rels, and the first draft never
ran the two together — while concluding the chain's TPC-DS question was
settled. The reviewer was right that this was premature; note also that
`cost_funcs.go:502`'s `armLive` becomes `inNcols > 0` under that flag,
so R120's arm is *specifically* the consumer designed for R124's output.

Ran now, TPC-DS SF0.25, both flags on:

| arm | GroupAgg | HashAgg | aggregation-strategy |
|---|---|---|---|
| narrowing only | 23 | 130 | 69 |
| **narrowing + R120 arm** | **29** | **123** | **71** |
| R120 arm alone (R120 REPORT) | 29 | 123 | 71 |
| PG | 126 | 38 | — |

**The paired result is identical to R120's arm alone.** Narrowing adds
nothing to it, and `aggregation-strategy` still worsens 69 → 71 exactly
as R120 measured. `scan-type` 57 → 56, `rendering` 20 → 21; match
unchanged at 2.

So the pairing hypothesis is answered: **no**. R120's flag does not
become correct once ncols is narrowed. Its promote-or-delete decision
can now be resolved on evidence — **delete**, rather than carried to yet
another round, since the condition it was waiting for has arrived and
changed nothing.

## 8. Disclosures the first draft omitted

- **P4's TPC-H half is vacuous, not a passed test** (N6). R123's TPC-H
  census shows all 4 rel declines are arm (a) and **zero** arm (c), so
  R124 has no eligible rel on TPC-H and cannot fire there.
  `r124on.plans.txt` is byte-identical to `r122on.plans.txt`. "TPC-H
  holds R122's gains" is true but is not evidence about R124.
- **The values sweep ran on the instrumented binary** (N7).
  `sweep-20260914-042720.txt` names `tmp/goopg-bench-bin`, which carried
  the census scaffolding. Substantively fine — `ds-r124cen` is identical
  to `ds-r124on` modulo header and tempfile, so the instrumented build
  is plan-identical — but R123 stated this check explicitly and R124
  did not. The sweep's non-blocking runtime channel also reported
  `Q22 2s→6s`, `Q58 2s→5s`, `TOTAL 184s→188s (+2.2%)` versus the R122
  sweep, plausibly census stderr I/O; the first draft cited only
  `PASS=96`.
- **P2's methodological clause was not met** (N1 of the review). The
  SCOPE required R123's counter shape with **zero-valued arms emitted
  explicitly**; the R124 counter dropped R123's MIXED detail fields and
  emitted no zero arms — the same defect R123 self-criticised. MIXED=0
  is nevertheless real (the reviewer verified the MIXED literal is
  present and live-linked in the census binary while `arm=c` is present
  with zero emissions), but that verification rests on
  `tmp/goopg-bench-bin`, which the bench scripts overwrite, and is not
  reproducible from the committed artefact.
- **A stale pin name was corrected** (N4): the decline subtest
  `"no ColVarBytes (un-ANALYZEd / subquery / CTE / VALUES)"` asserted
  the opposite of what R124 ships. It passes only because the fixture
  sets `AvgVarBytes = 999`, so it is the *unattributable-statistic*
  trip-wire, and is now named that.

## 9. Corrections carried from review

- **A statement in my own scope was false**: "AvgVarBytes and
  ColVarBytes are always assigned together". Five upper-rel sites assign
  the first alone (`upperrel.go:187`, `groupingpaths.go:145`,
  `distinctpaths.go:112`, `windowsetoppaths.go:141`, `:323`). True only
  for level-1 rels, which is all `relNarrowedWidths` sees — so the guard
  is a **live** trip-wire for any successor extending narrowing upward,
  and a firing guard there is correct behaviour, not a bug.
- The predicate keys on the **statistic, not the leaf kind**, so it also
  admits an un-ANALYZEd ordinary table. P1's exact counts assumed zero
  such rels, which held at this epoch (all 32 were `via=r124-nontable`
  and R123 measured zero stats-less tables).
- P2 was strengthened to the full bucket vector precisely so a rise in
  BOTH-UNNARROWED would be attributed rather than misread; it fell.

## 10. Artefacts

`census-tpcds.raw.gz` — the post-cut census (committed, as R123
established). Sweep with the arm stamped:
`sweep-20260914-042720.txt`. Captures (tmp): `r124off.plans.txt`,
`r124on.plans.txt`, `ds-r124off.plans.txt`, `ds-r124on.plans.txt`,
`ds-r124cen.plans.txt`.

`make plan-gate`: not run — **reasoned omission**, default-off flag plus
P0 bit-identity on the default path.
