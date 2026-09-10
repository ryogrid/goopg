# R52 — R51 shape-movement adjudication (report, 2026-09-10)

*Closes R51 review item 1 (REPORT.md §7: name every moved query vs PG
before the costing-half round). Design basis: K26 §§7–9; R51 REPORT §§3–6.
No code changed in this round — adjudication only, so no values re-gates
(R51 digest 24/24 + SF0.5 all-zero stand).*

## 0. Method

A/B arms: goopg `rec-goopg-{tpch,ds.norm}` (R50 Slice A, `dcd960c`) vs
`r51-goopg-{tpch,ds.norm}` (R51, `ea1ecdae2`); PG arms reused unchanged
from the METHODOLOGY2 recount. Per-query OLD/NEW/PG sections extracted
to `/tmp/pp2/r52/{H-Q5,DS-Q4,Q11,Q25,Q31,Q47,Q57,Q64,Q72,Q84}.txt`
(tmp-only evidence, kept). Tag deltas re-derived mechanically from the two diff
files (not carried forward), and every +/− tag reconciled to a named
divergence via the tool's verbose output (divergence paths quoted
below). Verdict scale: TOWARD / AWAY / MIXED / NEUTRAL, always relative
to PG's plan, never to the old pin (R14 precedent).

## 1. Tag deltas, complete and reconciled

TPC-H (join-method 11→9, agg 8→9): only Q5 and Q9 moved headlines.
Q9 `−join-method` (reaffirmed §2); Q5 `−join-method +aggregation-strategy`.
Q15a-VIEWBODY text-changed is the known splice-header artifact (R51 §5;
plan body identical) — not movement.

TPC-DS (scan +1, sort +1, parallelism +1, qual +1 — net): headline
movers reconciling the four counts — Q31 `+scan-type +qual-placement`,
Q4 `−qual-placement`, Q11 `−qual-placement`, Q47/Q57 `+qual-placement`
each, Q84 `+sort-strategy +parallelism` (net qual: −2 +3 = +1).
Correction provenance: the first draft of this section claimed "exactly
three" — the headline-diff regex covered only MATCH/SHAPE-DIFF verdicts
and missed MISSING-NODE lines (Q11/Q47/Q57). The agent review caught it;
this revision re-derived the deltas over ALL verdicts and adjudicates
the three missed moves below. join-order 95→95,
join-method/param/agg/rendering flat.

## 2. TPC-H verdicts

- **Q9: TOWARD** (reaffirm R51 REPORT §2, no new analysis). New
  innermost join is PG's `partsupp ⋈ part` on the synthesised clause;
  headline drops join-method.
- **Q5: MIXED — join half toward, agg half sideways/away.** Toward:
  the top Hash Cond is now PG's exact 2-clause cond
  (`l_suppkey = s_suppkey AND c_nationkey = s_nationkey`) PLUS one
  implied-redundant synth (`n_nationkey = s_nationkey`), and the tree
  below is index-driven NLI into lineitem (`l_orderkey` bitmap probe)
  and customer (`c_nationkey` bitmap probe) where PG uses Index Scans —
  up from seq scans on both in R50. Away/sideways: the aggregate is
  now a *serial* HashAggregate — the Gather vanished — against PG's
  `Finalize GroupAggregate + Gather Merge + Partial GroupAggregate`.
  The +1 agg tag names the parallel-path admission gap for the new
  shape, not a new capability loss (same gap as DS-Q84 §3). Values
  bound by R51's digest (24/24); the extra synth cond is implied, so
  worst case is redundant evaluation (review §(c)).

## 3. TPC-DS verdicts

- **Q4: TOWARD, strict improvement (−qual, +nothing).** Both ratio
  CASE comparisons are now co-located in ONE top Join Filter — where
  R50 scattered one CASE per Join Filter down the hash chain — matching
  PG's single top-level Join Filter carrying both comparisons. Caveat
  on "exactly": PG's top filter ALSO carries the customer_id conjunct
  (`t_s_secyear.customer_id = t_c_firstyear.customer_id AND CASE… AND
  CASE…`), while goopg's top filter carries only the two CASE
  comparisons, with the customer_id equalities as Merge Cond (9×, top
  Merge Join) and doubled Hash Conds below. So the equality placement
  differs (merge/hash cond vs filter), but the CASE co-location —
  which is what the −qual tag names — matches PG. No new tags.
- **Q31: MIXED leaning toward.** Toward: the top node is now a Nested
  Loop, PG's node kind (was Hash Join). Away-cosmetic (the two +tags):
  `+qual-placement` =
  `/Q31/Sort/c2/Nested Loop qual-placement: JoinQual signature differs`
  — the residual filter carries 9 redundant `ca_county` equalities
  (incl. base-table-qualified copies, see §4.3) against PG's single
  equality + 2 CASE; `+scan-type` =
  `.../c0/Hash Join/c1/CTE Scan scan-type: CTE Scan on ws ws2 vs CTE
  Scan on ss ss3` — a genuine re-pairing of CTE refs under the
  reordered parent (the tool's `cmp_same` reordered-parent suppression
  did NOT fire here — top NL leaf sets are equal, flag False — so the
  tag names the same re-pairing the join-order "leaf sets differ: ss
  ws vs ss ss ss ws ws" divergence already signals; redundant signal,
  no access-path change on either side). Neither +tag is an access-path
  change; both values-safe per R51's sweep.
- **Q84: MIXED — join-kind toward, parallelism concretely away.**
  Toward: the top two levels are now text-identical to PG modulo
  Parallel (NL with Join Filter `(cd_demo_sk = c_current_cdemo_sk)` =
  PG's line verbatim; Hash Join on the synthesised `(sr_cdemo_sk =
  c_current_cdemo_sk)` = PG's Parallel Hash Join cond verbatim; inner
  probe on the same key, Index vs PG's Index Only = the known no-IOS
  gap, predates R51). Away (and it counts — net tags went 3→5): R50's
  `Gather Merge (Workers: 1)` matched PG's Gather Merge, and the new
  shape lost it (`+sort-strategy`: `Sort vs Gather Merge`;
  `+parallelism`: `Parallel flag differs`). The parallelism loss is as
  concrete as the join-kind gain, so MIXED, not TOWARD. Input to the
  costing/parallel-admission half, not a capability regression.
- **Q11: TOWARD (−qual, +nothing).** Right-deep hash chain →
  balanced bushy pairing by sale_type/year (`s⋈s`, `w⋈w` under a 4×
  `customer_id` top cond); PG pairs `s_secyear⋈w_secyear` via Merge
  Join at the bottom. Both pair secyear refs first, and the ratio CASE
  now sits in a single top Join Filter as in PG (PG's top Join Filter
  carries customer_id + ratio CASE; goopg's top carries the 4×
  customer_id cond + ratio CASE — converging). The −qual reconciles
  to two `Filter signature differs` JoinQual divergences present in
  R50's verbose output and gone in R51's under the bushy re-pairing.
  No new tags.
- **Q47/Q57: NEUTRAL-TO-AWAY-cosmetic (+qual each, values-bound).**
  The +qual on both is the doubled top Hash Cond (`rn = (rn + 1) AND
  rn = (rn − 1)` plus duplicated category/brand/store equalities, Q47
  NEW lines 69–76) plus the un-doubled 4-col inner Hash Cond. The leg
  order moved cosmetically AWAY from PG: PG's outer leg is `CTE Scan
  on v1 v1_lead` under `Join Filter: (v1.rn = (v1_lead.rn - 1))`
  (Q47 PG lines 113–116), matching R50's lead-outer top cond
  (`((rn − 1) = rn)` over `v1_lead` outer, OLD lines 29–30); R51's top
  is now plain-`v1`-outer with the doubled cond. Neutral-to-away, and
  cosmetic: the dominant gap on both queries is unchanged — the
  WindowAgg MISSING-NODE (goopg's bare WindowAgg vs PG's `Window: w1 /
  w2` specs) — and values stand bound by R51's SF0.5 all-zero sweep.
- **Tag-flat shape changes (all NEUTRAL to leaning toward, no gaps
  opened or closed at headline level):**
  - Q25 — store_sales probe edge switched to the transitive
    `(ss_item_sk = cs_item_sk)` (PG probes `(ss_item_sk =
    sr_item_sk AND ss_ticket_number = sr_ticket_number)`); the
    customer_sk filter set converges to PG's 3-way transitive set
    (`sr=ss`, `cs=ss` on both sides). Neutral (equally-valid
    transitive edge; order-driven).
  - Q64 — the item_sk equivalence set (`cs=ss`, `cs=item`,
    `cs/sr/item` copies) moved into Hash Conds/Join Filters,
    mirroring PG's `ss_item = i_item` / `sr_item = i_item` filter set;
    store_returns probe `(sr_item_sk = ss_item_sk AND
    sr_ticket_number = ss_ticket_number)` = PG's probe verbatim.
    Neutral-to-toward.
  - Q72 — inventory probe re-keyed to the transitive item side
    `(inv_item_sk = i_item_sk)` + residual `(cs_item_sk =
    inv_item_sk)` (PG probes `(inv_item_sk = cs_item_sk)`); valid
    transitive alternative following the new order (item joined
    first). Neutral.
- **Text-only, no shape change (12):** Q58/Q65/Q74 = pure redundant
  synth duplicates in place (e.g. Q58 `Hash Cond: ((item_id =
  item_id) AND (item_id = item_id))`, Limit 4.01..4.02→4.06..4.07) —
  redundant eval only; Q17/Q24/Q29/Q37/Q82 = Join Filter/Index Cond
  text (same class); Q83 = CTE leg swap (`sr_items⋈cr_items`
  build/probe sides exchanged, `item_1.`→`item.` naming) + doubled
  cond + estimate move (rows 25→1/3) — neutral, leg-swap class;
  Q36/Q70/Q86 = capture-ERROR filename churn
  (`parity-capture-*.sql` run id) — harness artifact, not plan
  movement (all three unplannable on both engines, standing).
  (Q47/Q57 moved up to headline movers — see above.)

## 4. Cross-cutting findings for the costing-half round

1. **Redundant synth-clause eval has visible cost; DP-minimisation
   is a hypothesis, not a finding.** Q58 Limit +0.05 (4.01..4.02→
   4.06..4.07); Q11 (7.68→7.93) and Q72 (36.40→44.63) had their
   winners chosen at HIGHER cost than R50's — CONSISTENT WITH
   identical shapes carrying extra duplicate-clause eval, but the
   reachability half ("all original edges remain admitted", "no
   search-space hole") was never verified: no edge-admission audit
   was run in this round. Treat DP-still-minimises as the null
   hypothesis for the costing half to confirm or refute, not as an
   established fact. Identical-cond dedup is optional hygiene either
   way, never correctness.
2. **New synth-opened orders repeatedly lose parallel paths**
   (H-Q5's Gather, Q84's Gather Merge). Parallel-path admission and
   pricing for synth-opened shapes belongs to the costing half
   alongside `join_search_one_level` work.
3. **Noted, not pursued:** Q31's outer Join Filter references
   `customer_address.ca_county` above the CTE boundary —
   base-table-qualified Var naming in synth copies. Values-bound by
   R51's all-zero sweep (likely origin-name rendering); one line only.
4. **Standing opens (unchanged):** Q15a working-copy EXPLAIN prefix;
   Q36/Q70/Q86 unplannable both engines; nullable-side closure-input
   assertion (R51 review item 3).

## 5. Debt ledger after R52

- R51 review item 1 **CLOSED** by this report: all 11 headline/shape
  movers (8 headline-tag: H-Q5, H-Q9, DS-Q4, Q11, Q31, Q47, Q57, Q84;
  3 tag-flat shape: Q25, Q64, Q72) + all 12 text-only changers named
  against PG.
- R51 review item 2 **carried**: Q15a-splice / `.norm`-arm provenance
  (R51 §5) stays attached to the corpus numbers; evidence
  `/tmp/pp2/r52/` kept alongside `/tmp/pp2/r51-*`, `rec-*`.
- R51 review item 3 (nullable-side assertion) **open** → costing-half
  round or standalone hardening.
- **Next round: join-order costing half** — `join_search_one_level`
  pricing from the same (now open) candidate set, plus parallel
  admission/pricing for synth-opened shapes (§4.2). Join-order stands
  95/18; zero queries are blocked by anything alone (METHODOLOGY2 §3).
