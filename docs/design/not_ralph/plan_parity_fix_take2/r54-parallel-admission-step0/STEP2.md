# R54 Step-2 — Q5 filed-not-won split pricing (scope, 2026-09-10)

*Scope for the R54 second fix round (TODO.md R54 entry; REPORT-step1.md
§6): Step-1 closed the veto census — top-run hash collapses to V9-vs-V4
with every V4 outer a starved-only set, V6/V7/V8a/V8b absent, merge sites
firing but never M12, Q5's subtree refusal flipping to split via the
splice arm. Two of §6's three slices are adjudicated BELOW from the PG
oracle (no code change); the remaining slice is a PRICING question, so
Step-2 is trace-only: name the cost term(s) that make Q5's filed split
lose while Q9's identical split wins. No cost constant moves in
Step-2: the deliverable is the priced fix proposal. Design basis:
REPORT-step1.md §§1/6; PG oracle Q5/Q9 fetched 2026-09-10 (:65432,
`work_mem=64MB`, `mpwg=4`, read-only EXPLAIN).*

## 0. Oracle adjudications (REPORT-step1 §6 slices 1 & 3 — closed, no code)

- **Slice 1 (V4 starved-LED) — CLOSED as correct deaths, by measurement**
  (the justification §6.1(b) owed). PG Q5 keeps supplier a serial
  hashed inner (Seq Scan 10k rows, never a parallel outer); nation/region
  serial; PG's parallelism sits elsewhere (Parallel Seq Scan customer as
  an NL outer, index probes on orders/lineitem, Gather Merge upper with
  2 workers). PG Q9 likewise (supplier serial hashed inner; parallel on
  orders/part/partsupp; Gather(4) + Partial HashAgg upper). Supplier-,
  nation-, region-LED orientations are serial in PG's winning shape, and
  all three leaves are below PG's parallel-scan size threshold (supplier
  10k rows; nation/region trivial), so V4 deaths there are PG-faithful;
  no orientation-level PG counterexample is in evidence. Seeding partials
  for B4 leaves (option (a)) would DIVERGE from PG. No veto relaxation,
  no seeding. The V4 rule stands: starved-LED orientations die correctly;
  mixed outers already propagate (V9). Scope: this closure covers the
  TPC-H B4 leaves (supplier/nation/region) adjudicated against the Q5/Q9
  oracle above. Q84's DS starved-only sets are NOT closed by this oracle;
  they are out of Step-2 scope (Q84 needs no new lines per §3) and remain
  parked, not pronounced correct deaths.
- **Slice 3 (M12) — CONFIRMED out of reach, unchanged.** 0 admits from
  3000+ site calls (Step-1 §6.3); PG Q5's Gather Merge rides a
  *sorted* Partial GroupAggregate, i.e. an ordered-partial producer
  goopg does not have. Needs the R50 post-pass ruling or a new producer —
  explicitly NOT Step-2.
- **Remaining slice (§6.2): Q5's filed-not-won split.** The only live
  Step-2 question.

## 1. The contrast (both arms in evidence, `/tmp/pp2/r54s1/`)

- goopg-top Q5 (98673.84): Sort → **serial HashAgg** → Gather(4) → PHJ →
  PHJ (Parallel Seq Scan lineitem outer; supplier serial hashed inner).
  The split files (Q5's 2 upper lines flip refused→split) but serial wins.
- goopg-top Q9 (451804.20): Sort → **Finalize HashAgg → Gather(4) →
  Partial HashAgg** → PHJ → PHJ. The identical split WINS.
- PG Q5 (92120.38): Sort → Finalize GroupAggregate → Gather Merge (2
  workers) → Partial GroupAggregate → Sort(n_name) → Hash Join → NL
  spine (Parallel Seq Scan customer outer). PG *does* parallelise the Q5
  upper — via sorted GroupAggregate, not HashAgg, and with 2 workers.
- PG Q9: Finalize HashAggregate → Gather(4) → Partial HashAggregate —
  the SAME shape as goopg-top Q9's winner. Q9 is at shape parity; Q5 is
  not.
- Pricing question: which cost term makes Q5's split lose while Q9's
  wins? Live candidates: the Gather price at 4 workers against Q5's
  single output group (rows=1 width=3154 at the Gather — per-worker
  partial output vs Q9's 5000 groups), the Finalize-vs-serial-HashAgg
  totals at near-identical *within-query* input cardinalities (i.e. Q5
  serial leg vs Q5 split legs on the same join input — not Q5 vs Q9
  inputs, which differ 1834 vs 75773 rows), the PHJ-below totals.
  Note the join-shape gap underneath (goopg PHJ-lineitem-outer vs PG
  NL-spine-customer-outer) is downstream: the upper loss is the priced
  decision, and reproducing PG's exact NL-parallel shape would need the
  H4 producer (no partial NL arm) — but parallelism itself is already
  reachable via the filed hash arms, so this stays downstream and
  excluded, explicitly NOT Step-2.

## 2. Instrument recipe (trace-only, same pattern as Step-0/Step-1)

- One gate-held line per upper candidate at the agg level, both queries:
  serial HashAgg total vs Finalize + Gather + Partial totals, with the
  row estimates each leg prices (Q5's rows=1 vs Q9's 5000 groups is the
  suspect — rows AND width must be ON the line for every leg: serial
  input rows/width, Partial output rows/width, Gather rows/width
  including the observed 1054→3154 blowup, Finalize input rows/width;
  log the losing candidate's full leg totals too — Q5's filed split —
  not just each query's winner). Each line must also carry the input
  join-path total beneath that upper candidate (the PHJ-below cost
  feeding the serial leg vs the one feeding the Partial leg), so §4 can
  subtract join-leg delta from upper-leg delta. A split-vs-serial
  comparison without join-below totals does not answer §4. Reuse
  estimate-audit/DPPATH lines where present; add a `dupagg`-style trace
  line only if the audit lines do not carry per-leg totals.
- Same gates: nil-safe recorder, `enumtrace.go` discard for any new tag,
  unit pins (existence-before-verdict), full optimizer +
  estimateaudit suites green, no `-count=1`.
- Standing warning (memory: cost model has NO parallel dimension): the
  trace must show whether the Gather leg prices workers at all — if it
  does not, the proposal must say so rather than assuming a worker term.

## 3. Measurement protocol (Step-1 repeat, top-mode only)

Same capped clones (`/tmp/pp2/clone-tpch` :5534, `/tmp/pp2/clone-ds05`
:5533), same GUCs, top-mode binary (`GOOPG_GATHER_PATHS=top`),
Q5 + Q9 (Q9 is the winning control; Q1/Q84 need no new lines —
identical under top). Baseline-vs-instrumented EXPLAIN byte-identity
×2 runs each, then harvest the §2 costing lines per query.

## 4. Exit criteria (what Step-2 owes the fix round)

A priced fix proposal, not the fix: the named cost term(s) +
magnitude explaining Q5-loss vs Q9-win, with (i) a PG-faithfulness
argument (PG parallelises Q5's upper — the direction is toward PG, and
Q9's winning shape already matches PG), and (ii) a no-regress list
(Q9 must keep winning; Q1/Q84 plans identical; TPC-H bench shapes
unchanged outside Q5). Pricing numbers themselves are IN Step-2
(they are the deliverable); moving any constant is the fix round.

## 5. NOT in Step-2

Any cost-constant change; any new producer (ordered partials, NL
partial arm); any veto relaxation (V4/B4 closed by §0); sizing,
footprint model; R50 post-pass ruling; join-shape parity below the
agg (downstream of the priced decision). Also NOT in Step-2: changing
Workers Planned (PG Q5's 2 vs goopg's 4 stays as-measured) and changing
the upper agg algorithm (HashAgg vs GroupAggregate — PG Q5's sorted
GroupAggregate rides the ordered producer Slice 3 already excluded).
Step-2 prices the filed HashAgg split as-is.
