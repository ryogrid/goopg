# R49 — parameterize the bitmap-heap NLI probe

*Agent review: APPROVE-WITH-NOTES (1 merge blocker + 8 notes);
all notes applied 2026-09-10 — see TODO.md R49 entry.*

Named by R48 DESIGN §4 ("IOS/bitmap-`Cond` inners ... (their
double-eval is a separate R)"). Two slices; Slice A first
(EXPLAIN-only, de-risks the census).

## 0. Step-0 — grounded facts (no models, only readings)

Step-0 pair: TPC-DS Q3, captured live 2026-09-10 (PG
`:65438/tpcds05` vs post-R48 corpus, GUCs pinned
`work_mem='64MB'`, `max_parallel_workers_per_gather=4`),
archived in-tree as `pg-dsq3.txt` + `goopg-dsq3-r48.txt`.
Plus `pg-q5.txt` + `goopg-q5-r48.txt` as the shape-divergence
witness (see §4).

- PG: `Nested Loop` with NO join-level line; inner
  `Bitmap Heap Scan ... Recheck Cond: (ss_item_sk =
  item.i_item_sk)` + `Bitmap Index Scan ... Index Cond:
  (ss_item_sk = item.i_item_sk)` — the outer ref is the probe
  parameter.
- goopg r48: same join shape, but the probe clause stays at
  join level (`Filter: (item.i_item_sk =
  store_sales.ss_item_sk)`) over a key-less
  `Bitmap Index Scan on store_sales_pkey` (no `Index Cond:`,
  no `Recheck Cond:` anywhere).

Census on the post-R48 corpora (`oc-tpch-r48h2.txt`,
`oc-ds05-r48h2.txt`): a `Filter:` on a `Nested Loop` is
`NestedLoopIndexJoin.Predicate` (plain `*Join` renders
`Join Filter:` — `operators_explain.go:838-840` vs NLI
`Filter:` `:851-853`),
and every such line whose inner is a `Bitmap Heap Scan` sits
over an UNPARAMETERIZED bitmap — 0/40 TPC-DS `Bitmap Index
Scan`s carry an `Index Cond:` (precise indent-aware census;
the 8 near-misses are plain-`Index Scan` neighbours), TPC-H
likewise. In-scope set: TPC-H Q2(4)/Q5/Q8(2)/Q11(4)/Q20;
TPC-DS Q3/Q19/Q21/Q30(ctr_customer_sk)/Q32/Q37/Q39(×2)/Q40/
Q42/Q49(×2)/Q52/Q53/Q55/Q61(×2)/Q63/Q64/Q75(×2)/Q76(ws_item_sk)/
Q80(×2)/Q81(ctr_customer_sk)/Q82/Q89/Q98 — 28 lines, inner-child
mapping verified per line (inner = `Bitmap Index Scan on
*_pkey`; the mapping table is archived with the Slice-A
census per review note 8). Non-bitmap `Filter:` lines
(Q1/Q6/Q18/Q30a/Q34/Q44/Q46/Q54/Q68/Q71/Q72/Q73/Q76a/Q79/
Q81a — CTE/Hash/Merge/Seq inners, SubPlan/InitPlan quals)
are out of scope (§4).

Mechanism readings (all verified in-tree, not assumed):

- The NLI-bitmap path arm (`createNestLoopBitmapJoinPlan`,
  `createplannl.go:454-530`) binds probe keys per outer row
  already: `bis.Key/Keys` overwritten with outer-layout
  translated keys (`:471-480`), and the executor resolves
  them against the bound outer slot
  (`operators_bitmap.go:101-104` `BindOuter`,
  `:190-258` `lookupBounds`/`lookupKey(s)` via
  `evalExprSlot(..., o.outerSlot ...)`, per-probe `Rescan`
  from the NLI driver (`operators_nljoin.go:321-323`)).
  Parameterized probes EXECUTE today; only their rendering
  + recheck placement diverge.
- The arm deliberately clears `BitmapQual` (`:482-486`,
  "no leaf-local form of = \<outer key\>") and folds the
  probe clauses into the join Predicate (`:499-509`,
  OP1-3 guard `createplannl_bitmap_recheck_test.go` pins
  this: lossy-page recheck rides the Predicate because a
  bitmap does not enforce keys exactly once `tbmLossify`
  degrades pages). That test's contract is UPDATED by Slice B,
  not deleted — the recheck moves, it must not vanish.
- NULL-probe-key hole (review blocker — Slice B MUST fix
  before merge): `bitmapIndexScanOp.lookupKey` / `lookupKeys`
  (`operators_bitmap.go:219-220`, `:247-248`) return
  `(nil, nil, nil)` on a NULL key, and `buildBitmap`
  (`:122-165`) feeds that into
  `RangeScanWithPos(nil, nil, …)` — open-ended = FULL scan
  (`btree.go` `RangeScan` contract: nil bound = open-ended)
  — while the sibling index arm returns explicit
  `ok=false` ("produce no rows",
  `operators_index.go:946`, `:972`). Benign TODAY only
  because the join Predicate discards everything
  (`evalPredicateSlot` NULL→false,
  `operators_nljoin.go:342-355`); Slice B removes that net,
  and on exact pages with `needsRecheck()==false` the
  per-tuple recheck is skipped (`:755`, `:814` gate on
  `recheck`), so non-matching rows would EMIT. Fix: mirror
  the sibling — propagate a null-key flag out of
  `lookupBounds` and return an empty TBM from `buildBitmap`
  (PG-faithful: btree NULL equality matches nothing).
  Composite-prefix probes are safe already
  (`needsRecheck()==true` forces per-entry recheck) and
  lossy pages always recheck (`nextLossyTuple` walks every
  offset with recheck forced, `:599-611`) — but the §3.4 NULL
  test must cover the leaking shape (single-column
  full-key index) AND the safe shape (composite prefix),
  or the guard does not cover the hole. NULL *recheck*
  semantics already agree (`evalBitmapQual` NULL→false,
  `:911-914`, same as the Predicate) — no issue there.
- The executor BUILD side already anticipates a set
  BitmapQual on NLI-bitmap inners: `executor.go:256` threads
  `in.BitmapQual` into the probe's deform bound, `:480-490`
  stamps it. Only the planner arm clears it, and only the
  per-tuple eval lacks outer bindings.
- The per-tuple eval gap, precisely: `evalBitmapQual`
  (`operators_bitmap.go:905-922`) evaluates via
  `evalExpr(qual, o.scanRow, ...)` — inner row only, no
  outer slot — and `bitmapHeapScanOp` does NOT retain the
  bound slot (`BindOuter` `:367-371` forwards to the
  producer only). A BitmapQual carrying outer refs needs
  (a) merged outer++inner coordinates (same
  `translateToLayout` move the arm already makes for keys),
  (b) the heap op to retain slot+width at `BindOuter`, (c)
  combined-row eval (the NLI `virtualOut` pattern —
  sibling-path audit: `fetchOneTuple`, `fetchExact`,
  `nextLossyTuple` all funnel through the single
  `evalBitmapQual`, so one fix covers serial + parallel;
  tests pin the serial lossy path and at least execute the
  parallel one).
- The renderer already prints `Recheck Cond:` from
  BitmapQual (`operators_explain.go:758-763`) but prints
  NOTHING from `BitmapIndexScan.Key/Keys/Pred`
  (`:2685-2693` label only) — hence 0/40.

## 1. Slice A — render `Index Cond:` on Bitmap Index Scan (EXPLAIN-only)

Render the bound probe keys as `Index Cond:` on the
`Bitmap Index Scan` node — from keys+columns via the
existing `formatIndexCondParts(index, keys, key, …)`
(`operators_explain.go:1070ff`); `formatIndexCondKey`
(`:1062-1068`) qualifies outer refs when rtable>1, i.e. it
produces PG's `(ss_item_sk = item.i_item_sk)` shape by
construction, the same mechanism as the working NLI index
probes. NOT from `bis.Pred`: that list is in SEARCH
coordinates and risks wrong qualification (review note 2).
The keys already drive the probe; printing them changes no
executor/estimate behavior — only the missing lines appear
(PG-faithful carrier, same argument as R48 Half-1's
host-line re-pricing).

Probe-test-first (R47 precedent: unit pins before corpus):
a rendering probe test must prove the text is PG-identical
(inner-first ordering per `keyPairs`) before the corpus
census runs.

EXPLAIN-only claim, strengthened (review note 3): the edit
point is the single `emitNodeDetailLines`, shared by the
plain (`:537`) and ANALYZE (`:1700`) walks — there is NO
ANALYZE twin to miss and NO `BitmapIndexScan` case there at
all today (Slice A adds one). No estimate consumer exists
(`cardinality.go` has zero Bitmap cases); an
`attachedFilter` above a `BitmapIndexScan` is unreachable
(builder returns it bare, `createplanbitmap.go:103-122`,
heap asserts `bitmapProducer`,
`operators_bitmap.go:456-460`). ANALYZE "Rows Removed by
Join Filter" attribution moves join→scan — accepted per R48
F10, but re-verify no test asserts scan-level removal
counts on the newly-attributed lines.

Census expectation: every in-scope bitmap inner gains one
`Index Cond:` line; join-level `Filter:`s UNCHANGED (still
rechecking); ZERO other lines move. Values gates bind
(EXPLAIN-only must still be proved, not asserted).

## 2. Slice B — keep BitmapQual, drop the folded Predicate clauses

MOVE, not copy (R48 doctrine):

1. In `createNestLoopBitmapJoinPlan`, stop clearing
   `bhs.BitmapQual`; translate the probe clauses to merged
   outer++inner coordinates (the same `translateToLayout`
   with `outerLay` the arm uses for keys) instead of
   folding them into the join Predicate. The Predicate
   keeps ONLY `p.Residual` — in the corpus equi-probe
   cases that is nil, so the join line vanishes (PG: no
   join line).
2. `bitmapHeapScanOp.BindOuter` retains slot+width;
   `evalBitmapQual` evaluates against the combined
   outer++inner row (NLI `virtualOut` pattern). Deform
   bound needs NO change (`executor.go:256,480-490`
   already folds BitmapQual refs — a test pins the bound
   is a SUPERSET covering a probe BitmapQual's inner refs,
   i.e. widening, never narrowing, vs the Predicate-era
   bound).
3. `Index Cond:` keeps the Slice-A source (bound keys via
   `formatIndexCondParts`, NOT `bis.Pred`) — heap shows
   `Recheck Cond:` from the kept BitmapQual, index shows
   `Index Cond:` from the keys, same text by construction,
   exactly PG's two lines.

Correctness model (PG's, not new): exact pages rely on the
key-bounded probe (no recheck — the TBM entries are exact
TIDs from the range scan); lossy pages + AM-recheck entries
recheck via BitmapQual (existing `tbmLossify` /
`needsRecheck` / per-entry flags, unchanged). Values gates
arbitrate; the OP1-3 test is UPDATED to the new contract
(Predicate nil-or-residual-only; BitmapQual carries the
probe clause in merged coords) PLUS new lossy-page tests
that force `tbmLossify` (tiny work_mem) and prove the
recheck still fires per outer row — the exact regression
OP1-3 was written against, now through the new channel.
Doll-house fixture mirrors R48's (`ord`/`line` shape with
a bitmap-friendly probe + NULL probe keys: NULL key =
empty probe per `lookupKey`, and NULL recheck = no-match
per `evalBitmapQual` strictness — both pinned).

ANALYZE attribution note (accepted): moving clauses off
the Predicate changes "Rows Removed by Filter" attribution
(join → scan). Verified no test asserts per-query
`joinFilterRejected` counts (R48 REPORT §4, F10 closed) —
same disposition.

## 3. Success tests (all must hold)

- §3.1 census: post-Slice-A corpus A/B shows ONLY added
  `Index Cond:` lines on in-scope bitmap inners (join
  Filters stay); post-Slice-B shows each in-scope
  join-level probe `Filter:` moved onto its probe
  (`Recheck Cond:` + `Index Cond:`, no join line).
- §3.2 adjudication: every move read against its PG
  counterpart (Q5-class shape divergences recorded, not
  forced — §4); ZERO shape flips required beyond the
  moved lines, ZERO EXTRA allowed.
- §3.3 values: TPC-H digest 24/24 MATCH vs
  `bench/tpch/baseline-digests.txt`; SF0.5 sweep all-zero;
  spotcheck Q12=2/Q13=34; lossy-forced doll-house runs
  agree NLI↔SubPlan (R48 agreement-test precedent).
- §3.4 pins: renderer probe test (Slice A text); OP1-3
  contract update + lossy-page recheck tests + NULL-key
  tests in BOTH shapes (single-column full-key index =
  the leaking shape; composite-prefix probe = the
  always-rechecks safe shape) + deform-bound
  superset-coverage (Slice B); planner end-to-end (probe
  BitmapQual set in merged outer++inner coords, Predicate
  residual-only); S6-style end-to-end update if the shape
  moves under it.
- §3.5 Step-0 re-pass: DS-Q3 join core placement-identical
  to the in-tree archived `pg-dsq3.txt` (no fresh capture;
  no join line; probe carries both Conds).
  `pg-plan-parity-diff.py` will STILL report
  shapediff on Q3 (parallel/estimate gaps, R48 F9 family)
  — a hand-pass here must never be read as tool parity.

## 4. Non-goals / follow-ups (named, not owned)

- Q5-class shape divergence: PG hash-joins supplier where
  goopg nestloops with a bitmap inner (`pg-q5.txt` vs
  `goopg-q5-r48.txt`). Parameterizing the probe is PG-ward
  (PG renders that shape with Conds when it picks it, per
  DS-Q3) but NOT PG-identical on Q5 — join-choice
  competition is a separate R. NEVER force the shape to
  chase the tool.
- Plain-`*Join` `Join Filter:` residuals (TPC-DS Q7/Q13/
  Q14/Q15/Q17/Q25/...; TPC-H Q7) — separate R per R48 §4.
- Semi/anti over non-scan inners (Q21, Q22), SubPlan/
  InitPlan shapes (Q6/Q17/Q30/Q54...), LEFT (Q72 —
  null-safety, R48 argument), INNER non-bitmap NLI
  residuals (deferred per R48).
- `Join Filter: (true)` (`exists_to_any.go:355-367`):
  still corpus-zero, untouched.
- Firing micro-rule; K100; node/path agg-cost
  unification; TPC-H fixture re-capture (owner).
