# R120 result (rev 2) — the defect is real and the correction is directionally right, but ALONE it is insufficient AND net-negative. Keep default-off; pair it with K65/K66 ncols narrowing.

Three of five predictions **failed**, in the way SCOPE §1a/§4a
pre-registered. The round's disposition fires as written: *"report the
pairing requirement rather than tuning."* No constant was tuned; the
flag ships **default-off**.

Rev 2 remediates a BLOCK review: B1 (P4 cited a flag-OFF sweep), B2 (the
Q10 arithmetic was inferred, and self-refuting), B3 (P5 skipped). All
three are now answered by **measurement**, and the corrected numbers
make the finding stronger, not weaker. Notes 1-12 applied.

## 1. What was implemented

`GOOPG_HASHAGG_WIDTH_CURRENCY` (strict `== "1"`, default-off,
`internal/optimizer/hashagg_widthcurrency.go`), gating three arms in
`costAgg`'s hashed spill arm (`cost_funcs.go`):

- **Arm A** — `pages` uses `hashsize.EntryBytes(inNcols, inAvgVarBytes)`
  (the `relation_byte_size` per-row analogue) instead of bare
  `inAvgVarBytes`.
- **Arm B** — `hashAggEntrySize` receives the bare tuple width
  `W = 48*inNcols + inAvgVarBytes` (`EntryBytes` minus `RowSliceBytes`),
  since it adds its own 16-byte header exactly as PG's
  `hash_agg_entry_size` does (`nodeAgg.c:1706-1707`).
- **Arm C** — the gate becomes `inNcols > 0`.

Registered in the flag-provenance table and `scripts/planner-flags.env`
regenerated, so every artefact names the currency that priced it — the
mechanism that caught B1. 7 pins added. Temporary diagnostic scaffolding
was removed after measurement (R117/R118 discipline); `grep R120DUMP`
returns nothing.

## 2. The defect was confirmed, not asserted

`hashAggEntrySize`'s parameter is literally `tupleWidth` and the `pages`
comment cites `relation_byte_size(input_tuples, input_width)`, yet both
received the variable payload only. PG hands one and the same
`input_width` to both (`costsize.c:2801-2802`, `:2824`) and its sorted
rival shares that currency (`cost_tuplesort`, `:1903`). goopg's sorted
rival pays full `EntryBytes = 48*ncols+24+avgVar`. **The two aggregation
rivals were priced in different currencies.** That stands.

## 3. Results against the pre-registered bars

TPC-H via `estimate-audit -plan-only` (per-connection stats); TPC-DS via
`capture-tpcds.sh` on :65437 vs live PG :65438; `work_mem` 64MB both
sides → hash budget `64MB x hash_mem_multiplier 2` = **134,217,728 B
(128 MiB)**, confirmed reached by the dump's own `budget=` field.

| # | bar | result | verdict |
|---|---|---|---|
| P0 | OFF bit-identical to pre-round HEAD | TPC-H **byte-identical** (md5 `19b1c9a1…` both). TPC-DS A/A across a restart identical but for the header and the psql temp-file PID | **PASS** |
| P1 | TPC-DS GroupAgg 23 → ≥43 | **29** (+6); HashAgg 130 → 123 (−7) | **FAIL** |
| P2 | TPC-DS `aggregation-strategy` −5, no new category | **69 → 71 (WORSE +2)**; `rendering` +1; `scan-type` −1 | **FAIL** |
| P3 | TPC-H match ≥ 6, none of the six flips | **6 → 5**; **Q10 lost** | **FAIL** |
| P4 | values unchanged **ON** | SF0.25 sweep with the stamp reading `GOOPG_HASHAGG_WIDTH_CURRENCY=1`: **PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3**. TPC-H Q10 — the one query whose plan changed — result digest **byte-identical** OFF vs ON (`0225dfe3…`) | **PASS** |
| P5 | a just-below-threshold grouping does not move | **selected post-hoc** (SCOPE required naming it before the ON run; it was chosen from the ON dump afterwards). Highest sub-threshold node = **57.8% of budget** (groups=32000, ncols=16, avgVar=1618, entry=2424); it did **not** change strategy. The corpus contains no node in the 60–90% band — everything else is ≤10% or ≥171% | **PASS (band widened, stated)** |

Suites green, `go vet` clean, `tpch-spotcheck.sh` **RESULT=PASS**
(Q12=2, Q13=34).

## 4. Q10 — the mechanism, MEASURED (supersedes rev 1's inferred arithmetic)

Rev 1 reproduced SCOPE §4a's back-solved `avgVar ≈ 334` and concluded a
"~6.5% margin". Worked through the real code that account was
self-refuting: at those numbers `hashAggSetLimits` early-returns and the
arm charges *nothing*, yet the flip was real. The review caught it. The
temporary dump settles it — **`avgVar` is 2080, not 334** (6.2x off):

| term | OFF | ON |
|---|---|---|
| groups / ncols / avgVar | 59038 / 37 / 2080 | same |
| tuple width fed to `hashAggEntrySize` | 2080 | `48*37 + 2080` = **3856** |
| entry | 2112 B | **3888 B** |
| groups x entry | 124,688,256 B = **92.9%** of budget | 229,539,744 B = **171%** |
| arm | fits ⇒ inert | **spills** |
| plan | `HashAggregate` — **MATCHes PG** | `GroupAggregate` over an added `Sort` — match lost |

PG's entry for the same node is ≈224 B (~13 MB, comfortably inert), so
PG keeps `HashAggregate`. The `48*ncols` term alone (**1776 B**) is what
tips Q10 from 93% to 171% — and it is exactly the term ncols narrowing
would remove. The width gap is visible in the plan text: goopg's sort
input is `width=2112` where PG's aggregate output is `width=205`.

Corpus-wide **(TPC-H)**, the correction takes nodes over budget from
**4 to 5** — it newly tips precisely one, Q10. This census is TPC-H only,
at that cluster's budget; on TPC-DS at least six nodes newly tip (+6
GroupAggregates), so do not quote the 4→5 figure as global. The other four were already over
(1894%→3741%, 490%→1161%, 372%→735%, 122%→214%).

## 5. Why the correction alone cannot work (the round's finding)

**It over-charges and under-reaches at once, for one reason.**

1. **Over-charge (Q10, measured above).** Correcting the currency while
   `ncols` is the full concatenated relation width converts an
   under-charge into an over-charge. goopg's entry is **3888 B against
   PG's ~224 B — 17x** — on the same node, after the fix.
2. **Under-reach (P1).** Closing the GroupAggregate gap needs **+103**
   nodes; the currency buys **+6**.

On (2) the honest formulation, per the review: what was *measured* is
that this correction moved 6 nodes at the pinned budget. The inference
— reasonable but an inference — is that a currency now over-charging
~17x relative to PG should have over-fired if PG's 126 GroupAggregates
were spill-driven, and it did not; so the ~100-node residue is unlikely
to be spill-driven and points at **K12** (PG picks `GroupAggregate`
mostly because it *delivers an ordering* the plan owes anyway, decided
on pathkeys). R72 STEP-0 measured that mechanism directly, but on a
single query (Q4), so K12-dominance is a well-supported hypothesis here,
not a corpus measurement.

## 6. Measurement hygiene (three traps hit and controlled)

- **Provenance stamp.** Rev 1's P4 cited a sweep whose own header read
  `GOOPG_HASHAGG_WIDTH_CURRENCY=unset(off)` — a flag-OFF run reported as
  ON evidence. Re-run with the flag exported in the shell that starts
  the server; the stamp now reads `=1`. The guard that caught this is
  the one this round extended.
- **Stats-drift epoch.** The values sweep re-samples stats; a post-sweep
  OFF capture differs in `rows`/cost and can tip cost-close join orders.
  Two "worsenings" first attributed to the flag (`join-order` 89→90,
  `qual-placement` 16→17) were **drift** — the post-sweep OFF baseline
  already showed them. All §3 numbers are from a **same-epoch** pair
  (`off3` vs `on2`); the SCOPE's 22/129 baseline became 23/130 across
  that epoch, which does not affect any verdict (29 < 43 either way).
- **A/A noise floor = 0.** Two flag-OFF captures across a restart are
  identical but for the header and psql temp-file PID.
- A capture against a stopped server failed loudly ("connection
  refused") — the sweep script stops sf025 on exit.

## 7. Verdict and disposition

**Keep `GOOPG_HASHAGG_WIDTH_CURRENCY` default-off.** The code, pins and
provenance registration are retained: the defect is real, the arms are
PG-cited, and this is the ready-made second half of the pairing
(the R108/R113 class — both of those default-off cost arms still sit in
`planner-flags.env` on the same rationale). Reverting would force R121
to re-derive Arms A/B/C and their citations.

Do **not** promote it, and do **not** tune `DatumBytes`, the gate, or
the entry formula so Q10 survives — that is fitting a constant to one
query, which this workstream forbids.

**Expiry (review note 9):** three default-off cost arms now accumulate
with no end date. `GOOPG_HASHAGG_WIDTH_CURRENCY` is tied to R121 —
**promote or delete when ncols narrowing lands**; it should not outlive
that decision.

**R121 (the pairing).** Narrow the planner's `ncols`/`AvgVarBytes` cost
inputs. The seam is confirmed open at HEAD: `relfromjoinlist.go:699`
`stampNeededColsOnRels()` already lands `NeededCols` on the base rels
*before* paths are costed at `:707-709`, while the existing narrowing
machinery (`narrowoutput.go`; `deriveJoinKeeps` at
`createplanroot.go:122`) runs **entirely post-selection** at
`createPlan` time and touches no cost — so the planner costs joins at
full concatenated width while the executor builds a narrowed hash
table. For Q10 that means 37 columns and `avgVar=2080` against the ~7
columns and ~205 bytes PG actually carries. Narrowing removes the
overshoot that sank this round; only then is the currency correction
safe to enable.

Datum-size / `minimize_datum` stays **closed** (R114 user-stop).

## 8. Corrections carried from review

- Q10's verdict moved `MATCH [rendering]` → `SHAPE-DIFF [join-order,
  aggregation-strategy, sort-strategy]`, so the TPC-H category line also
  moved `join-order 14→15` and `rendering 1→0` — one query, three
  category deltas.
- `hashAggEntrySize` omits PG's unconditional `TupleHashEntrySize()`
  (`nodeAgg.c:1726-1730`): pre-existing under-charge, now recorded in
  the function's own comment.
- `costAgg`'s header still claimed "No spill arm exists … inNcols/
  inAvgVarBytes are its future inputs" — false since R3 and left through
  R120's edit of that arm. Corrected.
- The P0 pin could degenerate into a tautology if the arm stopped
  firing; it now fails loudly instead. Test constants carry the
  measured `avgVar=2080`.

## 9. Artefacts

`/tmp/pp2-r120/`: `r120.plans.txt` + `r120.pg.plans.txt` (baseline),
`r120off.plans.txt` / `r120on.plans.txt` (TPC-H A/B),
`goopg-tpcds-off3.plans.txt` / `goopg-tpcds-on2.plans.txt` (same-epoch
TPC-DS A/B), `pg-tpcds.plans.txt`, `dump-off.txt` / `dump-on.txt` (the
per-node arm inputs behind §4), `p0.diff`. Checksummed baseline copy in
the session scratchpad `r120-baseline/MD5SUMS`. Sweeps:
`sweep-20260914-013301.txt` (OFF) and `sweep-20260914-015214.txt` (ON,
stamp `=1`).

P4's TPC-H digest is reproducible with, on each arm:
`psql -h 127.0.0.1 -p 65433 -U tpch -d tpch -X -q -c "SET
max_parallel_workers_per_gather=0" -f /tmp/parity-r0/queries/tpch/Q10.sql
| md5sum` → `0225dfe3808a05c60ec30ea5e72acfdc` both. Q10 is the only
TPC-H plan section that differs between the arms, so the other 23
queries execute byte-identical plans and a full 24/24 digest would be
23 tautologies plus this check.

**Epoch note for R121:** the ON sweep (01:52) ran AFTER the `off3`/`on2`
A/B pair (01:38/01:39) and re-sampled stats, so the next round's OFF
baseline sits in a third epoch — re-take it rather than reusing these.

`make plan-gate` (SCOPE §7.6) was NOT run: it is inapplicable here.
The flag is default-off and P0 proves the default path is bit-identical
to pre-round HEAD, so the gate's structural pins cannot have moved. This
is recorded as a reasoned omission, not a silent skip.
