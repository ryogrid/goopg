# R129 recon — the bucket charge is parity-INERT. Do not scope it as a parity round.

Recon only: a one-line probe (`MapSlotBytes` 48 → 96), built to a temp
binary, measured, reverted. Tree clean throughout, nothing committed but
this document. Same pattern as the step-(d) FK recon that closed that
chain.

**Question:** R128 cut the bucket charge to a follow-up and left it a
clean baseline. Is that follow-up worth a round?

**Answer: no, not for parity.** Measured on the live TPC-H corpus with
R128's default-ON narrowing as the floor:

| | match | join-method | scan-type | Q14 |
|---|---|---|---|---|
| `MapSlotBytes = 48` (shipped) | 6 | 9 | 8 | **MATCH** |
| `MapSlotBytes = 96` (corrected) | 6 | 9 | 8 | **MATCH** |

**Plan shapes are IDENTICAL** — cost-stripped diff between the two
captures is empty. The constant moves no plan on TPC-H at all. Every
category is unchanged; `match` stays 6/22.

## The two things this recon settles

1. **The historical blocker is gone, confirmed on current code.** The
   ledger's coupling claim — "with [cost-side narrowing] applied, the
   bucket-charge patch no longer flips Q14" — was measured in 2026-09
   against `tmp/d05p3-costside-narrow.patch`, a *different*
   implementation (R128 §3). It now reproduces against the shipped
   default: **Q14 holds MATCH.** So anyone landing this for
   memory-accuracy reasons no longer inherits the Q14 regression that
   stopped it, and R128's worst-case-loses-a-MATCH argument for cutting
   it, while correct as a precaution, does not fire in practice.

2. **But there is no parity reason to land it.** Under a goal that counts
   only PG-likeness, an inert change is not progress. `MapSlotBytes = 48`
   remains genuinely wrong — documented "KNOWN 2x LOW … a hand-derived
   guess, not a measurement", against go1.25's measured 96.1 B for
   `map[string][]Row` — and correcting it is real work with real value
   (the ledger measured bucket heap 586.7 → 286.0 MB, per-worker peak
   −34.5%). That value is **memory accuracy, not plan parity**, so it
   belongs to the `minimize_datum` workstream that owns it, not to this
   one.

## Scoping fact for whoever does land it

**The preserved patch is stale and does NOT apply.**
`tmp/d05p2-bucket-charge.patch` fails against current HEAD:

```
error: patch failed: internal/executor/hashsize/hashsize.go:51
error: patch failed: internal/executor/join_batch.go:208
error: patch failed: internal/optimizer/pathtarget_test.go:1350
```

It is a 197-line, 5-file change from 2026-09-06 — not the one-constant
edit its name suggests — and it needs re-derivation, not replay. The
one-line constant probed here is only its core; the patch also carries a
`Choose` bucket charge, a `join_batch.publish` change and an
`entrywidth.go` edit whose interaction with R128's now-default narrowing
is **unmeasured**.

## Where this leaves the parity programme

TPC-H stays **6/22**. Adding this recon to the list of measured-and-
rejected levers (`../r127-semijoin-selectivity/FRONTIER.md` §4):

- rows-only on Q4 — refuted, R71
- semi-selectivity wiring on Q4 — bounded at 1.28x, R78
- FK evidence on Q9 — refuted, step-(d) recon
- cost-input narrowing — real but +2 categories, no flip, R128
- **bucket charge — inert, this recon**

The frontier is unchanged: Q4 and Q9 both block on **projection
pushdown / DatumBytes**, which does not exist. Every cheaper lever aimed
at the two closest queries has now been measured and rejected.
