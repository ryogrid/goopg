# R68 SCOPE — join-order costing, Step 0: re-measure on post-R66 numbers (2026-09-11)

Ordered by the R67 triage: join-order is the dominant untouched blocker
(TPC-H `join-order=14` on `pp66-s2cpost.txt`; TPC-DS ~95, ROADMAP), and
the R51→R53 lineage named its costing half. This round is MEASUREMENT
ONLY (R53 precedent): re-locate the divergence on current numbers, then
scope the pricing slice. No planner/executor/costing change.

## 0. Baseline and what went stale

- R53 Step-0 (2026-09-10, `r53-q9-costing-step0/REPORT.md`): Q9 diverges
  at L4 (`{ps,p,s}+lineitem` vs PG `{ps,p,s}+nation`), decided by
  PRICING at L6 composition, hash-vs-hash, 2.5% margin (winner
  616861.02 vs rival 632364.99). Sizing/admission/parameterisation OUT
  with named evidence. Slice-1 (`SLICE1.md`) attributed the margin to
  the spill term from column-count footprint (build-op/row 4× vs PG;
  PG's partition priced no-spill) — width hypothesis CONFIRMED in
  M0076 form (validate the shape, not just the number).
- Stale since: R54 (seed rows, OR-selectivity), R55–R60 (sort/tie-break,
  GatherMerge, worker sizing, index-probe pricing, partial-NL), R61–R62
  (group-count inputs, Yao), R63–R66 (IOS resolver, NLI decline,
  rendering). Q9's current order is UNVERIFIED since R51; the L6 margin
  since R53-Slice-1. K65/K67 + R38 (rejected) reframed the width side:
  goopg has no column pruning (20–32× widths) AND pruning alone is
  insufficient (DatumBytes 48 vs PG ~22 — the `minimize_datum`
  workstream). A "fix the spill term" cut designed from R53's numbers
  would target a world that no longer exists.
- Standing instrument: R53's `DPTRACE cost` line + Slice-1's
  partition attribution (`OuterRelids/InnerRelids` on Path, trace-only)
  LANDED (`f4f1bc058`, `8b77a905b`) — verify presence at implementation
  (cite, don't assume). Step-0 is therefore cheap: trace env + EXPLAIN.

## 1. Step-0 protocol

Question: with candidates open (R51) and current prices, at which DP
level does goopg's pick first diverge from PG's Q9 order (re-verify
the PG side live — do not carry R51's string; if Q9 matches or the PG
order is unverifiable live, STOP and re-scope rather than forcing the
L-table), and does the deciding term live in sizing (`sizeJoinRel`)
or pricing (`addPaths`)? Q9-first is a cost-of-measurement call (R67
sequencing + the standing R51→R53 instrument), not a nearness call —
Q12/Q13 are category-nearer and untouched by this choice.

Method (all FOREGROUND): clean-HEAD binary + private TPC-H clone
(`:55xx`, inode-verified launch, serving probe) with
`GOOPG_PGSHAPED_DP_TRACE=1`; hand-written Q9
(`internal/testutil/tpch` `Queries()[9]`) via psql EXPLAIN, twice
(byte-identical plans required — else the trace is A/A noise, STOP
and diagnose the nondeterminism first). PG oracle: live `:65432`
EXPLAIN (pinned GUCs) for the current PG order + the PG-side L6
partition price if PG exposes it (it does not print DP internals —
the comparison is structural: which partition PG's winner corresponds
to, read off its plan shape). Capture `SHOW work_mem` (plus any cost
GUCs touched) on BOTH engines into the evidence — spill geometry is
uninterpretable without it (R53-Slice-1 §0 admitted the gap; R54
precedent records SHOW).

Exit (all four, R53's adjudication re-run, not carried):
sizing IN/OUT (per-level rows both spines), admission IN/OUT (PG
partition offered? declines at the deciding level?), parameterisation
IN/OUT (`reqouter` on winners), pricing IN with level/arm/margin.
Deliverable: `STEP0.md` with the L-table + one scoped pricing slice.

## 2. Slice menu (decided AFTER Step-0; pre-declared options)

- (a) Pure cost-term fix: the margin decomposes to a mis-transcribed
  term — diff `hashJoinCost`/`mergeJoinCost`/`nliCost`
  (`cost_funcs.go`, `joinsearchnlicost.go`) term-by-term against
  `initial/final_cost_{hashjoin,mergejoin,nestloop}`
  (`postgres/.../path/costsize.c`). Bounded, verdict-visible.
- (b) Width/footprint input fix: the margin is spill-driven at current
  widths — routes to the `minimize_datum`/K65 program (cross-workstream
  dependency, stated not owned) or a bounded planner-side width cut
  with R38's lessons built in (target `NCols`/`AvgVarBytes`, never
  `Width`; K67's insufficiency caveat: pruning alone leaves 72 B/row
  vs PG 22). A spill-term CONSTANT move without the footprint fix is
  overfitting — explicitly out.
- (c) Dominance/pruning divergence: PG's partition loses on
  `add_path` dominance rather than price — scope the comparator
  (`setCheapest`, fractional paths), not the terms.
- (d) Nothing: the margin vanished under current prices — re-baseline,
  record WHICH round closed it (no unattributed closure), close.
- (e) Sizing-input re-scope: sizing decides at current numbers (rows
  moved since R53 via R54/R57-class inputs) — name the moved
  rows/inputs and scope the sizing slice. The Step-0 question permits
  sizing-IN; the menu covers it here, not by rule violation.

Whichever slice (exactly one of §2a–e, no hybrids): falsifiable bar
(named queries move toward PG's order with per-level numbers), ZERO
EXTRA flips, values + sweep bind as usual at implementation (this
Step-0: trace-only, see §5).

## 3. Predictions (measurement round)

- P0: a named diverging level/arm/margin (or a named exoneration:
  margin gone) with the 4-way adjudication — each IN/OUT evidenced,
  none carried from R53's text.
- P1: ZERO plan moves (trace-only by construction; the byte-identity
  gate §5.2 is the proof, not the code reading).
- P2: the slice for the NEXT round scoped with its own falsifiable bar
  (one of §2a–e, no hybrids — a hybrid is two rounds).

Re-audit rule (R59–R66): any P-miss triggers diagnosis before scoping,
not celebration. Match count is NOT a criterion at any point
(conjunction rule).

## 4. Sibling audit (trace channels move together)

Writer (`joinsearchtrace.go`, `pathtrace.go`) ↔ parser
(`internal/testutil/estimateaudit/enumtrace.go`): R53 §4 precedent —
any new line kind updates both or neither (Malformed-counter
discipline). Production-inertness: trace behind
`GOOPG_PGSHAPED_DP_TRACE`, default off, nil-receiver discipline;
proven by gate §5.2 (byte-identical plans trace on/off), not asserted.
No estimator/executor/costing reader may consume the new fields
(trace-only provenance, per R53-Slice-1's `OuterRelids` precedent).

## 5. Gates (Step-0 measurement, all FOREGROUND)

1. `go test ./internal/optimizer/ ./internal/executor/
   ./internal/testutil/estimateaudit/` green (no `-count=1`);
   `go vet` clean.
2. Trace-on/off byte-identical plans on the Step-0 corpus (Q9 ×2 runs
   + a 3-query control: Q6/Q13/Q22 — production-inertness proof; any
   byte difference → STOP, the instrument is not inert).
3. `STEP0.md`: L-table + 4-way adjudication + one scoped slice (§2),
   recording which spotcheck branch was taken (`:65433` free vs
   deferral-carries) for provenance.
4. Agent review (APPROVE* to proceed; REJECT re-scopes).
5. Commit (explicit pathspec: STEP0 + TODO — SCOPE herewith) with
   `-n` + push. NO code change in this round by design; values gates
   (digest/sweep) are vacuous for trace-only work (R53 precedent:
   suites + spotcheck discipline) — spotcheck runs iff `:65433` is
   free, else the standing deferral rationale carries.

## 6. Ledger (carried, not re-litigated)

R51 items 2–3 (Q15a-splice/`.norm` provenance; nullable-side
assertion), R52 §4.2 parallel-admission half, R54 follow-ups, K26
standing (H ~18/DS ~95 until this round re-measures), #6/R61-#4/(b)
watches, NLI staleness comment. The join-order count itself is
re-measured as a side effect (pp is cheap next to a clone launch) but
judged by §3, never by itself.
