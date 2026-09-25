# R54 Step-1 REPORT — S4 path-level veto attribution (Q5/Q9/Q1 + Q84)

Date: 2026-09-10. Instrument binary: `/tmp/pp2/bin/goopg-r54step1`
(tree @ `93fcb7619` + uncommitted Step-1 cut; §7). Baseline-vs-instrumented
EXPLAIN byte-identity ×2 runs each, same capped clones and GUCs as Step-0
(`/tmp/pp2/clone-tpch` :5534, `/tmp/pp2/clone-ds05` :5533,
`work_mem=64MB`, `mpwg=4`). Two trace modes: gate-on/mode-off
(`GOOPG_PGSHAPED_DP_TRACE=1`, producers gated) and gate-on/mode-top
(plus `GOOPG_GATHER_PATHS=top`, producers run every level).
Evidence (tmp-only, NOT committed): `/tmp/pp2/r54s1/`
(`q{5,9,1,84}-s1{base,f,t}.N.plan`, per-phase server logs
`start-*-trace{,-top}.log`). Driver: `run-step1.sh` (tmp-only).
Counts below are per 2 traced runs (halve for per-run); every rel-level
verdict is run-stable (identical lines ×2, zero drift).

Scope gap found, not re-scoped: STEP1 §3 never names `GOOPG_GATHER_PATHS`.
Measured off first (100% V0/M0 — the mode gate, §2), then ADDED top phases
rather than re-scoping, because off alone cannot distinguish the ~10
producer vetoes. The off run reproduces Step-0's "100% no-partials" and
proves it was the mode gate all along.

## 1. Plan identity (instrument inert; mode effects as predicted)

| query | base vs f.1 (off) | f.1 vs f.2 | base vs t.1 (top) |
|---|---|---|---|
| Q5 | identical | identical | DIFFERS (Gather + 2 Parallel Hash Joins, 246521.33→98673.84; serial HashAgg kept on top) |
| Q9 | identical | identical | DIFFERS (Finalize→Gather→Partial + 2 Parallel Hash Joins, 618239.80→451804.20) |
| Q1 | identical | identical | identical |
| Q84 | identical | identical | identical |

All `.1`-vs-`.2` pairs identical in all three arms (12/12 run-stable).
Q1 emits zero `problem` and zero `pveto` lines in every log (nrels=1 never
enters join search — C-19h holds); its two runs are trace-silent with
byte-identical plans. Q84 unchanged under top (Limit top terminates
partials — STEP1's prediction). Merge route files NOTHING even under top
(no M12 anywhere, §3) — Q84's plan is untouched by the mode.

## 2. Off-run veto table (the mode gate, total)

Every hash orientation → V0, every merge call → M0 (both sites).
`detail=jt=INNER` on all 1004+1220+1004 lines — the corpus exercises only
INNER at these sites (V1 jointype filter untested by corpus; covered by
unit pin instead).

| query | hash | merge | mergeu | base admitted | base veto |
|---|---|---|---|---|---|
| Q5 | 380 V0 (190/run) | 476 M0 (238/run) | 380 M0 `sub=gather-off` | customer, orders, lineitem (w=2,4,4) | supplier, nation, region → B4 (w=0) |
| Q9 | 352 V0 (176/run) | 472 M0 (236/run) | 352 M0 | part, partsupp, orders, lineitem (w=2,3,4,4) | supplier, nation → B4 (w=0) |
| Q84 | 272 V0 (136/run) | 272 M0 | 272 M0 | customer, customer_demographics, store_returns (w=1,3,1) | customer_address, household_demographics, income_band → B4 (w=0) |
| Q1 | — (silent) | — | — | — (no base lines: search never entered) | — |

Zero B1/B2/B3 lines anywhere (H1 dead, §5). Zero B4 with workers>0
(H2 exact). `upper`: 4×`split workers=4 divisor=4` (Q9×2, Q1×2) +
2×`refused gate=subtree subtree=no-driving-scan` (Q5×2 — H6, §5).

## 3. Top-run veto tables (producers live)

Base census byte-identical to §2 in every log (mode affects producers,
not base filing). Hash totals reconcile per query (V9+V4 = off V0).

**Q5** (hash 268 V9 + 112 V4 = 380 ✓; merge M7 354 + M3 122 = 476 ✓;
mergeu M7 268 + M3u 112 + M8 78 + M2 52 = 510 — loop fan-out over
candidates, cf. off 380 single-entry):

- V9 `workers=`: 4 ×196, 2 ×72 (customer-led outers take 2, orders/lineitem-led 4).
- V4 outers (clean split at `}+`): {supplier} 26, {region} 22, {nation} 22,
  {nation+region} 20, {nation+supplier} 12, {nation+region+supplier} 10 —
  **every V4 outer is a starved-only set** (0/112 contain an admitted leaf).
  Conversely V9 outers include mixed sets ({lineitem+supplier} 18,
  {customer+supplier} 10, {customer+nation} 12, …): a starved leaf kills
  only starved-LED orientations, never starved-CONTAINING joinrels —
  propagation death is orientation-specific, not relset-specific.
- Merge wrapper: M7 `osort=1` ×182 / `osort=2` ×172 (outer-sort decline),
  M3 ×122 (empty outer partials). Mergeu: M7 ×268 (`cand=` per candidate),
  M3u ×112, M8 ×78 (`isort=1` ×66, `isort=2` ×12), M2 ×52 (`cand=1` ×24,
  `cand=2` ×28). **Zero M12 at either site.**
- `upper`: 6×split (Q5's 2 flip refused→split via the splice arm, §6).

**Q9** (hash 288 V9 + 64 V4 = 352 ✓; merge M7 408 + M3 64 = 472 ✓;
mergeu M7 288 + M8 150 + M3u 64 + M2 34):

- V4 outers: {supplier} 22, {nation} 22, {nation+supplier} 20 — again
  exactly the starved-only sets (Q9's starved leaves; region not in Q9).
  V9 heads: {part} 30, {partsupp} 30, {orders} 24, {lineitem} 20, mixed
  ({partsupp+supplier} 12, {nation+partsupp+supplier} 10, …). Same rule.
- Merge: same vocabulary, no M12. `upper`: split ×2 (as off).

**Q84** (hash 196 V9 + 76 V4 = 272 ✓; merge M7 196 + M3 76 = 272 ✓;
mergeu M7 196 + M3u 76 + M8 26, **no M2**):

- V4 outers: {customer_address} 24, {income_band} 18,
  {household_demographics} 18, {household_demographics+income_band} 16 —
  starved-only sets again. V9 heads: {store_returns} 26,
  {customer_demographics} 26, mixed ({customer+household_demographics} 10,
  {customer+customer_address} 10, …).
- Mergeu M8: `isort=1` ×14, `isort=2` ×12. No M12 at either site (H5).
- `upper`: Q84 has no aggregate split path (Limit-topped; no upper lines).

V6/V7/V8a/V8b: **zero lines in all top logs** — the chain never gets past
the outer-partials gate, so no mid-chain veto names a Step-2 fix (H3).

## 4. Cross-checks (filing → addPath → winning plan)

- Hash: 556 V9 filings (Q5 268 + Q9 288; Q84 196 more in ds05 log) →
  DPPATH `join.hash.partial` 556 EXACT (214 `accepted` / 342 `dominated`).
  Every admitted filing reaches `addPath` exactly once.
- Base: 14 admitted (tpch, both modes) → 14 `scan.seq.partial accepted`
  (both modes — base filing is mode-independent).
- Upper: 6 splits (top) → 6 `upper.partialgroupagg.partial accepted`.
- Off: 0 `join.hash.partial` (V0 precedes construction — nothing built).
- Orthogonal residue (both modes, plan-identical, out of scope):
  24 `index.ordered.partial accepted` per tpch log — provenance not
  chased; mode- and instrument-independent, so not a Step-1 confound.
  Flagged, not explained.

## 5. H1–H6 adjudication

- **H1 (B3 bitmap starvation): DEAD.** 0×B3 in 6 logs. Q5's production-time
  leaves are all `leaf=seq` (S1 re-confirmed by census); final-plan bitmaps
  are the later legacy pick, as STEP1 §0 predicted.
- **H2 (B4 small-table sizing): CONFIRMED, extended.** B4 fires only with
  `workers=0`, always `leaf=seq`: nation/region (trivial) AND supplier
  (10k rows — below `min_parallel_table_scan_size`, consistent with the K14
  worker bands, so provisionally a confirmation not a target; a live-PG
  supplier-led parallel check is owed before calling it faithful). H2 quantifies V4: 100% of V4
  outers are starved-only sets (§3) — the "correct deaths" fraction is
  exactly the V4 row.
- **H3 (V6/V7/V8a/V8b): ABSENT.** Zero lines — no Step-2 slice there.
- **H4 (NL has no partial arm): NO SILENT ORIENTATIONS — complete winner
  census (4/4 NL winners).** Every admitted joinrel orientation emits
  hash+merge lines (coverage total; no `nclauses=0` admits), and each
  serial NL winner maps to a veto, never to silence:

  | winner | orientation (outer×inner) | veto |
  |---|---|---|
  | Q5 NL#1 (spine×lineitem bitmap) | `{customer+nation+orders+region}`×`{lineitem}` | V9 FILED (partial exists, serial won = costing) |
  | Q5 NL#2 | `{nation+region}`×`{customer}` | V4 / reverse V9 filed |
  | Q9 NL (part-tree×lineitem bitmap) | `{part+partsupp+supplier}`×`{lineitem}` | V9 FILED |
  | Q84 NL (top, Join Filter) | 5-rel×`{customer_demographics}` | V9 FILED |

  3 of 4 filed-and-lost (costing/competition, not coverage); the 4th is
  vetoed with a filed reverse. No new-producer slice is indicated by
  Step-1 data. (Basis is the flip world, where Step-2 operates; under
  production everything is trivially V0.)
- **H5 (Q84 merge route): ANSWERED.** Both sites fire (M7/M3/M8/M3u),
  neither admits (0×M12 corpus-wide, all queries). The sort-site wrapper
  dies on outer-sort decline or empty outer partials; the unsorted site
  dies on inner-sort decline / empty pathlist. Gather-Merge is NOT
  recoverable via current producers — needs ordered base partials or the
  open R50 post-pass answer, now quantified per site.
- **H6 (subtree disjunct): CONFIRMED** `no-driving-scan` (2/2 Q5 off runs;
  zero `unsafe`/`gathered` anywhere). First-wins order unit-pinned.

## 6. Step-2 scoping (owed by §4 of STEP1)

1. **V4 slice = starved-led orientations.** Fix candidates per veto, not
   lumped: (a) seed partials for B4 leaves anyway (fights PG-faithful
   sizing — needs a PG-measurement justification, not assumed); (b) accept
   V4 where PG also serialises (supplier-led joins are the test —
   PG's Q5 shape is the oracle); (c) mixed outers already propagate, so the
   slice is SMALLER than "all starved-containing joins". NOT in Step-1:
   pricing, sizing, footprint.
2. **Filed-not-won split (Q5 under top).** The splice arm
   (`gatherToUnwrapForPartialAgg`, Project/Filter-preserving, enabled)
   flips Q5's upper refused→split (`workers=4`), but the final plan keeps
   the serial HashAgg above the join-level Gather while Q9's identical
   split WINS (Finalize→Gather→Partial, §1). Contrast is the pricing
   question Step-2 starts from; the filed split + losing plan are both
   in evidence.
3. **Merge M12 never fires.** Any Gather-Merge ambition needs a producer
   that can order (or a post-pass ruling on the R50 question) — Step-1
   bounds it: 0 admits from 3000+ site calls.

## 7. Instrument (trace-only, production-inert)

`joinsearchtrace.go` (recorder: `tracePVeto` + render loop, nil-safe,
`dir=-` for base; `tracePVetoTag`), `joinpathsparallel.go` (hash V0–V9 +
merge-wrapper M0–M5, `site` threaded), `joinpathsmergeouter.go` (mergeu
M-site loop + M3u, first-veto-wins), `considerparallel.go` (B1–B4 + base
admitted), `partialaggupper.go` (`subtreeRefusalKind`, first-wins),
`estimateaudit/enumtrace.go` (`pveto` discard). Pins:
`TestTracePVetoRenderPins` (5 lines), `TestSubtreeRefusalKind` (5 cases),
`TestTraceAdmissionNilSafe` extended. Gates: optimizer + estimateaudit
suites green (cache-warm on this tree, no `-count=1`), `go vet` clean.
Sibling audit (STEP1 §5): no veto renamed an executor-visible shape —
`createplanjoin.go` untouched; `partialPathDrivingKind`/`drivingScan`
readings recorded in STEP1 §0/H6 only.

Review (APPROVE-WITH-NOTES) applied: V2 `sub=nil-joinrel` (was lumped
into `cp`), mergeu loop-M8 carries `isort=0` (was cand-only, ordering
state unnamed), B2 carries `leaf=` (nil-guarded; `workers=` unavailable
pre-sizing by construction). B1 stays sub-only deliberately: it fires
before the loop with no rel in scope (render-pinned with `rel=-`), so
leaf+workers cannot exist there — a contract deviation, not a gap.
Per-query segmentation method: problem-line brackets (each run prints one
`problem` line then its block; base blocks land mid-search, so rel-name
attribution, not line order, separates Q5/Q9 runs — verdicts identical
across duplicates either way).
