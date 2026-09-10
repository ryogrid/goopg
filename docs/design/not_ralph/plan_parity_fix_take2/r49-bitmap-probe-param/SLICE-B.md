# R49 Slice B — implementation plan (written 2026-09-10, pre-implementation)

*Design: `DESIGN.md` (§2). Step-0 pair + Q5 witness archived in this dir.
Slice-A report: `SLICE-A.md`.*

## 0. Contract change (MOVE, not copy — R48 doctrine)

Today `createNestLoopBitmapJoinPlan` (`internal/optimizer/createplannl.go:454-512`)
clears `bhs.BitmapQual` (`:486`) and folds the probe clauses into the join
Predicate as outer-left key pairs (`:499-509`); the OP1-3 guard
(`createplannl_bitmap_recheck_test.go`) pins that. Slice B moves the recheck
down onto the probe and drops it from the join:

1. **Planner** — build the pairs once via `in.keyPairs` (same call as today,
   so no new panic surface on non-equijoin probes); `bhs.BitmapQual` becomes
   one `&BinaryOp{Op: parser.OpEq, Left: kp.Right, Right: kp.Left}` per pair —
   inner-left, in merged outer++inner coords (`keyPairs` already
   `translateToLayout`s both sides into `in.lay`). Inner-left because PG's
   line reads `Recheck Cond: (ss_item_sk = item.i_item_sk)` (inner column
   first) — the same orientation `formatIndexCondParts` produces for Slice-A's
   `Index Cond:` by construction (index column first), so the two lines agree
   exactly. `Predicate` becomes `in.joinPredicate(kind, nil, p.Residual)` —
   residual-only; in the corpus equi-probe shape `p.Residual` is nil and
   `combineAnd(nil)` is nil, so the join line vanishes (PG: no join line).
   Non-probe residuals (extra join quals) stay in the Predicate and keep their
   driver-side eval on `virtualOut` — unchanged behavior.
2. **Executor recheck** — `bitmapHeapScanOp.BindOuter` (`operators_bitmap.go:367`)
   retains slot+width (forwarding to the producer as today) and `evalBitmapQual`
   (`:905-922`) evaluates against the combined outer++inner row (NLI
   `virtualOut` pattern: mem-slot sources `[outerMS, innerMS]`, cols
   `[(0,0..outerW-1),(1,0..innerW-1)]`, inner mem-slot row set to `o.scanRow`
   at the top of `evalBitmapQual` — one place, covering `fetchOneTuple`,
   `fetchExact`, `nextLossyTuple`, hence serial + parallel). Standalone
   bitmaps never receive `BindOuter`, so `outerSlot == nil` keeps the legacy
   inner-row `evalExpr(qual, o.scanRow, …)` — leaf-local BitmapQuals
   (the normal non-parameterized shape) are untouched. The combined mapping is
   full outerW+innerW regardless of published schema, so SEMI/ANTI inners (if
   any reach this arm) get the same treatment as INNER.
   Two-slot discipline (review note 3): `Cond` stays leaf-local against
   `scanRow`; only `BitmapQual` moves to the combined slot — the two qual
   sets resolve against different slots by construction. A doll-house case
   with BOTH a leaf `Cond` and a probe clause pins it.
   Width assumption (review note 2): this works because
   `BitmapHeapScan.Output()` is the full leaf schema
   (`createplanbitmap.go:42-53`), so merged-coord inner refs alias table
   ordinals 1:1 and `scanRow` (length `len(tbl.Columns)`) is
   dimension-compatible — named in a code comment, locked by a width assert
   (or the deform-superset pin) on NLI-bitmap inners, since a future
   projection pushdown between table and heap `Output` would silently break it.
   And/Or needs no handling (review note 4): `bhs.Outer.(*BitmapIndexScan)`
   (`createplannl.go:470`) hard-panics on And/Or inners, so parameterized
   probes under And/Or never reach this arm (corpus mapping confirms: all 28
   are direct `Bitmap Index Scan on *_pkey`); the existing producer-tree
   `BindOuter` forwarding is dead-but-harmless here. Parallel coverage is
   structural only (review note 5): NLI inners never carry `pbm`
   (`attachParallelBitmapScan` has no NLI case, `parallel_scan.go:199-228`;
   `terminatesPartial` stops at NLI, `parallel.go:496-510`) — no parallel-NLI
   census to spend.
   `Rescan` also resets the page-walk cursors (`inLossyPage`/`lossyOff`/
   `parOffsets`/`parOff`/`parRecheck`, not just `tbm`/`iter`) — serial
   self-heals via the `pinned == nil` guard, but SEMI early-exit
   (match → `innerExhausted` → next-outer `Rescan`) exercises stale cursors;
   the SEMI pin below covers it.
3. **NULL-key empty probe (Slice-B merge blocker, DESIGN §0)** —
   `lookupKey`/`lookupKeys` (`:214-258`) return `(nil,nil,nil)` on a NULL key,
   indistinguishable from the key-less full scan (`lookupBounds :209-210`);
   `buildBitmap` (`:122-167`) feeds that into open-ended `RangeScanWithPos`.
   Thread a null-key flag out of `lookupBounds` (re-evaluating is NOT an
   option — double-eval of volatile key exprs changes semantics) and return
   the TBM empty from `buildBitmap` (sibling index arm returns `ok=false`,
   `operators_index.go:946-947,:972-973`); the empty return preserves the
   `maxEntries` work_mem sizing (cf. `bitmapAndOp`'s bare `&TIDBitmap{}`
   precedent at `:992-993`) so `tbmLossify` sees a well-formed empty bitmap.
   Composite-prefix NULLs take the same path (PG-faithful:
   NULL equality matches nothing); the empty TBM short-circuits before any
   recheck question arises.

Verified readings (2026-09-10, not assumed): nil Predicate is already
match-all in the driver (`evalPredicateSlot`, `operators_nljoin.go:342-344`)
and renders no join line (`operators_explain.go:866-868`) — the corpus shape
needs no new nil-handling. `Index Cond:` keeps the Slice-A source (bound keys
via `formatIndexCondParts`, NOT `bis.Pred`) — no planner/renderer work there.
Deform needs NO change (`executor.go:256,480-490` already folds BitmapQual
refs; a test pins the bound is a SUPERSET vs the Predicate-era bound, i.e.
widening only).

Landing rule: all three PLUS the OP1-3 contract update land in ONE commit
(review note 7 — the old guard red-fails on the new planner output, so the
test edit is commit-atomic too). Planner-without-executor would eval
merged-coord refs ≥ outerW against a table-width row (wrong answers or a
panic, not slow ones); executor-without-planner is safe-but-dead. There is
no safe intermediate.

## 1. Census + adjudication (DESIGN §3.1–3.2)

Same corpora + clone discipline as Slice A (`:5533`/`:5534`, inode-verified
binary, GUCs `work_mem='64MB'`, `max_parallel_workers_per_gather=4`):
post-Slice-B A/B must show each of the 28 in-scope join-level probe `Filter:`s
moved onto its probe (`Recheck Cond:` under `Bitmap Heap Scan` + `Index Cond:`
under `Bitmap Index Scan`, no join line) — moves only, ZERO EXTRA.
Every move read against its PG counterpart; Q5-class shape divergences
recorded, not forced (§4). Step-0 re-pass: DS-Q3 join core
placement-identical to archived `pg-dsq3.txt`; `pg-plan-parity-diff.py` will
STILL report shapediff on Q3 (parallel/estimate gaps, R48 F9 family) — a
hand-pass here must never be read as tool parity.

## 2. Values bind (DESIGN §3.3)

- TPC-H canonical digest 24/24 MATCH vs `bench/tpch/baseline-digests.txt`
  (fresh capped server, `GOGC=100`); spotcheck Q12=2/Q13=34 from the same run.
- TPC-DS SF0.5 sweep all-zero (deferred from Slice A — this slice changes
  executor behavior, so the sweep binds here).
- Lossy-forced doll-house runs agree NLI↔SubPlan (R48 agreement-test
  precedent); ANALYZE attribution moves join→scan (accepted per R48 F10 —
  re-verify no test asserts scan-level removal counts on the moved lines).
- Units: `internal/executor` + `internal/optimizer` suites green (gate run,
  no `-count=1`).

## 3. Pins (DESIGN §3.4 — tests first where a text/contract is pinned)

- OP1-3 contract UPDATE (`createplannl_bitmap_recheck_test.go`): Predicate
  nil-or-residual-only; BitmapQual carries the probe clause in merged coords
  as inner-left `col(3) = col(1)`. The recheck moves, it must not vanish.
- Lossy-page recheck per outer row (the exact regression OP1-3 was written
  against, now through the new channel): force `tbmLossify` (tiny work_mem)
  on a doll-house NLI-bitmap; non-matching rows on lossy pages filtered,
  matching rows kept, per outer row.
- NULL-key in BOTH shapes: single-column full-key index (the leaking shape —
  exact pages, `needsRecheck()==false`, would EMIT without the fix) AND
  composite-prefix probe (the always-rechecks safe shape). NULL *recheck*
  semantics already agree (`evalBitmapQual` NULL→false, same as the
  Predicate) — no test needed there beyond the existing strictness.
- Deform-bound superset coverage for a probe BitmapQual's inner refs.
- Planner end-to-end (probe BitmapQual set in merged outer++inner coords,
  Predicate residual-only) + `Recheck Cond:` render pin (PG-identical text;
  renderer already prints from BitmapQual at `operators_explain.go:767-789`).
- Review-note pins: LEFT-join null-padding untouched (fallback emits without
  probe eval, `operators_nljoin.go:220-225` — safe by construction, pinned);
  SEMI/ANTI through the arm with the full-width combined slot (covers the
  `publishedLayout`-narrows-to-`outerCols` divergence AND the Rescan cursor
  reset), or an explicit INNER-only statement if none reach it; key-less
  bitmap (zero `IndexClauses` → empty BitmapQual, residual-only Predicate,
  full scan per probe — behavior identical to today).
- S6-style end-to-end update if the shape moves under it.
- `zz_probe_bitmap_test.go` (throwaway probe, untracked): graduate into the
  planner e2e pin or delete — it must not survive as an untracked file.

## 5. Gate report (IMPL a16db55 + PINS 3639208, 2026-09-10)

Binaries: `/tmp/pp2/bin/goopg-r49b` (worktree build; inode-verified on both
clone servers). Corpora: `/tmp/pp2/oc-tpch-r49b.txt`, `/tmp/pp2/oc-ds05-r49b.txt`
vs the Slice-A references (`oc-tpch-r49a.txt`, `oc-ds05-r49a.txt`); digests
`/tmp/pp2/dig-r49b.txt` vs `bench/tpch/baseline-digests.txt` + `dig-r49a.txt`.

### 5.1 Census + adjudication (moves only, ZERO EXTRA)

Normalization: header + `(N rows)` counters excluded (counters shift when a
line is added above them).

- **TPC-H: 14 removed, 14 added.** Every removed line is a join-level
  `Filter:` (probe equi-clause, outer-first); every added line is a
  `Recheck Cond:` (inner-first, PG orientation). Per-query:
  Q2(4)/Q5/Q8(2)/Q11(4)/Q16/Q19/Q20 — the TODO in-scope set plus Q16/Q19
  same-shape extras. Zero `Nested Loop` lines added/removed (the join node
  stays; only its Filter line vanishes). Indent-attribution: all 15 corpus
  `Recheck Cond:` lines (14 new + 1 pre-existing) sit under a
  `Bitmap Heap Scan`; orphans 0.
- **TPC-DS: 40 removed Filters ↔ 40 added Rechecks**, balanced per query
  (machine-checked, zero mismatches) across 33 sections incl. all in-scope
  (Q3/Q19/Q21/Q30/Q32/Q37/Q39×2/Q40/Q42/Q49×2/Q52/Q53) plus the Q72 LEFT×2
  and the Slice-A "extra probe" family (InitPlan `$0`, standalone-keyed).
  The 8 remaining diff lines are capture-harness noise: 6× temp-filename PIDs
  inside pre-existing psql ERROR lines (same noise as Slice A) + 2× `(N rows)`
  counters. Indent-attribution: all 40 under `Bitmap Heap Scan`; orphans 0.
- Sample pair (DS-Q19): `Filter: (item.i_item_sk = store_sales.ss_item_sk)` →
  `Recheck Cond: (ss_item_sk = i_item_sk)` — the MOVE, inner-first.
- Step-0 re-pass: goopg DS-Q3 join core (`Nested Loop` → outer `Seq Scan on
  item` + `Filter: (i_manufact_id = 816)` → inner `Bitmap Heap Scan` +
  `Recheck Cond: (ss_item_sk = i_item_sk)` → `Bitmap Index Scan` +
  `Index Cond: (ss_item_sk = item.i_item_sk)`) is placement-identical to
  archived `pg-dsq3.txt`. `pg-plan-parity-diff.py` will STILL report shapediff
  on Q3 (parallel/estimate gaps, R48 F9 family) — hand-pass, not tool parity.

### 5.2 Values bind

- TPC-H canonical digest (`cmd/tpch-runner --digest`, fresh capped `:5533`
  at `GOGC=100`): **24/24 MATCH vs `baseline-digests.txt` AND 24/24 vs
  `dig-r49a.txt`** (values, ordered digests included — row order unchanged).
  Tripwires from the same run: **Q12=2/Q13=34 (canonical)**.
  (`scripts/tpch-spotcheck.sh` SKIPs in this worktree — no bench data dir
  here; the fresh-restart + canonical-count gate above is its manual fallback,
  with stronger evidence: full 24-query value digests, not just 2 counts.)
- TPC-DS SF0.5 sweep (`SF05_NO_BUILD=1`, scratch results, engine
  `c50fccd231eaea44` running==on-disk, no VOID): **PASS=95 (57 ck-verified),
  MISMATCH=0, CKMISMATCH=0, ERROR=0, TIMEOUT=0, SKIP=4** — all-zero, gate
  binds. The 4 skips are oracle-side (`Q4 TIMEOUT`, `Q36/Q70/Q86
  SKIP_QUERYGEN`), not engine behavior. Report:
  `/tmp/pp2/sf05-r49b-results/sweep-20260910-143716.txt`.
- Lossy agreement: exact-vs-lossy on the doll-house NLI-bitmap
  (`TestNLIBitmapProbeLossyRecheck`, maxEntries=16 < 5000 TIDs structurally
  forced) + cond+recheck under lossy — closes the merged-coord correctness
  loop with OP1-3's orientation pin. No separate NLI↔SubPlan agreement file
  exists for bitmap probes; the R48-precedent requirement is met by
  exact-vs-lossy on the same shape.
- ANALYZE attribution (join→scan move, accepted per R48 F10): no test asserts
  scan-level removal counts on the moved lines — `internal/executor` suite
  green covers it.
- Units: `internal/optimizer` + `internal/executor` suites green (gate runs,
  no `-count=1`).

### 5.3 Sibling-path audit

- Planner arm is jointype-agnostic (`planJoinTypeFor`; BitmapQual built
  identically for INNER/SEMI/LEFT/ANTI — no per-type switch to miss). SEMI
  pinned at contract level, LEFT pinned e2e; ANTI shares SEMI's path and never
  reaches the arm in the corpus.
- Executor recheck has ONE choke point (`evalBitmapQual`, `recheck &&` gated
  at both fetch sites — PG-faithful skip on exact pages): bound →
  combined-slot `evalExprSlot`, unbound → legacy `evalExpr` on scanRow. The
  parallel path (`nextParallelTuple` → `fetchOneTuple`) funnels through the
  same point. NULL→false agrees in both branches. Two-slot discipline
  (`Cond` stays leaf-local at both fetch sites) is pinned e2e.
- `lookupBounds` nullKey (empty TBM) vs sibling `operators_index.go` ok=false:
  consistent NULL-matches-nothing semantics, both pinned; no drift.

### 5.4 Provenance notes

- `:5533`/`:5534` were held by stale Slice-A `goopg-r49a` servers (same
  session, 12:52/12:58); stopped via `goopg stop -D`, ports verified free
  before launch. First r49b launch's wrapper task was TaskStop'd, which killed
  the server (start foreground-blocks) — relaunched and left running; lesson
  recorded for the next gate run.
- Mid-gate, an SF0.5 `sweep` from the main tree rebuilt `GOOPG_BIN` in place
  (main-tree image clobbered `/tmp/pp2/bin/goopg-r49b`); caught by mtime,
  binary rebuilt from the worktree, sweep relaunched with `SF05_NO_BUILD=1`
  + scratch `SF05_RESULTS_DIR`. Census/digest evidence is unaffected (clone
  servers hold the pre-clobber image — `/proc/<pid>/exe` shows `(deleted)`).
  The sweep image is one docs-only commit (TODO stamp) newer than the
  census image; Go sources identical.

## 6. Files (landed)

- `internal/optimizer/createplannl.go` (BitmapQual-from-pairs + residual-only
  Predicate in `createNestLoopBitmapJoinPlan`)
- `internal/executor/operators_bitmap.go` (heap-op outer-slot retention +
  combined-row `evalBitmapQual` branch + NULL-key empty TBM)
- `internal/optimizer/createplannl_bitmap_recheck_test.go` (OP1-3 contract update)
- New: lossy-recheck + NULL-key (both shapes) + deform-superset + planner-e2e
  + Recheck-render pins
