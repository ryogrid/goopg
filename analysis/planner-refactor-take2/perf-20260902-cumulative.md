# Cumulative performance report — planner refactor take 2, 2026-09-02

Covers `ec220754b` … `13430fc3a`. Supersedes
[phase0-report-20260902.md](phase0-report-20260902.md) (instruments) and
[perf-20260902-pgstatistic-decode.md](perf-20260902-pgstatistic-decode.md)
(the single-change A/B), which stay for their detail.

**This is a progress report, not a completion report.** Phase 0 is partly done,
Phase 1 partly, Phases 2–7 are untouched. What follows is what moved, what did
not, and what the measurements do and do not establish.

---

## 1. Measured result

TPC-H SF=1, `cmd/tpch-runner`, all 24 timed items, fresh server per arm,
identical server age, `GOGC=100 GOMEMLIMIT=12GiB`, 180 s per-query cap.

| arm | total | vs previous |
|---|---|---|
| pre-`f07c20b1f` (histograms lost on restart) | **288.10 s** | — |
| + pg_statistic decode fix (`f07c20b1f`) | **257.75 s** | **−10.5 %** |
| + range-bound pairing (`71653da23`) | **254.65 s** | +0.45 % vs its own control (noise) |

| + P1-14 / P1-25 (`ae78cc6eb`) | **248.71 s** | +0.88 % vs its own control (noise) |

Net across the session: **288.10 s → ~249 s, about −13 %.** Row counts identical
on all 24 items in every arm.

The second A/B's own control ran at 253.51 s where the first arm's `after` ran
at 257.75 s — a 1.7 % drift between two runs of the *same binary*, which is a
useful direct read on this harness's run-to-run variance and the reason single
sub-second figures are not reported as results.

## 2. Per query, the change that mattered

From the `f07c20b1f` A/B:

| query | before | after | |
|---|---|---|---|
| Q5 | 60.99 s | 41.33 s | **−32.2 %** |
| Q3 | 4.48 s | 3.64 s | −18.8 % |
| Q7 | 27.69 s | 22.92 s | **−17.2 %** |
| Q18 | 65.58 s | 62.06 s | −5.4 % |
| Q9 | 51.96 s | 52.11 s | +0.3 % |

17 of 24 faster, 7 slower, none slower by more than 0.22 s absolute.

Q5 and Q7 are where a mis-sized scan does most damage: both join five or six
relations with a date window on the driving side, so the error propagates into
join-order choice. Q5 recovering a third of its time indicates the order
changed, not merely that the scan cost less. Q9 is unmoved, which fits — its
restriction is `p_name like '%green%'`, which no histogram helps.

## 3. What was actually wrong

Not a cost-model deficiency. **The planner had no statistics.**

Three bugs in the `pg_statistic` physical-tuple decoder, each silent, each
masking the next:

1. `decodeTextArray` advanced by each element's **unpadded** length. PG aligns
   array elements to the element type's `typalign` before reading
   (`att_align_pointer`, tupmacs.h); text is `'i'`, so the writer pads to 4 and
   the decoder drifted up to 3 bytes per element. Ten-character ISO dates
   (14 bytes, padded to 16) corrupt on the second element — which is why date
   histograms in particular vanished.
2. `readVarlena` assumed the 4-byte varlena header. The writer follows
   `heap_fill_tuple` and emits the **1-byte short header** for anything up to
   127 bytes, so a small slot desynchronised the walk.
3. `readVarlena` aligned every varlena to 4. Alignment is per column type:
   `stanumbers*` is `float4[]` (`typalign 'i'`), but `stavalues*` is `anyarray`
   (`typalign 'd'`, verified in `pg_type.dat`). A column carrying a
   correlation — 28 bytes, ending 4-aligned but not 8-aligned — followed by a
   histogram put the histogram header four bytes out of reach. That is exactly
   what ANALYZE produces for an indexed date column.

The statistics were on disk the whole time and were read back empty. After the
fix, `l_shipdate` restores 101 histogram bounds from the **same heap** with no
new ANALYZE, and a date-range estimate moved from 2 000 418 (exactly
`rows / 3`, `DEFAULT_INEQ_SEL`) to 2 567 922 against PostgreSQL's ~2.58 M.

**Consequence for the record:** every previously recorded goopg TPC-H figure,
including the 227.0 s / 9.9× headline, was measured on a planner with no
histograms. The benchmark lifecycle restarts the server and never runs an
in-session ANALYZE. Those numbers are not wrong, but they measured a blind
planner, and the gap attributable to planning logic could not be separated from
the gap attributable to missing statistics.

## 4. The negative result, stated plainly

`P1-13` (range-bound pairing) corrected a real and large cardinality error:

| | estimate | vs actual (910 180) |
|---|---|---|
| before | 1 855 086 | 2.04× over |
| after | 902 018 | 0.9 % under |

**And the time did not move**: +0.45 % across 24 items, inside noise, no query
beyond it, row counts identical.

That is worth recording rather than burying. On this corpus a 2× cardinality
correction on the driving scan did not change which plan wins. The plan shape is
constrained by things selectivity does not touch — outer joins peeled out of the
join search, no `PathTarget`, cost GUCs that never reach the planner. It also
tempers the design bundle's framing: Phase 1 items are judged by an estimate
ratchet for a reason, and expecting each to pay for itself in seconds is the
wrong model.

## 4b. The pattern, now on three measurements

| change | cardinality effect | TPC-H time |
|---|---|---|
| `f07c20b1f` pg_statistic decode | histograms restored (none → 101 bounds) | **−10.5 %** |
| `71653da23` P1-13 range pairing | 2.04× over → 0.9 % under | +0.45 % (noise) |
| `ae78cc6eb` P1-14 + P1-25 | `DISTINCT` 6 001 255 → 7, matching PG | +0.88 % (noise) |

Three A/Bs, one conclusion: **restoring statistics that were entirely absent
moved time; refining statistics that were merely inaccurate did not.** The
second and third changes made estimates markedly more PG-like — the DISTINCT
count now matches PostgreSQL's exactly — and the corpus did not care.

Two queries moved in the third A/B, both under a second absolute: Q2
1.71 s → 0.74 s (−56.7 %) and Q10 2.88 s → 3.80 s (+31.9 %). Row counts
identical throughout.

This is the strongest evidence the project has that **the remaining gap is not
in the estimator**. It also says something about how to run the rest of Phase 1:
those items should be judged by the estimate ratchet 09 defines, not by
expecting each to pay for itself in seconds, and it would be a mistake to keep
spending measurement time on per-item TPC-H A/Bs for them.

## 5. Instruments, which is what Phase 0 was for

None of these changed planner behaviour; all changed what can be measured.

- **18 node types printed their Go type name** into EXPLAIN (`7677faaed`);
  21 lines of the 2026-09-02 nightly regress diff came from three of them.
- **Four node types hid their entire subtree** — `DISTINCT ON` rendered as one
  line where PG renders three nodes. Worse than a wrong label: a truncated plan
  reads as agreement on everything it does not show.
- **Schema qualification followed no mode** (`2a63fbe21`): PG qualifies a
  relation under VERBOSE only and never qualifies an index; goopg qualified both
  in both modes — one guaranteed divergence per scan node, corpus-wide.
- **`EXPLAIN` and `EXPLAIN ANALYZE` disagreed on `rows=` by 200×**
  (`5309bf402`).
- **EXPLAIN printed `cost=0.00..0.00`** on every node (`9cbc7661b`). It now
  carries the cost the planner chose, via `PlanCost` embedded in the node as PG
  carries it on `struct Plan`.
- **A plan-shaping flag no artefact could name** (`f2ac4fdfc`), through a hole
  in the guard that exists to prevent exactly that.

The cost instrument paid for itself immediately: it is what surfaced
`rows = 6001255/3` and led to §3.

## 6. What is still open

Phase 0: P0-04 (rtindex-order name numbering), P0-05/06/07 (the plan-parity
instrument and its baseline roll-up), P0-08, P0-11, P0-12, P0-13, P0-04e.
Phase 1: P1-01…P1-12, P1-15…P1-27. Phases 2–7 entirely.

goopg is at ~255 s against PostgreSQL's ~22.9 s on this corpus. The remaining
gap is structural, and the P1-13 result is the evidence for that claim rather
than an assertion of it:

- no `PathTarget` — goopg carries whole tuples where PG projects, visible in
  EXPLAIN as `width=550` against PG's `width=2` on the same scan;
- no partial-aggregate split (PG plans Partial HashAggregate → Gather Merge →
  Finalize GroupAggregate; goopg plans one HashAggregate over a Gather);
- outer and semi joins peeled out of the join search, so mixed FROM lists
  collapse into pairwise problems;
- cost GUCs that never reach the planner (`defaultCostParams()` is hard-wired);
- no extended statistics, no MCV pairing in `eqjoinsel`, no `cost_material` or
  `cost_rescan`.

## 7. Caveats on these numbers

- **One run per arm.** The 1.7 % drift between two runs of the same binary (§1)
  bounds what a single figure means. Q5's −32.2 % and Q7's −17.2 % are outside
  it; nothing sub-second is.
- Light unit-test runs shared CPU during part of the first A/B's `before` arm.
  That biases *against* the fix, so it cannot manufacture the improvement, but
  it makes −10.5 % an upper bound.
- **Not comparable to the recorded 227.0 s baseline**: different binary,
  different histogram state, and the cluster was restarted and re-ANALYZEd
  during diagnosis. The 288.10 s figure is this session's own control.
- TPC-DS was not re-measured; :65437 was never started.
- The two TPC-H clusters hold different `lineitem` loads (+0.040 %), and
  `shared_buffers` is 4× apart on top of the `work_mem` and
  `effective_cache_size` gaps P0-12 records. Cross-engine timing comparisons
  remain confounded until P0-12 lands.
