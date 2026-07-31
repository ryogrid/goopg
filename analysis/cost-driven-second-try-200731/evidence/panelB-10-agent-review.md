# 10 — Adversarial review record

| field | value |
| --- | --- |
| reviewed | this bundle, all of README + 01–09, at the state of 2026-07-31 |
| method | two passes, both completed. (i) A source-verification pass by the author over every citation and load-bearing claim → findings **A1–A4**. (ii) A dedicated adversarial reviewer agent instructed to verify **every** file:line citation against source, attack the central technical claim, attack the rejections in [02](02-premise-audit.md) §3, and enumerate omissions → findings **F1–F14** and citation corrections **C1–C7**. Every finding below was re-verified by the author against source before folding. |
| outcome of pass (ii) | **three findings the reviewer rated potentially fatal (F1, F4, F3) were all confirmed and all three changed the design.** The bundle's structure survived; its central diagnosis, its Stage-0 measurement, and its top risk's mitigation did not survive unmodified. |
| constraint | read-only; no server started, no benchmark run (a TPC-DS sweep owned the machine) |
| outcome | **the recommendation stands, but three of its supporting claims were wrong.** 4 + 14 findings; all folded back or explicitly declined below |

The point of recording this is that the previous attempt at this problem
(`docs/design/cost-model/15-…md`) was **rejected at SEV-1 by agent review at v1** and had to be
rewritten; its v2 was then implemented, measured, and reverted. A review record that is not
written down gets re-litigated.

---

## A. Findings folded back into the documents

### A1 — SEV-3, and it halves the claimed Stage-1 win: slot chaining removes only ONE of TWO per-probe-row copies

**Finding.** The bundle's central claim ([02](02-premise-audit.md) §2) identified
`r := slotRow(probeSlot)` at `internal/executor/operators_join_agg.go:1214` as *the* per-level
copy. It is not the only one. Immediately after, `nextLazy` copies the same row **again** into a
full-width scratch buffer purely to evaluate the probe-side hash key:

```
operators_join_agg.go:1219-1234
    if o.lazyKeyRow == nil || len(o.lazyKeyRow) != w { o.lazyKeyRow = make(Row, w) }
    …
    copy(o.lazyKeyRow[:o.lazyLW], r)
    copy(o.lazyKeyRow[o.lazyLW:], nullRight)
    …
    kd, kok, kerr := o.evalHashKeyDatum(probeKeyExpr, o.lazyKeyRow)
```

`evalHashKeyDatum` (`:960-969`) takes a `Row` and calls `evalExpr`. So per probe row the cascade
pays `slotRow` (allocate + copy W Datums) **plus** `copy` into `lazyKeyRow` (another W Datums).
Removing only the first leaves half the traffic.

**Fold-back.** [04](04-fusion-design.md) §1.1 now specifies that Stage 1 must *also* convert the
probe-key evaluation to the slot path: add `evalHashKeyDatumSlot(keyExpr, SlotView)` over the
existing `evalExprSlot` (`internal/executor/expr.go:353`) — the same primitive
`joinPredicateMatchSlot` (`:1019`) already uses — and evaluate the key against
`o.lazyVirtualOut` with the build slot bound to the pre-allocated null row. Without this, Stage 1
is a partial fix and its measurement will under-report.

### A2 — SEV-4, and it materially lowers the top risk: existing retention sites already deep-copy

**Finding.** [07](07-risk-register.md) R1 was written as if slot chaining newly exposes every
downstream retaining operator. Verified: it largely does not.

- `drainRowsCtx` (`operators_join_agg.go:3351-3382`) *always* does
  `dup := make(Row, len(row)); copy(dup, row)` and `cloneRowOwned` for arena Datums.
- `sortOp.Open` retains via `slot.Materialize()` (`internal/executor/operators.go:677`).
- `windowOp` uses `slot.Materialize().Row()` (`operators_window.go:62`);
  `lockRowsOp` likewise (`operators_lockrows.go:805`).

Decisively: the parent join's output remains a `*VirtualSlot`, and `VirtualSlot.Row()`
(`slot.go:159-164`) allocates a fresh row regardless of what its sources are. **Downstream
consumers therefore see exactly the same slot type and the same copy semantics as today.** The
new aliasing is confined *inside* the join chain.

**Fold-back.** R1's severity is retained at SEV-1 (a wrong-values bug is a wrong-values bug) but
its mechanism statement is corrected in [07](07-risk-register.md) to name the actual exposure —
a consumer that holds a `SlotView` (not a `Row`) across a `Next()` — and the test matrix is
narrowed accordingly. Over-stating a risk is not free: it inflates the gate cost and buries the
real one.

### A3 — SEV-2: a fused operator must survive re-`Open` (subplan rescan)

**Finding.** Not considered anywhere in the bundle. `internal/executor/subplan.go:222-235` walks
the plan tree to decide a correlated subplan's rescan strategy and promotes `rescanReOpen` to
`rescanCloseOpen` when it encounters a `*planner.Join` (and, today, a `*planner.MultiHashJoin`).
So a join cascade inside a correlated subplan is **closed and re-opened per outer row**. A fused
operator must therefore: rebuild its hash tables on every `Open`, release the previous ones, and
never retain state across `Close`. `multiHashJoinOp.Close` (`multi_hash_join.go:631-649`) nils
its state but also closes build children a **second** time (they were already closed at `:185`)
— an idempotence assumption a fresh operator should not inherit blindly.

**Fold-back.** Added as an explicit requirement in [03](03-semantic-contract.md) §12 (new) and as
a Stage-3 gate item in [08](08-staged-plan-and-gates.md): a correlated-subquery test whose
subplan body is a 3-level cascade, run with fusion on and off.

### A4 — SEV-4: `parallel_hash_build.go` citation was off by one function

**Finding.** The bundle cited `collectShareableJoins` at `parallel_hash_build.go:131-160`; that
range is `prebuildSharedHashJoins`. `collectShareableJoins` is at `:168`.

**Fold-back.** Corrected in [03](03-semantic-contract.md) §9, [04](04-fusion-design.md) §3.1 and
[07](07-risk-register.md) R7.

---

## B. Findings recorded but deliberately NOT acted on

### B1 — "The probe side of a goopg hash join is often a leaf, not a sub-join, so the chaining win is smaller than claimed"

Partially true and worth stating. `buildJoinFromDP` sets `buildLeft = lRows < rRows`
(`internal/planner/bushy.go:1367`) — **build the smaller side** — with a small-dimension override
at `:1372-1389`. In a fact-star cascade the accumulated intermediate is usually the larger side,
so it lands on the **probe** side and the chain forms. Verified on the committed Q5 plan
(`plan_snapshots/r5-default.txt:57-75`): of its three stacked `Hash Join` levels, **two** have a
`Hash Join` on the probe side (chain forms) and the innermost has the sub-join on the *build*
side (correctly, because it is the smaller side, 1.99 M vs 6 M — exactly what PG would do).

So the win is real but applies to (depth − 1) seams, not all of them. No document change beyond
recording it here; the Stage-0/Stage-1 measurement will price it exactly.

### B2 — "goopg's row estimates are so wrong that any cost-model conclusion is unsafe"

Also true, and visible in the same Q5 plan: the estimates run 6 M → **402 301 450** → 2 011 507 →
100 575 350, i.e. non-monotonic by three orders of magnitude. This is a real hazard for
[06](06-cost-model-interaction.md), but it is *pre-existing* and out of this bundle's scope; it
is already the subject of `docs/design/cost-model/05` and `/14`. Recorded, not adopted.

### B3 — "Stage 3 should be cut from the bundle entirely since you expect to decline it"

Rejected. The task under review is specifically the runtime-fusion proposal; a bundle that
merely asserts "we expect it to be unnecessary" without a design cannot be falsified, and if the
Stage-2 exit measurement *does* demand fusion, a later loop would have to start from nothing.
The contingency is expressed as a **numeric entry condition**
([08](08-staged-plan-and-gates.md) Stage 3), which is the honest form.

---

## C. Claims the review could NOT verify, stated as such

- **Every runtime number.** No server was started and no benchmark run; the machine was running a
  TPC-DS sweep. Every performance statement in this bundle is either quoted from a committed
  evidence file or derived arithmetically from data-structure sizes. The 21.6 GB figure in
  [02](02-premise-audit.md) §2.1 in particular is an *arithmetic estimate*, not a measurement,
  and is labelled as such.
- **Whether Stage 1 actually recovers the round-5 regressions.** This is the entire open question
  and it is precisely what Stage 2's exit measurement exists to answer. The bundle deliberately
  does not assert it.
- **The `BuildFast`/`opnode.go` slab path's exact behaviour for stacked joins.** The bundle flags
  it as a must-decide ([04](04-fusion-design.md) §3.1) rather than resolving it, because it needs
  execution to confirm which path real queries take.

---

## D. Standing review checklist for the implementing loop

Reject any diff that:

1. introduces `drainRowsCtx` (unbounded) on a hash-join build side instead of
   `drainRowsBounded(child, ctx.WorkMem)`;
2. sorts join inputs or output columns by anything (OID or otherwise);
3. re-derives residual-filter placement instead of using each level's own `Predicate`;
4. reorders conjuncts within a level's `Predicate`;
5. adds a `case *planner.<anything>` in the **planner** as part of Stages 1–3 (they are
   executor-only by construction; a planner hunk means the blast radius grew);
6. changes `EXPLAIN` (non-ANALYZE) output in Stages 1–3;
7. lands more than one stage in one commit;
8. cites a measurement that is not committed under `analysis/cost-driven-second-try-200731/evidence/`.

---

## E. Reviewer pass (ii) — findings F1–F14, and where each landed

I re-verified each of these against source before folding; the verification result is stated per
row so a later reader does not have to repeat it.

### E1. Confirmed, fatal-if-unfixed, and folded

| # | finding | verified? | folded into |
| --- | --- | --- | --- |
| **F1** | **The named defect line is already zero-copy on the live server path.** `BuildFastIterator` (`internal/server/dispatch.go:2839`, `:3691`) → `opTreeSlab.buildRec`; a join's probe child is an `opNodeOperator` whose `Slot.Row()` returns `Row(s.Cells)` — a **view** (`opnode.go:111`). The copy lives in `joinOpKernelNext` → `fillFromTupleSlot` (`opnode.go:869-876`, `:133-152`) and is paid **twice** (`ts.Row()` then `copy(s.Cells,row)`). | **Yes.** Read both functions. Also confirmed the reviewer's "partial reprieve": `buildRec` migrates only `SeqScan, Filter, Project, Limit, Sort, Update, Delete, Insert, Join` — **no `Aggregate`** — so aggregate-topped TPC-H star queries do run their joins under legacy `Build`, where the original diagnosis holds. | [02](02-premise-audit.md) §2.0/2.1/2.2 (rewritten as two sites on two paths), [04](04-fusion-design.md) §1.1c, [08](08-staged-plan-and-gates.md) Stage 0 deliverable 4 + Stage 1 deliverable 3, README verdict. **This is the single largest change in the fold-back.** |
| **F4** | **`EstimateRows` has no `MultiHashJoin` arm**, so every MHJ estimates 0 rows and every ancestor `BuildLeft` / `Algo` guard short-circuits — meaning `GOOPG_MHJ_PACKING` is *not* a shape-only toggle and Stage 0 cannot produce the number it exists to produce. | **Yes.** Enumerated every `case` in `cardinality.go:37-83`; there is none. Snapshot corroborates (`r5-default.txt:129` prints `rows=1` for a 4-table MHJ). | New risk [07](07-risk-register.md) **R14**; [08](08-staged-plan-and-gates.md) Stage 0 gains a deliverable-2 (`EstimateRows` arm, own commit) and its exit decision is now conditional on it; "constant order" given an operational definition. |
| **F3** | **`VirtualSlot.Materialize()` does not `cloneRowOwned`** (`slot.go:167-169`), unlike `MaterializedSlot.Materialize()` (`:106-109`), `Slot.Materialize()` (`opnode.go:115-125`) and `drainRowsBounded`'s explicit `rowHasArena` guard (`spill.go:388-394`). So R1's sole mitigation ("consumers call `Materialize()`") does not mitigate R1 once the chain roots at arena-backed scan slots. | **Yes.** Both implementations read directly. | New prerequisite commit [04](04-fusion-design.md) **§1.0**; [07](07-risk-register.md) R1 mitigation rewritten; [08](08-staged-plan-and-gates.md) Stage 1 deliverable 0 + an arena-lifetime gate. |

### E2. Confirmed and folded — corrections to claims the bundle got wrong

| # | finding | verified? | folded into |
| --- | --- | --- | --- |
| **F9** | 03 §2 asserted the OID sort *changes* column order; `bushy.go:1790-1795` says it exists **to match** the replaced tree, and the skew being repaired is the **bushy DP's DFS order**. | **Yes** — the comment says exactly that. The bundle's own §4 business case had to be re-derived. | [03](03-semantic-contract.md) §2 rewritten with the comment quoted; [02](02-premise-audit.md) §3.3 row 4 and §4 re-derived from the flatten-and-remap round trip. |
| **F5** | The probe-side-vs-build-side question: **mixed, not universal.** `buildLeft = lRows < rRows` (`bushy.go:1382`) builds the *smaller* side; `parallel_hash_build.go:163-166` documents both shapes; Q5's deepest level has its sub-join on the **build** side. Also: goopg's DP emits **bushy**, not left-deep, trees — so §1's PG equivalence does not transfer. | **Yes**, and independently reached by the author while drafting (former §B1). | Promoted from "recorded, not adopted" into [02](02-premise-audit.md) **§2.5**; §2.3's arithmetic recomputed over probe-side seams only (21.6 GB → ~37 GB over 2 seams × 2 copies); Stage 5's case now carries the left-deep question explicitly. |
| **F6** | Mechanism A (rebind `sources` per row) and Mechanism B (`sources` immutable after `Open`) are mutually inconsistent — and `joinOp` genuinely returns **three** different slot objects (`:1188`, `:1148`/`:1301`/`:1310`, `:1181-1186`). | **Yes.** | [04](04-fusion-design.md) **§1.1a** — new decision to normalise `joinOp` to a single emitted slot object, made a hard dependency of Mechanism B and an explicit part of Stage 1's diff. |
| **F7** | "structurally comparable to PG for the first time" is false: goopg emits **no `Hash` node**; PG always does. | **Yes** — zero `-> Hash` lines in `plan_snapshots/r5-default.txt`, and the only label is `algo = "Hash Join"`. | [05](05-explain-and-observability.md) §1 (claim retracted and replaced) and §2 (new Stage-4 prerequisite decision); [08](08-staged-plan-and-gates.md) Stage 4. |
| **F8** | The SF0.5 checksum gate covers **57 of 99** rows: 42 are `ck=n/a` (LIMIT-saturated), 16 return zero rows. | **Yes** — `grep -c '|n/a|'` = 42. | New risk [07](07-risk-register.md) **R13**; the gate-to-risk matrix, [08](08-staged-plan-and-gates.md) Stage 1, and [09](09-rollback-and-killswitch.md) §4 all corrected; `pg-oracle-diff.sh` on the 9 MHJ-shaped queries added as the companion gate. |
| **F2** | Slot chaining removes at most half the per-probe-row copy — `lazyKeyRow` (`:1216-1241`) remains. Plus two `Row` consumers missing from the "must not change" list: `lazyKeyRow` and `lazyOuterOnlySlot`. | **Yes.** Independently found by the author as A1; the reviewer added the `lazyOuterOnlySlot` site. | [04](04-fusion-design.md) §1.1b and §1.3 (five sites → seven). |
| **F12** | The cancellation root cause is asserted, not established — `seqScanOp.Next` already returns `57014` (`operators_storage.go:1451-1452`), so Stage 0's proposed gate would pass *before* the fix. | **Yes.** | [07](07-risk-register.md) R6 re-stated as defence-in-depth; [08](08-staged-plan-and-gates.md) Stage 0 gate replaced with one that discriminates. |
| **F13** | Entry conditions only half checkable: "% slower" is undefined for a non-completing query, and §1's practice of counting a hang at its 300 s cap would let a hang read as "3.4× slower". | **Yes.** | [08](08-staged-plan-and-gates.md) Stages 3 and 5 — non-completion is now a separate automatic trigger, never a ratio. |
| **F11** | Rescan of a fused operator under a correlated `SubPlan` was unconsidered. | Independently found by the author as A3 and already folded before pass (ii) returned; the reviewer added the spill-file and `Memoize`-interposition consequences. | [03](03-semantic-contract.md) §11; [04](04-fusion-design.md) §3.3 runtime disqualifier; [08](08-staged-plan-and-gates.md) Stage 3 gate. |
| **F14** | Smaller gaps: `EXPLAIN (BUFFERS)` / heap-fetch attribution across a fused pipeline; `GatherMerge` / `PartialAggStates` not covered by the `SharedHashBuilds != nil` test; `preserveCTIDRel` (FOR UPDATE) has no fused equivalent; **`planner.Material` does not exist**, so R1's test matrix listed a test that cannot be written; MHJ `Filters` need no re-homing at Stage 5. | **Yes** on all five. | [05](05-explain-and-observability.md) §3.4 (new, incl. adding BUFFERS to the byte-identity gate); [04](04-fusion-design.md) §3.3 (two new disqualifiers) and §4 step 5; [07](07-risk-register.md) R1 matrix corrected. |
| **C1–C7** | Citation errors: `multiHashJoinCost` at `:177` not `:169`; `remapColumnRefsAfterRewrite` at `:1854` not `:2189`; lineitem at `r5-default.txt:133` not `:131`; `lazyHash` appends at `:681`/`:731` not `~:660`; Q5 has **five** `Hash Join` nodes and is bushy; six off-by-a-few ranges; and `operator.go:49-64` is a **historical note about deleted APIs**, not a normative contract. | **Yes**, all. | All corrected in place. C7 in particular changed [02](02-premise-audit.md) §2.4 and [04](04-fusion-design.md) §1.2 from "the contract already grants this" to "the control flow permits this, and Stage 1 should promote it to an asserted invariant". |

### E3. Confirmed and explicitly NOT acted on

| # | finding | why not |
| --- | --- | --- |
| **F10** | The plan-gate rejection ([02](02-premise-audit.md) §3.1) is **fair** — the reviewer verified `cmd/plan-snapshot/main.go` has only `capture`/`diff`, that its `sql.Open("postgres", …)` is the driver name not a PG oracle, and that `pg-oracle-diff.sh` contains no `EXPLAIN`. The one nuance — `plan-snapshot capture --host/--port` *can* point at PG 65432 — is folded into [05](05-explain-and-observability.md) §2, which now scopes Stage 4 as "write the normaliser", not "write the tool". | Confirmation, not a defect. Folded the nuance only. |

### E4. The reviewer's own stated non-verifications

Recorded because they bound what this review establishes: no server was started, so **every
runtime number in this bundle remains unverified by either pass** — the 37 GB arithmetic, the
round-5 wall times, the "Q9 = 25.69 s" correction, and the staleness of 804/118. The reviewer
also did not open `docs/design/cost-model/15-*.md` or `docs/design/0125-0002-*.md`, so the
quotations from those two documents rest on the author's reading alone. And no one audited every
retention site for correct `Materialize()` placement — [07](07-risk-register.md) R1's test matrix
is the instrument for that, and it has not been run.

**What this means for the programme: Stage 0 exists precisely because nothing here is measured.
No claim in this bundle about speed should be acted on before Stage 0 reports.**
