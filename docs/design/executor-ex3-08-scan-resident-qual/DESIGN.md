# E-17 (EX3-08) — scan-resident qual: evaluate the predicate ONCE

Status: accepted (design; no code in this commit). Implements TODO_ALL.md
E-17 / EX3-08, **cut 2**. Cut 1 (a per-row "already decided" flag) is
ruled out by the owner and is not designed here: it would leave two
evaluators in the tree and add a third artefact to keep consistent.

Revised 2026-09-07 after two adversarial reviews (a source-falsification
pass over `internal/`, and a PG-18.3-oracle pass over `postgres/`). Both
found real errors in the first draft; §3, §4.3, §4.4 and §4.5 are
materially different as a result, and the review findings are recorded
inline rather than silently absorbed.

## 1. Why this exists

`DATUM-ROW-FORMAT-AND-PG-COMPAT.md` §5.1.1: on every row a
`Filter{SeqScan}` scan admits, goopg evaluates the WHERE predicate
**twice** — once inside `seqScanOp` (`scan_prefilter.go`, over the
deformed prefix `[0, MaxCols)`) and once in `filterOp` on the finished
row. `scan_prefilter.go` claims "the shape is PostgreSQL's"; it is not.
PG carries the qual as a field of the scan plan node (`Scan.plan.qual`),
evaluates it exactly once per tuple, and has no Filter node.

The saving PG gets for free — deform only what the qual reads — comes
from its lazy slot: `ExecInitQual` builds **one** `ExprState` for the
whole AND-list with a single leading `EEOP_SCAN_FETCHSOME` whose
`last_var` is the max attnum over all clauses
(`execExpr.c:2933-2938`, `ExecComputeSlotInfo` at `:3064`), and the
projection then deforms the rest under its own separate bound. That is
*exactly* goopg's `MaxCols` prefix model, arrived at independently.
goopg has no lazy slot (D-03 `PackedSlot` is inert, stopped on
measurement at D-04), so it emulates the bound with a two-phase deform
in the scan. The emulation is right. The second evaluation is not.

Cut 2 makes the scan the **sole** evaluator and deletes `filterOp` from
above the scan.

## 2. Oracle

The hot path in PG 18 is **`ExecScanExtended`** (an `always_inline` in
`execScan.h`), not `ExecScan` — `ExecScan` now serves only the EPQ
variant. `nodeSeqscan.c:262-277` picks one of four init-time-specialised
entry points (`ExecSeqScan`, `…WithQual`, `…WithProject`,
`…WithQualProject`) so the compiler eliminates the qual and projection
branches outright, plus `ExecSeqScanEPQ` when `es_epq_active != NULL`,
plus a no-qual-no-projection path returning the raw slot
(`execScan.h:175-179`). PG specialises *harder* than "one branch"; the
parity argument below is a floor, not a ceiling.

Within that loop:

- `execScan.h:214` `econtext->ecxt_scantuple = slot`, then `:223`
  `if (qual == NULL || ExecQual(qual, econtext))` — **once**, before
  anything else touches the tuple.
- `:234` `ExecProject` runs only after the qual passes.
- `:245` `InstrCountFiltered1(node, 1)` on the else branch — the
  rejection is counted **on the scan node**.
- `:184-185` and `:250` reset the per-tuple memory context before the
  loop and again immediately after every rejection
  (*"Tuple fails qual, so free per-tuple memory and try again."*).
- `explain.c:2015-2018` — `show_scan_qual(plan->qual, "Filter", …)` and
  `show_instrumentation_count("Rows Removed by Filter", 1, …)` for
  SeqScan. There is no Filter plan node to print.
- `instrument.c:187` `dst->nfiltered1 += add->nfiltered1` — worker
  counts are folded into the leader; `explain.c:3977-3987` divides by
  `nloops`.

## 3. Three findings that change the shape of this item

### 3.1 Plain EXPLAIN does not move. EXPLAIN ANALYZE does — toward PG.

The E-17 row and the task brief both assume "cut 2 moves EXPLAIN output —
the `Filter` line disappears — so it needs a `plan_snapshots/` re-pin".
**Plain EXPLAIN does not move.** All three renderers collapse a
`*optimizer.Filter` wrapper into the scan node it wraps and key on the
**plan node**, never on the operator: `walkPlanFiltered`
(`operators_explain.go:430-446`), `walkPlanAnalyzeFiltered` (`:1553-1570`)
and `jsonCollapse` (`:1921-1937`, consumed at `:1974-1981` and
`:2010-2016`). `COSTS OFF` (`:1592`, `showCostsA`) and VERBOSE
(`schemaColumnNames` on the surviving node) are likewise operator-blind.
The collapsed line even takes `rows=` from the *wrapper*, because that is
where the post-qual estimate lives (take2 P0-04b). Evidence:
`plan_snapshots/c20a-c06s-plancost-rows-20260907.txt` (31,141 B, the
newest pin) has 66 `Filter:` detail lines and **zero** standalone
`-> Filter` nodes; `warm-pin-20260905.txt` has 60 and zero.

Cut 2 is therefore scoped to the **executor**: the `*optimizer.Filter`
*plan node* stays (it is goopg's carrier of the post-qual row estimate
and cost), and the executor stops instantiating a `filterOp` above a
`SeqScan` whose qual the scan has absorbed. Plain EXPLAIN — labels,
`cost=`, `rows=`, `Filter:` lines, all three formats — is byte-identical
by construction, and the plan gate is a **regression check, not a
re-pin**.

**EXPLAIN ANALYZE is not byte-identical** (first draft claimed it was;
falsified). Two counters move, both toward PG:

- **`actual rows` on the collapsed scan line.** The ANALYZE label reads
  `stats[n]` for the *surviving* (scan) node and prints `s.rowsOut`
  (`operators_explain.go:1609-1616`; JSON `obj["Actual Rows"]` at
  `:1821-1823`), and `rowsOut` counts rows the scan **returns**
  (`instrument.go:200`). Today, whenever the prefilter is disarmed —
  which is every qual outside the 9-arm whitelist (`scan_prefilter.go:88-131`):
  `FuncCall`, `CaseExpr`, `InExpr`, `LIKE … ESCAPE`, and anything with
  `need >= ncols` — the scan returns every visible tuple and the
  collapsed line prints the **pre-qual** count. After cut 2 it is
  post-qual, which is what PG reports. This is a fix, on a much larger
  surface than §3.2, and §7's plan gate cannot see it (the gate runs
  plain EXPLAIN; the pins contain no `actual`/`Rows Removed` lines).
- **`Rows Removed by Filter`** — §3.2.

### 3.2 Cut 2 fixes a three-part `Rows Removed by Filter` defect

`filterRemoveCounter` is implemented **only** by `filterOp`
(`operators.go:573`); `seqScanOp` implements it nowhere. The prefilter's
reject path is a bare `continue` (`operators_storage.go:2095-2103`) and
increments nothing. PG counts every scan-qual rejection
(`execScan.h:245`). So goopg under-reports. Three separate defects sit
under that one sentence, and the design must name which of them it fixes:

1. **Text format, prefilter armed.** Under-reported by the prefilter's
   entire rejection count. Fixed by §4.5.
2. **JSON format, always.** `jsonCollapse` reads `filterRejected` off the
   **surviving** node (`operators_explain.go:1821`, `:1840`), i.e. the
   scan, while the text walker reads it off the **Filter** node
   (`:1566-1568`). So `EXPLAIN (ANALYZE, FORMAT JSON)` already reports
   `0` for every `Filter{Scan}` — a pre-existing sibling-path divergence
   that the first draft's wiring would have preserved. §4.5 resolves it
   by making the scan node the single home for the counter, as PG does.
3. **Parallel plans, always.** `foldGatherWorkerStats`
   (`instrument.go:399-425`) copies only `rowsOut`, `loops`, `startupNs`,
   `totalNs` into `workerNodeStat` (`:380-390`) — `filterRejected` is
   never folded, and worker trees are built under a fresh scope
   (`operators_gather.go:126`). PG folds it (`instrument.c:187`).
   **TPC-H bench plans are all parallel**, so the Q6 headline the first
   draft quoted ("~98 % of 6 M rows") is *not* fixed by the counter work
   alone. Worker folding is in scope for this slice (§4.5); the serial
   example is used for illustration.

### 3.3 Where the follow-up's cost actually is

The first draft justified deferring "move the qual into
`optimizer.SeqScan.Qual` and delete the plan node" by claiming it must
churn `rows=`. That is not a property of the target shape: PG's
`create_seqscan_plan` (`createplan.c:2910-2939`) attaches the clauses to
the `SeqScan` and `copy_generic_path_info` gives it `rel->rows`, which is
**already** the post-`baserestrictinfo` estimate. PG has no separate
carrier. Done PG-faithfully the refactor is rows-neutral; the churn risk
is in *goopg's* `EstimateRows(rowSrc)` plumbing, not in the shape. It
stays deferred (§8) on blast radius, not on an invented invariant.

## 4. The design

### 4.1 One evaluator, one node, at most one verdict per row

`seqScanOp` gains an absorbed qual (`o.qual` plus its compiled form) and
one evaluator entry point, `func (o *seqScanOp) evalQual(slot SlotView)
(bool, error)` — today's `evalPrefilter` body, generalised. It is called
at exactly one of two points in a row's lifecycle:

- **early** — after `decodeScanRowRange(0, MaxCols)`, before the tail
  deform, the detoast and the deep copy. Today's prefilter position, and
  where the win is.
- **late** — the position `filterOp` occupies. This is not a vague
  "after the row is finished": it is **immediately before
  `return &o.slot, nil` at `operators_storage.go:2274`**, i.e. after
  `DetoastRowBound` (`:2146`), `cloneRowOwned` (`:2183`), the enum
  injection (`:2185-2201`), the page `RUnlock` (`:2203-2205`), the three
  ACL rewrites (`:2212-2239`), the xmin hint-bit write (`:2241-2247`),
  the resjunk-ctid append (`:2258-2262`) and the
  `hasCTID`/`ctidBlock`/`ctidOff` stamp (`:2266-2269`). Rejecting there
  and `continue`ing the inner tuple loop is safe: the page RLock was
  already released at `:2204` and the loop re-RLocks per tuple at
  `:1949-1951`; the per-page arena is reset only at the page boundary
  (`:2286-2288`). Naming the exact line is load-bearing — the position is
  a 120-line ordering constraint, not a description.

  That the ctid stamp sits *below* the clone is also why `CTIDExpr` must
  stay early-ineligible: `o.scanSlot` (`scan_prefilter.go:146`) is a bare
  `rowSlotView` with no ctid.

**At most one verdict per row.** The first draft said "never both",
which its own §4.4 contradicts: a row whose early evaluation *errors* is
retried late (§4.4). The correct invariant is that an early evaluation
that errors produces **no verdict**, and the retry is sound because the
early-eligible set is pure — §4.3's veto list excludes `FuncCall`,
subqueries and params, so nothing early-eligible can observe a repeat or
leave a side effect.

This is not "two evaluators": one function, one node, one expression
walker; the two call sites differ only in how much of the row is
deformed. The consistency hazard cut 1 was rejected for — a second,
differently-written evaluator that must be kept in agreement — does not
arise.

**Keep condition.** `filterOp.Next`'s is
`!v.IsNull() && v.Kind == KindBool && v.BoolValue()`. The NULL arm is
three-valued logic and is PG (`execExpr.c:252-277`: `EEOP_QUAL`
short-circuits the first NULL to false). The `Kind == KindBool` arm is
**not** PG: parse analysis coerces WHERE to `boolean`, so a non-boolean
qual result cannot occur, and silently mapping one to *false* is a
wrong-answer sink. Today it is masked by `filterOp` doing the same thing;
once the scan is the sole evaluator it should raise rather than drop.
Behaviour change, so it lands as its own guarded arm with a test, not
folded into the move.

### 4.2 The early/late decision, and how each of today's abstains is absorbed

| today | why | cut 2 |
|---|---|---|
| `planScanPrefilter` returns `!ok` (unlisted node, whole-row/CTID ref, subquery, param, volatile func) | prefilter off; `filterOp` alone | **late**, whole scan |
| `need >= ncols` | "pure second evaluation" | **late**, whole scan (nothing to save; there is no second evaluation left to avoid) |
| Open-time disarm: enum column, `typacl`/`attacl`/`datacl`, GiST/GIN SSI (`operators_storage.go:1584-1607`) | the row is REWRITTEN after clone, so prefix values differ from what `filterOp` sees | **late**, whole scan |
| `needsDetoastPrefix` true | a toasted prefix value would be judged un-detoasted | **late**, this row |
| `perr != nil` | error must surface from where it did before | **late**, this row (§4.4) |

The first three are per-scan (decided in `Open`), the last two per-row —
exactly today's split. All five preserve the whitelist's failure
direction verbatim: an unhandled expression kind, an unmodelled rewrite
or a toasted prefix costs **performance** (late evaluation, i.e. today's
`filterOp` behaviour) and never correctness. That property is the one
the brief insists must not be lost, and it survives because the fallback
is a *position*, not an omission.

**On detoast, the first draft had the PG model backwards.** PG never
detoasts a row: `slot_getsomeattrs` hands the qual a raw, possibly
toasted, possibly short-header Datum, and expansion happens inside the
called function per argument (`fmgr.h:248,292,309` —
`PG_GETARG_TEXT_PP → DatumGetTextPP → PG_DETOAST_DATUM_PACKED`).
`execExprInterp.c` has no detoast step in the scan/qual path at all.
Combined with `EEOP_QUAL` short-circuit, a toasted attribute referenced
only by a later clause is never expanded for a row a cheaper earlier
clause rejects. So goopg's `DetoastRowBound`-the-whole-row-before-the-
predicate is *itself* the divergence, and this design's late fallback
merely inherits it. Prefix detoast (a second buffer plus a resumable
deform offset) is therefore **not** the right deferral: the PG-parity
item is **lazy per-datum detoast inside the evaluator**, and that is what
§8 files. The late fallback detoasts correctly and abstains from
nothing, which is all cut 2 needs.

### 4.3 The expression walker: exhaustive, fail-closed, on the existing primitive

The brief asks for `cloneExprRefs` style (all arms, build-time gate), not
`shiftColumnRefsBy` style (13 of 32 arms, `return e` default). The
strongest form of that is not a new hand-written switch but the primitive
those drivers are built on: `exprChildSlots` (`exprwalk.go:109`)
enumerates all 32 `Expr` types with no `default:`, is kept exhaustive by
`exprwalk_exhaustive_test.go`, and `walkExprRefs` (`:322`) **aborts** on
an unenumerated type — fail-closed by construction, as its own header
says: *"Silence must mean 'refuse', not 'safe'."*

So `planScanPrefilter`/`prefilterSafeExpr` are replaced by one plan-time
analysis in package `optimizer`, exported alongside
`WalkPlanExprs`/`WalkExprTree` in `walk_export.go`:

```
// ScanQualPlan reports how a scan may evaluate qual.
type ScanQualPlan struct {
    Early   bool // safe to evaluate on the deformed prefix
    MaxCols int  // exclusive upper bound on column indexes read
}
func PlanScanQual(qual Expr, ncols int) ScanQualPlan
```

**The mechanism the first draft specified was wrong and would have
introduced the exact bug it was chosen to prevent.** It proposed
expressing early-ineligibility as "a `Visit` veto". But `Visit` returning
false **prunes the node's children without aborting**
(`exprwalk.go:300-302`, `:333-335`); only `scopeVeto` or an unenumerated
type aborts (`:311-317`, `:337-342`). Vetoing on, say, `*RowExpr` would
stop the descent and leave any `ColumnRef` beneath it unseen —
`MaxCols` too small, `Early` still true, and the qual then reads an
undeformed cell holding the **previous tuple's** datum. Wrong answer.

The two jobs must therefore be kept strictly apart:

1. **`MaxCols`** — a *full* descent, always. `Visit` **always returns
   `true`**; it only records `max(ColumnRef.Index)`.
2. **early-eligibility** — a closure-side `early = false` flag set as a
   *side effect* of `Visit`, never via its return value. `OnUnknown` and
   a `false` return from `walkExprRefs` also clear it (fail-closed).
   Half the veto list is derived rather than restated: any qual carrying
   a `slotInnerPlan` or `slotSubqRow` child (`exprwalk.go:53-83` — a
   correlated subquery's own plan tree) is early-ineligible by the
   primitive's scope model, reached through `OnScope` under
   `scopeSignal`. Only the leaf kinds (`OuterColumnRef`, `ParamRef`,
   `ExecParamRef`, `CTIDExpr`, `TableOidExpr`, `MergeWholeRowRef`,
   `MergeActionExpr`) and `FuncCall` need naming.

When `Early` is false, `MaxCols` is not read; the test suite asserts
both — a vetoed node wrapping a high-index `ColumnRef` must still yield
a correct `MaxCols`, or `Early=false` with `MaxCols` provably unread.

The veto list is a **performance** classification only. PG places no
volatility or expression-kind restriction on scan quals: the only PG
gates that touch qual placement are `qual_is_pushdown_safe`
(`allpaths.c:3925-3945`, subquery pushdown only), `is_parallel_safe`
(`allpaths.c:749`, which disqualifies the whole relation rather than
relocating the qual), and pseudoconstant quals, which PG moves to a
gating `Result` (`createplan.c:1022`, `:2923`) printed as
`One-Time Filter:`. That last one is checked and is **not** a hazard
here: goopg renders one-time filters off `optimizer.Result.OneTimeFilter`
(`operators_explain.go:637-656`), never off `optimizer.Filter`, so cut 2
cannot absorb one.

`CaseExpr`, `CollateExpr`, `ExtractExpr`, `LikeEscapePattern` — the four
the old whitelist left out as "plausible but unnecessary" — can be
admitted to Early on their merits later, with no correctness argument
attached (§8).

**Census correction.** The first draft said a new hand-written switch
would be pinned as a "51st `walkerPending` site". The pinned population
is **45** (`exprwalk_inventory_test.go:36-42`, `grep -c walkerPending`),
so it would be the 46th — *and only if it lands in `internal/optimizer`*,
because the census globs `*.go` in that package only (`:331-344`). That
is an argument **for** the chosen placement, not a property of the status
quo: today's `prefilterSafeExpr` (`scan_prefilter.go:88-131`) is a 9-arm
hand-written `Expr` switch in package `executor` and is pinned by
nothing.

### 4.4 The error contract: position, and ordering

Today `perr != nil` falls through so `filterOp` raises the error "from
exactly where it did before". With `filterOp` gone the contract must be
discharged directly.

**Position — already satisfied; do nothing, but test it.** `filterOp`
adds nothing to the error: `filterOp.Next` returns `evalExprSlot`'s error
verbatim (`operators.go:595-598`) — no `ExecError.Pos` stamping, no
wrapping. And every `ee.Pos = o.pos` in `seqScanOp` is in `Open`
(`operators_storage.go:1433, 1454, 1633, 1639, 1668, 1682, 1697`);
`Next` (1851-2280) references `o.pos` **zero** times. (The first draft
implied a stamping site in `Next` had to be avoided; there is none.) The
position comes from the expression node under either caller. A test
pins `Pos` and `Code` byte-identical to the pre-change build.

**Ordering — a real divergence, and the first draft cited it wrongly.**
The draft said a later stage of the same row can *raise* an error that
today surfaces before the qual error. Wrong: `DetoastRowBound` failure is
a **skip**, not a raise (`operators_storage.go:2146-2153`, `continue //
skip undetoastable tuple`), as are `decodeScanRowRange` failures
(`:2085-2090`, `:2104-2110`, `:2117-2122`) and `PageGetHeapTupleInto`
(`:1962-1970`). The only genuine post-deform raise in `Next` is the GiST
SSI conflict-out at `:2166-2174`.

The conclusion survives and is in fact *load-bearing for the skip case*:
today a row that would error on the qual may instead be **silently
dropped** by a failed detoast, and the drop wins. Early evaluation would
invert both that and the GiST 40001 ordering. So: **a qual error at the
early position does not abort the scan — it promotes that row to late
evaluation**, where the error is raised at exactly the pre-change point
in the sequence. Same mechanism as `needsDetoastPrefix`; sound because
early-eligible expressions are pure (§4.1).

**This freezes a goopg ordering that PG does not have, and that is
deliberate.** In PG the qual is the *first* thing to touch the tuple
after the AM (`execScan.h:195 → :214 → :223`, projection only at `:234`),
so a qual error always wins; there is no "later stage" to lose to. Cut 2
keeps goopg's order so that the executor move is behaviour-preserving on
the error path. Converging on PG's "qual first" ordering is a separate,
observable change and gets a ledger row (§8).

**PG's only guaranteed qual ordering is intra-list, and it is a security
property goopg does not implement.** `order_qual_clauses`
(`createplan.c:5387-5404`, called by every scan-plan builder including
`create_seqscan_plan` at `:2921`) sorts clauses by `security_level`
first, cost second, with a leakproof exception gated on cost
(`:5455-5460`); `EEOP_QUAL` short-circuits, so the order decides *which*
qual error surfaces. goopg has no analogue — `grep -rn
"order_qual_clauses\|securityLevel\|leakproof"` over `internal/optimizer`
and `internal/executor` finds only pg_proc catalog plumbing. Cut 2 does
not create this gap, but it moves the qual to the node where PG's
contract lives, so it is recorded here and filed (§8). The early/late
split must not reorder clauses relative to whatever order goopg does use.

**A test per absorbed abstain path**, per the brief:
`TestScanQualLateOnDetoastPrefix`, `TestScanQualLateOnRewrittenColumn`
(enum plus all three aclitem columns), `TestScanQualLateOnUnknownExprKind`,
`TestScanQualLateOnIneligibleExprKind`,
`TestScanQualErrorPositionMatchesFilterOp`,
`TestScanQualErrorLosesToDetoastSkip`,
`TestScanQualErrorOrderingVsGistSSIConflict`.

### 4.5 Instrumentation

`seqScanOp` implements `filterRemoveCounter` and increments on every qual
rejection, early or late. Three corrections to the first draft, all
mechanism-level:

1. **The counter lives on the SCAN node's `nodeStats`**, not the Filter
   node's — PG's `nfiltered1` lives on the scan (`execScan.h:245`), the
   JSON renderer already reads it there (`operators_explain.go:1821,
   :1840`), and it is the only choice that fixes both formats. The
   **text** walker is changed to add the surviving node's own
   `filterRejected` to `fr` alongside the collapsed Filter node's
   (`:1566-1568`), so a `Filter{Scan}` whose qual the scan absorbed and
   one whose `filterOp` survives both report correctly. §3.2 defect 2
   closes as a side effect. The §4.5 test asserts **both** text and JSON
   — this is the documented sibling-path failure mode and the first
   draft walked into it.
2. **Wire the pointer explicitly, not through `maybeInstrument`'s
   interface probe.** The SeqScan arm already returns
   `maybeInstrument(p, op)` (`executor.go:283`), so under ANALYZE the
   Filter arm's `child` is an `*instrumentedOp`, and `instrumentedOp`
   implements no `setFilterRemoveCounter` forwarder (its method set is
   `underlying/Schema/Open/accountBuffers/Next/Close/RowsAffected`,
   `instrument.go:110-225`). `maybeInstrument`'s probe
   (`instrument.go:446-448`) would therefore silently wire nothing and
   *regress* `TestExplainAnalyzeRowsRemovedByFilter`
   (`explain_analyze_test.go:114-132`) instead of fixing §3.2 — and
   double-wrap the scan besides. The Filter arm already calls
   `unwrapSeqScanOp(child)` (`executor.go:557-568`), which peels exactly
   one `instrumentedOp`; the counter pointer is handed over there.
3. **Fold worker counts.** `foldGatherWorkerStats` (`instrument.go:399-425`)
   gains `filterRejected`, mirroring `instrument.c:187`, or §3.2 is fixed
   only for serial plans — and TPC-H's are all parallel.

`buildRec` gets **none** of this: it never calls `maybeInstrument`
(`executor.go:571-712`), its `OpFilter`/`OpSeqScan` nodes carry no
`nodeStats`, and `filterOpNext` (`opnode.go:807-838`) has no rejection
counter at all. The sibling-path discipline in §4.7 applies to the qual
absorption, **not** to §4.5.

**A counter §4 must not break: `statReturned`.** `operators_storage.go:2273`
increments it just before the return and `recordRelScan` (`:1844`) feeds
it to pg_stat's `tuples_returned` (PG's `seq_tup_read` semantics —
tuples *read*, not tuples surviving the qual). If late evaluation
`continue`s above that line, `tuples_returned` silently becomes post-qual
on every scan — a pg_stat regression on a wider surface than EXPLAIN.
**The increment must be moved above the late qual**, and the early
rejection path must increment it too (today's prefilter `continue`
already under-counts it — an existing defect this slice closes).

### 4.6 What else absorbs a responsibility

`filterOp` is structurally matched at **ten** sites. Nine are
pass-through recursions verified to terminate safely with the node
absent, each already handling `*seqScanOp` or falling through:
`attachParallelScan` / `attachParallelBitmapScan` /
`attachParallelIndexScan` (`parallel_scan.go:118-121, 195-198, 329-332`),
`collectBitmapScans` (`operators_gather.go:154`), `collectShareableJoins`
(`parallel_hash_build.go:283`), `markSortWantCTIDs` (`operators.go:1176`),
`findScanLeaf` (`operators_lockrows.go:306`), `findScanLeafForRel`
(`:421`, missing from the first draft's census) and
`markJoinPreserveCTID` (`:540`).

The tenth is load-bearing and is a **fourth absorbed responsibility the
E-17 row does not name**: `findFilterPred` (`operators_lockrows.go:171-181`)
recovers the scan-level qual **off the `filterOp`** — its only arms are
`*filterOp` and `*projectOp`, with `default: return nil` — and feeds
`o.filterPred` (`:735`) → `epqRecheckFilter` (`:1661-1678`). Delete the
node and EPQ recheck silently becomes a no-op: **wrong rows under
`SELECT … FOR UPDATE` with a concurrent update**, not a performance miss.
This is why the slice cannot be done by deletion alone. Three riders:

- `findFilterPred` must read the absorbed qual off `*seqScanOp`, and must
  also peel `*instrumentedOp` — unlike `findScanLeaf` (`:539`) it does
  not, so EPQ recheck already degrades silently under EXPLAIN ANALYZE.
  Fixed in the same touch.
- **EPQ recheck must force the LATE position.** Its input is a
  materialised replacement tuple, not the scan's partially deformed
  buffer, so the `MaxCols` prefix reasoning does not apply. PG does the
  same for its own reasons: `ExecSeqScanEPQ` deliberately forgoes the
  specialised variants (`nodeSeqscan.c:188-191`, *"EPQ doesn't seem as
  exciting a case to optimize for"*).
- **Do not repeat the claim that this mirrors PG.** The existing comment
  at `operators_lockrows.go:754-755` ("matching PG's EvalPlanQual
  re-running the whole plan") overstates it. PG re-instantiates and
  re-runs a *second copy of the whole plan subtree*: `EvalPlanQualStart`
  builds a child `EState` and `ExecInitNode`s the plan
  (`execMain.c:3002-3016`), `EvalPlanQualNext` is `ExecProcNode` on it
  (`:2920-2930`), `ExecScanFetch` substitutes `relsubs_slot` and applies
  only the *access-method* recheck (`execScan.h:82-104`; `SeqRecheck`
  returns unconditional `true`, `nodeSeqscan.c:90-97`) — the **qual**
  recheck is the ordinary `ExecScanExtended` loop, over the whole ordered
  clause list, for every node below LockRows. goopg's single recovered
  `filterPred` plus the `Cond` fold (`:790`) is a narrower approximation.
  `filterPredMaxColRef`'s `>= len(o.filterCols)` guard (`:745`) is
  unchanged.

**Cancellation.** `filterOp` checks `ctx.Err()` every 4096 rejections
(`operators.go:578-584`); `seqScanOp` checks it at every block boundary
(`operators_storage.go:1880-1884`), bounding the same latency to one page
of tuples, and it already covers today's prefilter rejections. No change.

**Per-tuple memory reset.** PG frees the qual's per-tuple context
immediately after each rejection (`execScan.h:250`). goopg has no
per-tuple context; the equivalent boundary is the per-page arena reset
(`operators_storage.go:2286-2288`), unchanged by this slice because both
qual positions sit inside the page loop. Recorded so the absence is
deliberate rather than unnoticed.

**`SetBorrow` is dead prose.** `executor.go:150` and `:161` describe a
`SetBorrow` propagation; no such method exists on any operator
(`grep -rn SetBorrow internal/executor/*.go` returns only those two
comments). Delete the arm without carrying the comment forward.

### 4.7 Scope

Both build paths take the identical qual-absorption change: `buildNode`
(`executor.go:141`) and `buildRec` (`executor.go:599`, the live-server
`BuildFastIterator` path parallel workers use). A fix in one and not the
other is this codebase's documented sibling-path failure mode. `buildRec`
additionally drops the `OpFilter` node and its `filterState`/`predIdx`
(`opnode.go:807-838`) for the absorbed case.

Out of scope: `Filter` above anything other than a `SeqScan` (`filterOp`
stays, untouched); `IndexScan.Cond` / `BitmapHeapScan.Cond`, already
scan-resident; `Result.OneTimeFilter` (§4.3, structurally unreachable
from here); deleting `optimizer.Filter` from the plan (§8).

## 5. Why cut 2 is safely achievable

The wrong-answer direction — a qual that wrongly says *false* drops a row
nothing above can recover — is guarded today by the whitelist, the
`MaxCols` bound and `poisonDeformTail`. All three survive. The bound and
the poison are unchanged, and `deformBoundBelow`'s Filter arm folds
`p.Predicate` off the **plan** node (`scan_deform.go:227-228`), so
keeping the node keeps `survivorBound` covering every qual column and
`poisonDeformTail` (`operators_storage.go:2113`) valid — late evaluation
reads only deformed cells. The whitelist is replaced by a strictly
stronger mechanism: an exhaustiveness-gated primitive that aborts on the
unknown, rather than a hand-written switch that must remember to.

Every case the old design handled by "let `filterOp` decide alone" is
handled by "decide late, on the row `filterOp` would have seen" — same
values, same evaluator, same error, at most one verdict. Nothing is
suppressed and no divergence is preserved. The two reviews found
mechanism errors (§4.3, §4.5) and falsified one claim (§3.1's ANALYZE
half), but no blocker to the shape.

## 6. Measurement

The E-17 row says *measure before cut 2*, and specifically build the
**unmeasured low-selectivity case** — few columns read, most rows
admitted — because it is the only shape where today's prefilter is a
pessimisation. That arm is built first, before any of §4 lands:

- `BenchmarkScanQual/{q6like,lowsel}` × `{today,absorbed}`, serial
  (`-parallel-workers 0`: TPC-H bench plans are all parallel and a
  parallel scan short-circuits parts of this path).
- Q6 (~2 % survival): the prize is one predicate evaluation on 2 % of
  6 M rows plus one node's per-row overhead — expected small.
- Low-selectivity (~90 % survival, e.g. `l_shipdate > '1992-01-01'`):
  today this pays a full extra evaluation on 90 % of rows for zero
  deform saving. This is where cut 2 should show, and if it does not,
  that is reportable.
- A/A the unchanged binary first; hold server age constant; verify
  binary provenance by marker string; `GOOPG_ANALYZE_SEED=20260905` and
  ANALYZE in the measuring session (goopg statistics are per-connection).

## 7. Gate

- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`, `go vet`
  (never `-count=1`).
- Executor + optimizer suites; the seven §4.4 tests; the §4.5
  instrumentation test asserting **text and JSON** and a parallel plan;
  a §4.3 test that a vetoed node wrapping a high-index `ColumnRef` does
  not under-count `MaxCols`.
- **Two existing tests break by design and must be updated in the same
  commit**: `phase_c_test.go:352-358` asserts `Filter{SeqScan}` builds to
  `OpFilter`, and `:978-980` fails with "expected OpFilter below
  top-level node". Both are build-shape pins of the node this slice
  deletes.
- A §3.1 ANALYZE-shape test on a **non-whitelisted** qual (e.g.
  `WHERE lower(c) = 'x'`) asserting `actual rows` = survivors and
  `Rows Removed by Filter` = rejects — the surface the plan gate cannot
  see.
- TPC-H digest 24/24 MATCH **on ordered value hashes** (`ordered=`), not
  row counts: if predicate evaluation moves, a dropped row or an
  ordering change is the failure mode and row counts do not see it.
- TPC-DS SF0.5 sweep: PASS=95 MISMATCH=0 CKMISMATCH=0 TIMEOUT=0
  (order-sensitive checksums).
- Plan gate as a **regression check** against the newest pin,
  `plan_snapshots/c20a-c06s-plancost-rows-20260907.txt`, via
  `make plan-diff LABEL=<pin>` (not `make plan-gate`, whose `ls -t`
  baseline pick is unreliable in a worktree), captured with
  `PLAN_DB=tpch PLAN_USER=tpch PLAN_PORT=65433` (with `postgres` the
  per-table ANALYZE fails and writes a ~1.7 KB pin that pins nothing; a
  real 22-query pin is ~31 KB). Expected **empty diff** per §3.1; a
  non-empty diff falsifies §3.1 and stops the slice.
- PG-parity re-check (`pg-plan-parity-diff`) unchanged-or-better.

## 8. Deferred (ledger rows)

- **Lazy per-datum detoast inside the evaluator** — PG's actual model
  (`fmgr.h:248,292,309`); goopg's whole-row `DetoastRowBound` before the
  predicate is the divergence, and it is what forces the toasted-prefix
  row to late evaluation (§4.2). Supersedes the first draft's
  "prefix detoast with a second buffer", which was solving the wrong
  problem.
- **Qual-first error ordering** — converge on PG's order, in which a
  qual error always precedes any later per-row stage, replacing the
  promote-to-late compatibility rule (§4.4). Observable; needs its own
  values gate.
- **`order_qual_clauses` equivalent** — clause ordering by
  `security_level` then cost, with the leakproof exception
  (`createplan.c:5387-5404`). goopg has none, so RLS / `security_barrier`
  leak-prevention has no analogue and the qual-error order is arbitrary
  (§4.4).
- **Non-boolean qual result should raise, not drop** (§4.1).
- **Early-eligible `CaseExpr` / `CollateExpr` / `ExtractExpr` /
  `LikeEscapePattern`** — performance only (§4.3).
- **Delete `optimizer.Filter` above scans from the plan**, carrying the
  qual in `SeqScan.Qual` with the post-qual estimate on the scan — true
  structural parity, PG-faithfully rows-neutral (§3.3), deferred on
  blast radius.
- **Absorb the qual into `IndexScan` / `BitmapHeapScan` /
  `IndexOnlyScan` the same way** — they already carry a `Cond`, so the
  `filterOp` above them is the same duplicate-node shape.
