# 11 — Adversarial review, and the fold-back log

| field | value |
| --- | --- |
| reviewer | an independent adversarial agent, read-only, instructed to falsify rather than confirm |
| date | 2026-07-31 |
| scope | all documents in this set as of the first draft |
| method | the reviewer was given six named design claims and told to attempt to falsify each, plus an open brief to find what the design missed |
| outcome | **5 SEV-1, 8 SEV-2, 4 SEV-3.** Two SEV-1s changed the design materially; one SEV-1 would have introduced a silent wrong-answer bug if implemented as written |

Every SEV-1 and SEV-2 below was **independently re-verified against the source** before being
folded in; the verification command output is what the "verified" column reflects.

## Verdict on the six named claims

| # | claim | outcome |
| --- | --- | --- |
| 1 | build-time fusion leaves EXPLAIN untouched | **survives** (with F6's caveat) |
| 2 | the telescoping width identity protects the column mapping | **FALSIFIED** — F1 |
| 3 | `LeftKey`/`RightKey` are in the merged coordinate space | **survives for the DP-built cascade only**, not globally — F9 |
| 4 | holding the child's slot (Stage 0a) is safe | **survives on lifetime**, but the cost was mis-located (F2) and the mechanism was under-specified (F7) |
| 5 | the fused odometer's row order equals the cascade's | **survives** |
| 6 | `instrumentScope` is visible at build time | **survives on visibility**, fails on safety — F8 |

## SEV-1 findings and what changed

### F1 — the width identity cannot detect the bug class it was sold as preventing

`Join.Output()` (`internal/planner/plan.go:889-897`) returns the **cached** `n.schema` for
every non-semi/anti join; it does not recompute `Left ++ Right`. The comment at `:869-886`
records that this cache can become "a stale *permutation* of the real layout" — the
M0125-0008 wrong-rows bug. **A permutation has the same width**, so the proposed check passes
on exactly the corrupted input it claimed to reject.

*Verified:* read `plan.go:886-897`. Confirmed — `return n.schema`.

*Folded back:* [04 §4](04-fusion-site-and-data-structures.md) demoted the width identity to
"necessary but not sufficient" and added a mandatory **element-wise** column-identity
assertion; [05 Q6](05-qualification-predicate.md) gained clause 3 marked load-bearing;
[08 R2](08-risk-register.md) records that without clause 3 the risk has no mitigation;
[09](09-staged-implementation-plan.md) gained `TestFusedSchemaElementWiseIdentity`.

### F2 — Stage 0a targeted the wrong code site

On the live path (`BuildFastIterator`), a `Join`'s children are `opNodeOperator` whose
`Next()` returns a `*Slot`, and `Slot.Row()` is a zero-copy view (`opnode.go:110-111`). So
`nextLazy`'s `slotRow(probeSlot)` allocates nothing in production. The real materialisation is
`joinOpKernelNext` → `Slot.fillFromTupleSlot` (`opnode.go:868-876`, `:129-150`), which calls
`ts.Row()` on the child's `*VirtualSlot` (→ `acquireRow`) and then copies again into
`dst.Cells`, dropping the pooled row without `releaseRow`.

*Verified:* read `opnode.go:105-150` and `:860-876`. Confirmed.

*Folded back:* [02 §4.1](02-premise-audit.md) rewritten to separate the two builders' profiles
and to name `fillFromTupleSlot` as the live site; Stage 0a split into **0a-live** (a ~5-line
`*VirtualSlot` fast path, no lifetime reasoning) and **0a-legacy** (the slot-holding variant,
only if measurement justifies it).

### F3 — `tryFuseHashCascade(p, root)` is unimplementable at the chosen site

`Build(plan planner.Node)` (`executor.go:21`) takes one node, recurses as `Build(child)`, and
has no session and no `*Context`. So Q0's root walk has nothing to walk, and the promised
**session-level GUC** cannot be read at build time at all.

*Verified:* read `executor.go:21`, `:424-560`. Confirmed.

*Folded back:* [04 §1.1](04-fusion-site-and-data-structures.md) adds an explicit `buildEnv`
plumbing section and calls it "likely the largest single piece of Stage 1";
[10 KS1](10-rollback-and-kill-switches.md) **withdraws** the session-GUC claim and the
"A/B without restarting" benefit that was justified by it; [09](09-staged-implementation-plan.md)
Stage 1 scope now lists the plumbing first.

### F4 — the `Gather` exclusion does not fire where it matters

`Gather` builds each worker's tree via `func() (Operator, error) { return Build(p.Child) }`
(`executor.go:213-219`); inside that call no `Gather` is visible, so a plan walk cannot see it.
Separately, `prebuildSharedHashJoins` discovers shareable builds by walking the **built
operator tree** for `*joinOp` (`parallel_hash_build.go:119-150`), where a `fusedHashJoinOp`
is invisible.

*Folded back:* [03 C10](03-semantic-contract.md) replaced the plan walk with a positive
`inWorker` flag set by the `Gather` closure, and added the `collectShareableJoins`
requirement; [05 Q0](05-qualification-predicate.md) restructured accordingly;
[09](09-staged-implementation-plan.md) Stage 1 scope includes it.

### F5 — C13 was factually wrong and would have caused corruption

`drainRowsBounded` **does** deep-copy every retained row —
`dup = make(Row, len(row)); copy(dup, row)`, or `cloneRowOwned(row)` when `rowHasArena(row)`
(`internal/executor/spill.go:388-399`). The first draft's C13 told the implementer not to copy,
on the premise that `drainRowsBounded` does not. Following it would make hash entries alias a
producer's reused buffer (`seqScanOp` reuses and releases `o.scanRow`,
`operators_storage.go:1361`) — the M0097-0058 corruption class.

*Verified:* read `spill.go:382-402`. Confirmed.

*Folded back:* [03 C13](03-semantic-contract.md) now states the opposite, in bold, with the
reason; [02 §7](02-premise-audit.md) corrected to say MHJ's real defect is **no budget and no
spill**, not copying; [08 R15](08-risk-register.md) added as a new risk, because an implementer
optimising for memory will be tempted to remove the copy.

## SEV-2 findings and what changed

| # | finding | folded into |
| --- | --- | --- |
| F6 | `EXPLAIN ANALYZE` already runs the **legacy** `Build` under `withInstrumentation` (`operators_explain.go:57-64`) while the server runs `BuildFastIterator`, and `buildRec`'s Join arm never calls `maybeInstrument` — so the ANALYZE/production divergence pre-exists this work | [06 §2](06-explain-and-plan-shape.md), [08 R8](08-risk-register.md) |
| F7 | children do not return a stable slot object (`lazyVirtualOut` / `lazyOuterOnlySlot` / `Materialize()` / fresh `asSlot` in `spill.go:441,468`), while `ensureLazyVirtual` caches a fixed `sources` slice — the source must be re-bound per pull, with a fallback | [09](09-staged-implementation-plan.md) Stage 0a-legacy |
| F8 | `instrumentScope` is a mutable package global set/restored without a lock (`instrument.go:215,225-233`) — gating fusion on it is cross-session non-deterministic and racy | [05 Q0](05-qualification-predicate.md), [06 §2](06-explain-and-plan-shape.md), [08 R16](08-risk-register.md) |
| F9 | the merged coordinate space is per-construction-site, not global (`unnest.go:2107` vs `:2071-2077`; repaired late by `reresolveJoinByName`, `bushy.go:2902-2925`) — one unit test cannot establish it | [05 Q3](05-qualification-predicate.md) caveat rewritten: the range check is the only authority |
| F10 | rescan was not addressed at all; `subplan.go:223-230` forces `rescanCloseOpen` for any `Join` under a SubPlan | new [03 C15](03-semantic-contract.md), new [08 R3b](08-risk-register.md), new `TestFusedCascadeRescan` |
| F11 | `evalHashKeyDatum` takes a `Row` (`operators_join_agg.go:960-968`), so "reused, not reimplemented" and "evaluate against the VirtualSlot" contradicted each other | [04 §7/§8](04-fusion-site-and-data-structures.md), Stage 0b scope |
| F12 | Q0 declines on plans containing an MHJ, and packing is on until Stage 4 — so Stage 2's A/B would have measured nothing | [09](09-staged-implementation-plan.md) Stage 2 ordering trap |
| F13 | deleting the MHJ node is ~20 files across planner and executor, and `generateMultiHashJoinPath` (`pathgen.go:100-105`) is a separate decision | new [08 R17](08-risk-register.md), Stage 4 |

## SEV-3 findings and what changed

| # | finding | folded into |
| --- | --- | --- |
| F14 | `acquireRow` only pools widths ≤ 64 (`row_pool.go:23,43`), so the Stage-0 cost is bandwidth + pool round-trips, not heap allocation — material, because the go/no-go rests on this number | [02 §4.1](02-premise-audit.md) magnitude paragraph |
| F15 | the Q2/Q3/Q5/Q7/Q10/Q11/Q18/Q21 packing list is an **M0054-0002 baseline** observation, not a HEAD fact | [09](09-staged-implementation-plan.md) Stage 0c now derives the set by running EXPLAIN at the measurement HEAD |
| F16 | gate names and semantics all check out; correction: `plan-gate` also needs `PATH`, and against a non-`tpch` cluster `PLAN_DB=postgres PLAN_USER=postgres` | [09](09-staged-implementation-plan.md) gate table |
| F17 | R1 / Stage −1 confirmed correct: the two-line `len(keys) != len(scans)-1` guard type-checks and fails closed exactly as claimed | no change — keep it as the first commit |

## What the review did not change

- The **verdict** (adopt with modification) and the ordering (Stage 0 before Stage 1) survived
  unchanged; F2 in fact strengthened Stage 0 by making its cheapest variant cheaper.
- The **rejection of the doc-15-escape claim** ([02 §3](02-premise-audit.md)) was not
  challenged.
- The **row-order argument** (C1) survived falsification.
- The **"no fusion discount" cost-model invariant** ([07 §4](07-cost-model-interaction.md)) was
  not challenged.

## Standing weakness of this review

The reviewer was read-only and ran no code: no `go test`, no server, no benchmark (a TPC-DS
sweep owns the host). So every *performance* claim in this set remains **unmeasured**, and
Stage 0c exists precisely to measure the one the whole proposal depends on. A design set that
has been adversarially read is not a design set that has been proven.

---

# Part B — the second design pass's independent adversarial review

This bundle was produced by **two independent design passes** over the same brief, neither seeing
the other's work, each of which then ran its own adversarial reviewer. Part A above is the first
pass's review. Part B records the second pass's, because it caught defects the first review did
not. The second pass's review document is retained verbatim at
[evidence/panelB-10-agent-review.md](evidence/panelB-10-agent-review.md).

The second review returned **14 findings plus 7 citation corrections**, rated three as
potentially fatal, and all three changed that pass's design. The synthesis pass re-verified the
load-bearing ones against source before folding them in
([evidence/judge-verifications-20260731.txt](evidence/judge-verifications-20260731.txt)).

| id | finding | status | where it landed in this bundle |
| --- | --- | --- | --- |
| **B-F1** | The central diagnosis was aimed at a line that is already free on the live server path. `BuildFastIterator` (`internal/server/dispatch.go:2839`) routes production queries through the slab, where the probe child's `Slot.Row()` returns a **view** (`opnode.go:111`) — so `slotRow` at `operators_join_agg.go:1214` copies nothing there. The copy has moved into `joinOpKernelNext` → `fillFromTupleSlot` (`opnode.go:869-876`, `:133-152`), which copies **twice**. | **CONFIRMED** — and independently reached by the first pass as its own finding F2, from the opposite direction. Highest-confidence item in the bundle. | [02 §4.1](02-premise-audit.md), Stage 0a's two variants |
| **B-F1b** | Partial reprieve: `buildRec` migrates no `Aggregate`, so every aggregate-topped TPC-H star query still runs its joins under legacy `Build`, where the original diagnosis holds. | **CONFIRMED** by enumerating `buildRec`'s `case` arms (`executor.go:425-546`): `SeqScan Filter Project Limit Sort Update Delete Insert Join` — no `Aggregate`. | [02 §9](02-premise-audit.md) (new section) |
| **B-F4** | Stage 0 could not produce the number it exists for: `EstimateRows` (`cardinality.go:37-83`) has **no `MultiHashJoin` arm**, so every MHJ estimates 0 rows and flipping `mhjPackingEnabled` changes build sides *above* the chain — the exact confound this line of work was criticised for. | **CONFIRMED**, and **severity raised** by the synthesis pass: with packing on by default this is a live defect in production, not only a measurement artefact. | [08 R18](08-risk-register.md), [09 Stage −1a](09-staged-implementation-plan.md) |
| **B-F3** | The top risk's only mitigation did not work: `VirtualSlot.Materialize()` (`slot.go:167-169`) does not `cloneRowOwned`, unlike its three siblings. After chaining, a consumer that correctly calls `Materialize()` gets dangling arena references. | **CONFIRMED** | [08 R3c](08-risk-register.md), [09 Stage −1b](09-staged-implementation-plan.md) |
| **B-F5** | goopg's DP emits **bushy** trees, and Q5's deepest level has its sub-join on the **build** side — so the PG left-deep equivalence does not transfer, and the per-level arithmetic must be recomputed over probe-side seams only. | **CONFIRMED** (`bushy.go:1382`; `parallel_hash_build.go:163-166`; `plan_snapshots/r5-default.txt:62-71`) | [02 §10](02-premise-audit.md) (new section) |
| **B-F7** | "Structurally comparable to PG for the first time" was false: goopg emits **no `Hash` node** where PG always does. | **CONFIRMED** by count: 0 bare `Hash`, 40 `Hash Join` in `plan_snapshots/m0125-0043-after.txt` | [06 §6](06-explain-and-plan-shape.md) (new section) |
| **B-F8** | The SF0.5 checksum gate covers **57 of 99** rows, not 99 (42 are `ck=n/a`, LIMIT-saturated) — which weakens the gate designated as R1's sole detector. | **CONFIRMED** by parsing `oracle.txt` (`q\|status\|rows\|ck\|secs`): 99 rows, 57 real checksums, 42 `ck=n/a`. | [08](08-risk-register.md) anchor-corpus correction, [09](09-staged-implementation-plan.md) gate-coverage caveat |
| **B-F9** | The claim that the OID sort *changes* column order was backwards — `bushy.go:1790-1795` says it exists to *match* the replaced tree; the skew comes from the bushy DP's DFS order. The business case had to be re-derived. | **CONFIRMED** — corrected in place rather than dropped; the hazard is the remap layer, not the sort. | [02 §11 row 4](02-premise-audit.md) |
| **B-F6** | Two proposed fusion mechanisms were mutually inconsistent; resolved by normalising `joinOp` to emit one slot object. | accepted by that pass | consistent with the first pass's finding F7 (children do not return a stable slot object) — [09 Stage 0a hazard](09-staged-implementation-plan.md) |
| **B-F12** | The cancellation root cause was asserted, not established (`seqScanOp` already checks), so a stage gate would have passed *before* the fix and was replaced. | accepted | [01 §3](01-motivation-and-measured-evidence.md) states the HANG class is GC/memory, **not** a missing cancellation check |

## What the two reviews, taken together, establish

**The strongest signal in this bundle is where the two passes agreed without contact.** Both
independently:

1. located the real cost at the **re-materialisation of the probe input at the join seam**, not
   at operator-boundary overhead — and both then had to correct themselves in the same way
   (the live path's copy is one layer down, in `fillFromTupleSlot`);
2. concluded that the correct first move is **de-materialise the seam, then measure**, and that
   building a fusion operator first is wrong;
3. rejected the `plan-gate` / `pg-oracle-diff` benefit as a misreading of what those gates do;
4. rejected "closer to a relocation" — `multi_hash_join.go` cannot be lifted, principally
   because its build path has no `WorkMem` and no spill;
5. put MHJ node deletion **last** and made it conditional on measurement.

Independent agreement on five points, from two cold starts, is the highest-confidence content
here. Treat items 1–5 as settled and spend review effort on the contested item instead — the
doc-15 dead-end question, adjudicated in [02 §3](02-premise-audit.md).

## Standing non-verification, stated plainly

Neither pass, and not the synthesis pass, started a server or ran a benchmark: a TPC-DS sweep was
occupying the machine and a Ralph loop was editing the tree. **Every runtime number in this
bundle is a citation of a committed artefact, none is an observation.** That is precisely why
Stage 0c exists and why it is blocking.
