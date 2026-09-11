# R68 STEP-0 — Q9 re-measured: NLI won everywhere, price both sides (2026-09-11)

SCOPE: `r68-joinorder-costing-step0/SCOPE.md` (reviewed
APPROVE-WITH-NOTES, notes applied; committed `35e020d`). No code in
this round (tree clean throughout) — the deliverable is this
measurement + one scoped slice.

## 0. Method (SCOPE §1, executed verbatim)

Clean-HEAD binary `/tmp/pp2/r68/goopg-r68` (md5 `40879558…`) + private
TPC-H clone (`/tmp/pp2/clone-tpch-r65` :5533, inode-verified launch,
serving probe, `GOOPG_ANALYZE_SEED=20260905`); hand-written Q9
(`Queries()[9]`, saved `/tmp/pp2/r68/q9.sql`) via psql EXPLAIN, twice —
byte-identical (`q9-plan1|2.txt`), so the trace is stable — and the two
trace halves of `dppath.txt` (1240+1240 lines) are md5-identical, so
the trace itself repeats exactly, not just the plans. Controls
Q6/Q13/Q22 with trace on AND off — all four byte-identical
(`ctl/` + `q9-mp0.txt`), so the instrument is production-inert
(P1's gate, not asserted). Q9 with `max_parallel_workers_per_gather=0`
in session is byte-identical to default — the session asymmetry
(goopg default 4 + `GATHER_PATHS=off` vs PG 0) is immaterial (no
Gather in any plan; partials discarded either way). `SHOW work_mem`:
64MB on BOTH engines (`show-goopg.txt`, `show-pg.txt`); PG oracle plan
`pg-q9.txt` (serial, 64MB). Evidence tmp-only `/tmp/pp2/r68/`
(`dptrace-cost.txt` — first line `58` is the counter print of this
file's own extraction script, not trace content).

## 1. Step-0 numbers (both runs identical)

goopg winner spine (DPTRACE cost; `p=part s=supplier l=lineitem
ps=partsupp o=orders n=nation`; relids 0=p 1=s 2=l 3=ps 4=o 5=n) —
each row the relset the WINNER is built from (via-composition stated;
rev-1 listed non-ancestor relsets carried from R53's text):

| lev | winner relset (ancestor of winner) | rows | winner | total |
|-----|------------------------------------|------|--------|-------|
| L2 | {p+ps} | 40404 | hash | 37095.30 |
| L3 | {p+ps+s} | 40404 | nli | 37876.77 |
| L4 | {n+p+ps+s} | 40404 | hash | 38433.89 |
| L5 | {l+n+p+ps+s} | 303093 | nli | 96778.63 |
| L6 | full, via `{l+n+p+ps+s}⋈{o}` | 303093 | **nli** | **117342.97** |

The L6 NLI offer carries `outer={0,1,2,3,5} inner={4}` with
`inputtotal=96778.63` — the L5 relset above — and the rendered plan
top (`Nested Loop cost=6777.82..117342.97`, `q9-plan1.txt:7`) is that
path verbatim. L6 ladder (DPPATH, all offers): NLI inner={4} accepted;
hash PG-orientation (`outer={4}, inner={0,1,2,3,5}`) 528266.23
dominated (336940.93 above its 191325.30 startup); flipped-hash
539118.03 dominated (373109.58 above 166008.45); merge 439296–991043;
plain-NL billions (no index). PG live (`:65432`, serial, 64MB —
`pg-q9.txt`): Sort → HashAggregate → **Hash Join**
(`o_orderkey = l_orderkey`, outer = 1.5M-row Seq Scan on orders,
inner = hash of the ~302k lineitem subtree; top
146504.58..147406.46) — i.e. PG's partition IS the
`outer={4}`-hash offered above, losing on price. PG order re-verified
live (R51's string retired as required — it matches, but the match is
measured, not carried).

## 2. Adjudication (re-run, nothing carried)

- **Sizing: OUT.** L6 is a single result relset (303093, no
  information); L5 ties at 303093 vs PG's pre-orders subtree 301659
  (`pg-q9.txt`, Nested Loop rows — file-cited, not carried). R53's
  argument with new numbers.
- **Admission: OUT.** Zero lev-6 declines (69 total: 24/24/15/6 at
  L2–L5, all `no-join-clause` on clauseless splits; every relset on
  both spines still forms). PG's L6 partition offered in both
  orientations, losing on price.
- **Parameterisation: OUT.** `reqouter={}` on the L6 winner and every
  L4–L6 winner (NLI winners are unparameterised paths).
- **Pricing: IN — two-sided.** Hash side (PG's partition): 528–539k
  total (336940.93 / 373109.58 above the respective startups) — the
  K65 width/spill term (1.5M `orders` rows at 448 B/col-count widths
  vs PG's 14 B → multi-batch at 64MB; PG prices the same join ≈98k:
  top 146504.58 minus outer scan 42814.00 and inner build, `pg-q9.txt`).
  NLI side (winner): join-added (117342.97 − 96778.63)/303093 ≈
  **0.068/probe** for a unique-index probe + heap fetch — whose
  faithfulness vs PG's `cost_nestloop`/rescan pricing is UNMEASURED
  (PG never prices this shape: it picked hash). (An earlier draft
  quoted 0.365/probe — that amortizes the outer input run cost into
  the probe; the join-added figure is 0.068. Corrected on review.)

R53's L6-hash-arm 2.5% question is SUPERSEDED, not answered: NLI took
over L3–L6 winners since (hypothesis: R59/R64 NLI repricing — same-
relset NLI moves measure ~2×, e.g. L4 200738→96222, so stated as
hypothesis with magnitude, not fact; hash still wins most L2 cells).
Location (L6 top join) is stable; numbers and arms are new.

## 3. Scoped slice: (a) NLI probe-term audit

**Slice (a): attribute goopg's 0.068/probe join-added price against
PG's price for the same probe, term by term** — composed in
`joinpathsnli.go` (`nestLoopInnerRescanCost` + `nestloopCost`,
`cost_funcs.go:692`, plus the `initial_cost_nestloop` enable term at
`joinpathsnli.go:355-363`; there is no `joinsearchnlicost.go` — only
its test), vs PG's `final_cost_nestloop` + inner-rescan pricing
(`costsize.c`) — and fix iff mis-transcribed. Bar: the audited NLI
price moves TOWARD PG's analogous number with the residual decomposed
(not merely lowered); L6 re-measured after; ZERO EXTRA flips; values
+ sweep bind.

Why (a) first (dependency, not preference): the winner's price decides
everything downstream. If NLI is faithful, the width fix alone
converges (R53-Slice-1's no-spill hash price 44–77k < NLI 117k — STALE,
re-measure in-slice, do not carry). If NLI is underpriced, fixing
width lands on a wrong winner. (a) is diagnostic-gated action either
way. (b) width/footprint ledgered as the conditional follow-up (K65
program or bounded planner cut per R38 lessons — overfitting a spill
constant without the footprint fix stays explicitly out). (c)
dominance: dropped — every L6 loser loses on total (winner is
min-total; no pruning anomaly). (d) margin present: dropped. (e)
sizing OUT this round: dropped.

## 4. Gates met (SCOPE §5)

1. `go test ./internal/optimizer/ ./internal/executor/
   ./internal/testutil/estimateaudit/` green (all three executed this
   round); `go vet` clean. Tree untouched, so they re-verify the
   identical code the Slice-2 gates ran — stated, not implied.
2. Trace-on/off byte-identical ×4 queries (Q9 ×2 stability + Q6/Q13/Q22
   inertness) + max_parallel-0 byte-identity.
3. This document (L-table §1 + adjudication §2 + slice §3).
   Spotcheck branch: `:65433` peer-held → deferral-carries (standing
   rationale; trace-only round, no values channel).

## 5. Ledger (carried)

R51 items 2–3, R52 §4.2, R54 follow-ups, #6/R61-#4/(b) watches, NLI
staleness comment. Join-order counts re-measured as a side effect
(TPC-H 14, both refs) — judged by §2–§3, never alone.
