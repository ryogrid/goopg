# 13 — How this bundle was produced, and what the panel agreed and disagreed about

This document is the provenance record. It exists because the *shape* of the agreement between
two independent passes is itself evidence, and a reader deciding how much to trust a given claim
in this bundle should be able to see whether one pass asserted it or two reached it
independently.

## Method

The same brief — the proposal in [README](README.md), plus the repository, plus a standing
instruction to verify every claim against source and to say so if the premise was wrong — was
given to **two independent design passes**. Neither saw the other's brief, work, or output. Each
was told to run its own adversarial review and fold the findings back. A third **synthesis pass**
then read both outputs cold, re-verified every point of disagreement (and every load-bearing
claim unique to one pass) directly against the tree, and merged them into this bundle.

No pass started a server or ran a benchmark: a TPC-DS SF0.5 sweep was occupying the machine and a
concurrent Ralph loop was editing `internal/planner/` and `internal/executor/` throughout. All
verification is static. The synthesis pass's transcript is
[evidence/judge-verifications-20260731.txt](evidence/judge-verifications-20260731.txt); the second
pass's independent chapters are retained at
[evidence/panelB-02-premise-audit.md](evidence/panelB-02-premise-audit.md) and
[evidence/panelB-10-agent-review.md](evidence/panelB-10-agent-review.md).

Numbers: pass A produced 13 documents / 2,304 lines and 5 SEV-1 review findings; pass B produced
11 documents / 2,148 lines, 14 review findings and 7 citation corrections. The overlap in
*conclusions* was much higher than the overlap in *evidence*, which is the useful property.

---

## Consensus — reached independently, therefore high confidence

Five conclusions were reached by both passes from cold starts, by different routes. These should
be treated as settled; review effort is better spent on the contested item below.

1. **The premise is right about PostgreSQL and wrong about goopg.** PG's `HashJoin` materialises
   only the inner side and streams the outer, so a left-deep cascade with the fact outermost is
   already an N-way pipelined probe. goopg's `joinOp` has the same *structure*
   (`drainRowsBounded(o.right, …)` at `operators_join_agg.go:645`; streaming `openProbeSide` at
   `:510-522`) but **re-materialises the probe input at the seam between two stacked joins**.

2. **The real cost is that seam, not operator-boundary overhead.** Both passes located the copy,
   both initially named the wrong line, and both corrected themselves the same way: on the live
   slab path the copy lives in `joinOpKernelNext` → `Slot.fillFromTupleSlot`
   (`opnode.go:869-876`, `:133-152`) and copies **twice**; on the legacy `Build` path it is
   `slotRow(probeSlot)` (`operators_join_agg.go:1214`) plus the `lazyKeyRow` memcpy (`:1216-1241`).
   MHJ avoids all of it by composing **one** `VirtualSlot` over N persistent per-table slots
   (`multi_hash_join.go:265-291`) — a *data-structure* advantage, not a *fusion* advantage.

3. **Therefore the correct first move is Stage 0: de-materialise the seam on both paths, then
   measure.** Building the fusion operator first is wrong. Both passes made Stage 0 blocking and
   both made the rest of the proposal contingent on its result. If Stage 0 closes the gap, the
   entire fused-operator project is unnecessary and the proposal reduces to flipping
   `mhjPackingEnabled` off — the best available outcome.

4. **Claimed benefit (1) is a misreading of the gates.** `make plan-gate` (`Makefile:376-390`)
   diffs goopg against goopg's own newest snapshot; `scripts/pg-oracle-diff.sh` diffs result
   text. Neither compares plans to PG. Removing MHJ *enables a new gate that does not exist*.

5. **Claimed benefit (3) is false: `multi_hash_join.go` cannot be relocated as-is** — chiefly
   because its build path uses `drainRowsCtx` with no `WorkMem` and no spill, so a fused operator
   inheriting it would *lose* spill in exactly the dimension this work exists to fix.

Both passes also independently put MHJ node deletion **last** and conditional.

---

## Contradiction — one, and it is the substantive one

**Does runtime fusion escape the dead end recorded in cost-model doc 15?**

| pass | verdict | argument |
| --- | --- | --- |
| A | **FALSE** | Doc 15's own conclusion is *"the blocker underneath is the join ORDER, not the MHJ cost."* The proposal answers a *cost* objection with a *timing* change, but the cost objection was never the blocker. On a bushy tree the fusion predicate declines and the 804 s stands. |
| B | **ACCEPTED — "the strongest argument"** | The dead end doc 15 records is specifically that an MHJ candidate cannot win a cost comparison without distorting all 22 queries. A decision taken at execution time is not subject to that argument at all. Sound as stated. |

**Adjudication (synthesis pass): both are right about different halves, and the split matters.**

B is right narrowly: the *stated objection* is escaped cleanly, and that is a genuine advantage
of the third option over doc 15's v2. A is right about the consequence: the objection was not the
binding constraint, so escaping it buys nothing on the target queries.

The synthesis pass then found the decisive confirmation, which neither pass had:

> **Q5 — the worst regression in the entire evidence set (6.43 s → HANG) — contains no
> `MultiHashJoin` in its plan at all.**

Verified by attributing every `Multi-Way Hash Join` line in `plan_snapshots/m0125-0043-after.txt`
to its query: the MHJ-shaped set is `{Q2, Q3, Q7, Q9, Q10, Q11, Q18, Q21}`, and Q5 is not in it.
No fusion implementation could have helped Q5, because there is nothing there to fuse. Net
benefit of the claim on the queries doc 15 targeted: **≈ zero**. It must not appear in any
stage's success criteria. Recorded in [02 §3](02-premise-audit.md).

---

## Partial coverage — reached by one pass, verified, and now in the bundle

Each of these was found by exactly one pass, would have been missing from a single-pass bundle,
and was re-verified by the synthesis pass before inclusion.

**Only pass A had:**

- **The cached-schema permutation hazard.** `Join.Output()` returns the cached `n.schema`
  (`plan.go:889-897`), which `:869-886` records can go stale as a *same-width permutation*. A
  width-only assertion therefore passes on exactly the corrupted input it was meant to reject.
  This turned the bundle's central structural check from width-identity to **element-wise column
  identity** — the single most consequential correction in the set. [04 §4](04-fusion-site-and-data-structures.md).
- **`drainRowsBounded`'s deep copy is REQUIRED, not an inefficiency** (`spill.go:384-402`, the
  M0073-0004 retention boundary). Pass A's own first draft told implementers to remove it, which
  would have introduced a silent corruption bug. [03 C13](03-semantic-contract.md), [08 R15](08-risk-register.md).
- **`Build`/`buildRec` have no root, no session, no `*Context`** (`executor.go:21`, `:424`), so
  the kill switch cannot be a session GUC and Q0's root walk needs new plumbing.
  [04 §1.1](04-fusion-site-and-data-structures.md).
- **The `Gather` exclusion cannot fire where it is aimed** — a worker's build sees `p.Child`,
  which by construction contains no `Gather` (`executor.go:213-219`). The exclusion must be a
  positive flag set by the worker closure. [03 C10](03-semantic-contract.md).
- The full qualification predicate Q0–Q9 and the claim-verification table.

**Only pass B had:**

- **`EstimateRows` has no `*MultiHashJoin` arm** (`cardinality.go:38+`) ⇒ every MHJ estimates
  0 rows. Found as a Stage-0 measurement confound; the synthesis pass **raised the severity**,
  because `mhjPackingEnabled` defaults to true, making it a live defect in production today.
  [08 R18](08-risk-register.md).
- **`VirtualSlot.Materialize()` does not clone arena-backed Datums** (`slot.go:167-169`), unlike
  its siblings — a latent dangling-reference bug that slot chaining would make live.
  [08 R3c](08-risk-register.md).
- **goopg's DP emits bushy trees, not left-deep ones**, and Q5's deepest level has its sub-join
  on the *build* side (`bushy.go:1382`; `plan_snapshots/r5-default.txt:62-71`). The PG left-deep
  equivalence does not transfer to goopg's actual plans. [02 §10](02-premise-audit.md).
- **goopg emits no `Hash` node** where PG always does (verified: 0 vs 40), and every node prints
  `cost=0.00..0.00 width=0` (204 of each). Plan-shape parity is further away than the proposal
  assumes. [06 §6](06-explain-and-plan-shape.md).
- **The SF0.5 checksum gate covers 57 of 99 queries**, not 99. [08](08-risk-register.md).
- **`Aggregate` is not migrated to the slab**, so every aggregate-topped TPC-H star query runs its
  joins under legacy `Build` — which decides which Stage 0 variant governs the analytic
  workload. [02 §9](02-premise-audit.md).
- **The prior in-repo record of this exact regression**
  (`docs/design/0125-0002-...:188-198`), which had already concluded that the axis moves in both
  directions and must be measured per commit. Pass A re-derived that conclusion from raw data
  without knowing the record existed — a nice independent corroboration, and a reminder to search
  the design corpus before re-deriving.
- **The honest business case**: 28 non-test `case *MultiHashJoin:` arms across 15 files
  (verified). The reason to delete the node is bug-surface removal, not speed.

---

## Unique insight — the reframing that neither pass was asked for

Both passes, independently, concluded that **the proposal's stated benefits are largely wrong and
its goal is nevertheless right, for a reason it never states.** MHJ's existence forces a
flatten-to-OID-ordered-table-list-and-remap-every-`ColumnRef` round trip in the planner
(`bushy.go:1790-1830` + `:1854` + `:2349`), and that machinery is the origin of this project's
worst bug class — including the stale-permutation defect at `plan.go:869-886` and the
tip-of-branch commit `23a077ae`.

So the bundle's recommendation inverts the proposal's own justification: **do it to delete an
index-skew bug generator; require only that speed does not regress.** That reframing changes the
success criterion for the whole line of work, and it is the most useful thing the panel produced.

---

## Blind spots — what neither pass covered, and what the reader should not assume is handled

1. **Nothing was measured.** Every runtime figure is 2026-07-24 vintage (`cb37d166`) read against
   a 2026-07-31 tree. The bundle's central quantitative claim — that the seam copies account for
   a large share of the 117 µs/row vs 20 µs/row gap — is an **arithmetic estimate from data
   structure sizes, not an observation**. Stage 0c exists to settle it and is blocking for that
   reason. Do not cite any number here as current.
2. **TPC-DS is the larger exposure and neither pass sized it.** The same MHJ machinery runs under
   99 TPC-DS queries against only 22 TPC-H ones, and the SF0.5 gate content-verifies just 57 of
   them. The blast radius of Stage 4 is therefore concentrated where the detector is weakest, and
   no stage in [09](09-staged-implementation-plan.md) quantifies that.
3. **The evidence files' timeout asymmetry was noted but never corrected for.** The default run
   capped at 600 s, the cost-driven run at 300 s. Every "cost-driven fails" conclusion inherits
   it, and no pass attempted a corrected estimate or proposed a re-run at matched caps. That
   re-run is cheap and should probably precede Stage 0.
4. **Neither pass audited the TPC-DS SF0.5 side for MHJ shape at all.** The MHJ-shaped query set
   was derived for TPC-H only. Stage 4's risk on TPC-DS is unassessed.
5. **The interaction with the concurrently-running Ralph loop is unmodelled.** The loop is
   actively editing `internal/planner/` (including `bushy.go` and the small-dimension work that
   feeds `buildLeft`). Any Stage 0 baseline captured today may not describe the tree by the time
   Stage 1 starts. Implement in a worktree off a pinned HEAD ([08 R14](08-risk-register.md)) and
   record that SHA in the evidence file.

---

## Reading this bundle's confidence levels

| claim class | confidence | why |
| --- | --- | --- |
| The five consensus conclusions above | **high** | reached independently by two cold passes and verified against source |
| Every `file:line` citation in the bundle | **high** | [12](12-claim-verification-table.md) plus the synthesis transcript |
| The doc-15 adjudication ([02 §3](02-premise-audit.md)) | **medium-high** | contested by the panel; resolved by an independent third check (Q5 has no MHJ) |
| The magnitude of the Stage 0 win | **low** | arithmetic only; **no measurement exists** |
| Anything about TPC-DS | **low** | out of scope for both passes |
