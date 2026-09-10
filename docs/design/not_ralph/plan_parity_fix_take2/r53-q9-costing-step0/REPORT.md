# R53 Step-0 — Q9 L4/L5/L6 numbers: sizing vs pricing (report, 2026-09-10)

*Step-0 of the R53 costing-half round (TODO.md R53 entry; design basis K26
§§7–9, R52 REPORT §4). Question: at the L4 divergence — PG's
`{ps,p,s}+nation` vs goopg R51's `{ps,p,s}+lineitem` — what decides: sizing
(rows) or pricing (cost)? Answer: PRICING, at L6 composition, inside the hash
arm, by a 2.5% margin. Sizing and admission are ruled out with named evidence.
No planner behaviour changed: the deliverable is the instrument (a new
`DPTRACE cost` line, gate-held, production-inert) plus these numbers.*

## 0. Method

Instrumented worktree binary `/tmp/pp2/bin/goopg-r53step0` (commit on push;
`internal/optimizer/joinsearchtrace.go` + `joinsearchlevel.go` +
`internal/testutil/estimateaudit/enumtrace.go`, tests in
`joinsearchtrace_test.go`). Capped scratch server
(`GOOPG_CG_UNIT=r53step0`, `scripts/goopg-test-run.sh`) on
`/tmp/pp2/clone-tpch` :5534 with `GOOPG_PGSHAPED_DP_TRACE=1`; hand-written Q9
(`tpch.Queries()[9]`, the runner embeds it) via psql EXPLAIN, twice — the two
plans are byte-identical (`PLAN-IDENTICAL`), so the trace is stable, not A/A
noise. Evidence (tmp-only, kept): `/tmp/pp2/r53/{q9.sql,q9.plan,q9.plan2,
q9-dptrace.txt,q9-dptrace2.txt,server-start.log,server-start2.log}`.
Trace vocabulary: `DPTRACE cost lev=<level> rel=<relset> rows=<R51 sizing>
npaths=<pathlist len> cheapest=<kind> reqouter=<winner's RequiredOuter>
total=<winner total> second=<runner-up kind> secondtotal=<runner-up total>`.

## 1. Step-0 numbers (second run; first run identical modulo the new fields)

The two spines, level by level (`p=part s=supplier l=lineitem ps=partsupp
o=orders n=nation`):

| lev | goopg spine (R51 winner) | rows | winner | total | PG spine | rows | winner | total |
|-----|--------------------------|------|--------|-------|----------|------|--------|-------|
| L2 | {p+ps} | 40404 | hash | 37095.30 | {p+ps} (shared) | — | — | — |
| L3 | {p+ps+s} | 40404 | hash | 38103.86 | {p+ps+s} (shared) | — | — | — |
| L4 | {l+p+ps+s} | 303093 | nli, reqouter={} | 200738.33 | {n+p+ps+s} | 40404 | hash | 38242.92 |
| L5 | {l+o+p+ps+s} | 303093 | hash | 612691.93 | {l+n+p+ps+s} | 303093 | nli, reqouter={} | 200877.40 |
| L6 | full, via {l+o+p+ps+s}⋈{n} | 303093 | hash | 616861.02 | full, via {l+n+p+ps+s}⋈{o} | — | lost | >616861.02 |

L6 hash ladder (DPPATH, pre-existing instrument, cheap end by total):
hash 616861.02 accepted (startup 285519.56 — the plan's top node verbatim);
hash 632364.99 dominated (startup 295424.06); hash 643216.79 dominated
(startup 166606.70); hash 688957.75 dominated (startup 373314.70); merge
834054.90 accepted (= `second` on the cost line — the dominated hashes are
pruned from the pathlist, so the runner-up IN the pathlist is merge).
L6 offers: 8 partitions (4 phase-1, 4 phase-2), PG's
`{l+n+p+ps+s}|{o}` among them (phase 1, created=0). L6 declines: NONE —
zero `DPTRACE decline lev=6` lines (all declines are lev 2–5).

## 2. Adjudication

- **Sizing: OUT.** The decision levels tie on rows: L5 303093 = 303093 for
  the two spines (q9-dptrace2.txt:25–26); L6 is a single result relset at
  303093 either way, which carries no information. The L4
  row gap (303093 vs 40404) eliminates nothing — the DP keeps every relset
  and prices each; a locally-cheap relset is not a globally-cheap plan
  (PG's L4 relset is 5× cheaper absolutely at 38242.92 yet loses at L6:
  joining orders late costs ~411954 marginal, joining nation late ~4169).
- **Admission: OUT.** PG's L6 partition was offered (phase-1 pair line,
  §1), and no L6 pair was declined for any reason. The PG shape reaches
  pricing — it is not an enumeration hole (contrast the R51 half, where
  that was the open question).
- **Parameterisation: OUT as a dimension.** `reqouter={}` on every L4–L6
  winner: the `nli` winners are unparameterised PATHS (usable anywhere)
  with parameterised inners — the outer supplies the bindings. No
  CheapestTotal-fallback story, no above-tree binding debt.
- **Pricing: IN — L6 hash-vs-hash, 2.5% margin.** Winner hash 616861.02
  (startup 285519.56 ≈ L5 orders-hash startup 285518.00 + 1.56: hash with
  nation inner on the L5 spine) vs nearest hash rival
  632364.99 (startup 295424.06) — a 15.5k gap on 616k. The runner-up arm is
  merge at 834054.90, so the fight is inside the hash arm, not across arms.

A naive composition estimate says the PG shape should WIN by ~4k
(200877.40 + the L5 orders-join marginal 411953.60 ≈ 612831 < 616861) — it
does not, so the orders-join prices differently from the L5-nli base
(L5's `{l+n+p+ps+s}`) than from the L4-nli base. That delta — not rows, not
admission, not parameterisation — is the whole of the R53 pricing slice.

## 3. Scoped pricing slice (next)

**Slice: L6 hash-arm attribution.** One instrument gap blocks it: DPPATH
logs producer/kind/rows/startup/total/verdict per offered path but NOT the
partition (outer/inner) that produced it, so the 632364.99 rival cannot be
named as PG's partition — only bounded (nearest hash rival). The slice is:

1. Attribute L6 hash offers to partitions (DPPATH outer/inner fields, or a
   pair-key on the path — small, trace-side only, same gate).
2. Read the PG partition's hash price and decompose winner-vs-rival into
   arm terms (build/probe sides, widths, startup).
3. Standing hypothesis to confirm or refute: the outer-WIDTH term in the
   hash arm — the L6 outer carries nation's columns at identical rows
   (303093), and the margin (2.5%) smells like width-driven build/probe
   cost, not a structural misprice. (M0076 trap noted: validate the chosen
   SHAPE, not just the number; the width term rides build-side memory.)

Explicitly NOT in the slice: sizing (`sizeJoinRel` — exonerated §2),
enumeration/phases (PG partition offered), parallel admission (both L6
contenders unparameterised; the synth-opened parallel gap of R52 §4.2 is a
separate half), merge/NL arms (lost by 35%+/orders of magnitude).

## 4. Instrument notes (for review)

- `cost()` records AFTER `setCheapest`, per relset: winner = CheapestTotal
  (the path the search uses — never the min-total entry), second = cheapest
  non-winner by total. When those differ in parameterisation the lines say
  so directly (pinned by `TestTraceCostSecondAndReqouter`).
- `nli` = inner parameterised on the outer (index-assisted NL); `reqouter`
  = the path's own parameterisation. The two come apart exactly when the
  outer supplies the bindings — Q9's L4/L5 winners are that shape, and the
  old conflation would have mis-scoped the slice toward admission.
- Unknown PathKinds render `kind<N>` (never collapse into a known label);
  nil winner renders `none`/NaN. Single-path relsets render
  `secondtotal=NaN` — the §3 step-1 consumer must handle NaN (no numeric
  runner-up exists there).
- Sibling audit (practice card): writer (`joinsearchtrace.go`) ↔ parser
  (`internal/testutil/estimateaudit/enumtrace.go`) updated together — the
  parser recognises (and skips) `cost` so the new kind does not pollute the
  provenance channel's Malformed counter; structured cost parsing belongs
  to the slice that needs it (§3.1). Trace-off production path is one
  nil-receiver call per relset per level (nil check, no allocation).
- Gates: `TestTrace*` + full `internal/optimizer` + `testutil/
  estimateaudit` suites green; Q12/Q13 spotcheck (`scripts/
  tpch-spotcheck.sh`) is the pre-commit gate per the executor/planner card
  (this change is trace-only behind `GOOPG_PGSHAPED_DP_TRACE`, default off).

## 5. Debt ledger

- R53 TODO entry: Step-0 DONE by this report (numbers + scoped slice);
  agent review APPROVE-WITH-NOTES 2026-09-10, notes applied; remaining in
  the entry: commit → push.
- Carried unchanged: R51 review item 2 (Q15a-splice/`.norm` provenance),
  item 3 (nullable-side assertion); R52 §4.2 parallel admission/pricing
  half; K26 join-order stands (H 18 / DS 95).
