# R46 — Report: cost the legacy funnel's index-vs-seq choice (K98)

*Scan-type axis. Design: `DESIGN.md` (reviewed 2026-09-10,
APPROVE-WITH-NOTES, notes applied; design commit `ffab1708d`).*

## Change

`internal/optimizer/planner.go` (`seqWinsEqualityProbe` +
`indexLeadsRegIdentifierArray` + one call in the equality arm),
`internal/optimizer/scan_input_rewrite.go` (thread
`PlannerSettings`, gate the `eqKey` rewrite),
`internal/optimizer/legacy_scan_choice_test.go` (5 new pins),
`joinsearchseam_test.go` (signature update),
`m0116_multicol_indexonly_test.go` + `btree_array_key_indexonly_test.go`
(fixture re-baselines, below).

Both candidates priced by the search's own functions
(`costIndexScan` vs `costSeqscan`); strict `<` (ties keep the
index = today's behavior). Decline paths are the existing
fallbacks (caller Seq Scan; absorber keeps SeqScan + conjunct).

## What it fixed

- **TPC-DS Q9 → MATCH** (scan-type was its sole category) — the
  programme's first TPC-DS match. `Index Scan using reason_pkey`
  → `Seq Scan on reason` + Filter, PG's shape.
- `scan-type` 74 → 73 on TPC-DS; TPC-H categories byte-identical.

## Implementation findings (all in DESIGN.md §2/§3/§4)

1. **Two producers, not one.** The absorber pass
   (`rewriteScanInputsWithSingleTablePredicates`) rebuilt the
   declined IndexScan (live trace proved funnel verdict=true yet
   EXPLAIN showed Index Scan). Both gated.
2. **Reg-identifier carve-out + K100.** Sequential reg*[]
   comparison is broken twice over (scalar-cast leak,
   OID-vs-name compare without catalog); gate skips those
   indexes, documented workaround with ticket.
3. **Fixture re-baselines (R14 precedent).** 5 tests pinned
   unconditional-index shapes on 2-row tables: scaled to 2000
   distinct rows + ANALYZE in vacuum-then-analyze order. Notes:
   ANALYZE in the seeding txn is a no-op; TOAST compresses
   width-pads (measured), so scale via row counts.
4. **Build hygiene.** One `rtk go build` produced a binary with
   systematically-shifted widths corpus-wide (cause undetermined);
   `go clean -cache` + rebuild reproducibly yields HEAD-like
   widths. All reported numbers are from the clean binary with
   inode-verified serving (`launch-verified.sh`).
5. **Unexplained transient (recorded honestly).** `TestIOS_CompositeInt4Int4`
   passed 4× at 3 rows mid-session, then failed 20/20 on the same
   tree; suspected stale test binary in the stash-pop window, not
   reproduced since. Current state deterministic.

## Gates (all 2026-09-10, clean binary)

- Units: `RALPH_PRECOMMIT_SCOPE=units` pass; optimizer + executor
  suites pass (5 new R46 pins).
- TPC-H spotcheck: Q12/Q13 PASS.
- SF0.5 sweep: `PASS=95 (57 ck-verified) MISMATCH=0 CKMISMATCH=0`;
  plan-shape channel: 98 same / changed=Q9 (intended). Slower:
  Q74 2s->5s, Q44 3s->6s (non-blocking channel; no verdict change).
- Parity A/B (HEAD binary vs R46, identical data, pinned env):
  TPC-DS only Q9 moves (+ PID-noise error sections); TPC-H 22/22
  identical sections.
- Final verdicts: DS `match=1 shapediff=69` (scan-type 73);
  H `match=1 shapediff=19` — categories otherwise identical.

## Evidence

- `/tmp/pp2/oc-ds05-r46c.txt`, `/tmp/pp2/oc-tpch-r46c.txt`
  (R46 captures, clean binary); baselines `oc-ds05-goopg.txt`,
  `oc-tpch2-goopg.txt`
- Sweep: `bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260910-064731.txt`
- Servers: `:5553` (DS05 clone), `:5554` (TPC-H clone); foreign
  `:5545` untouched. (Note: a transient `:5556` probe on the same
  data dir stopped `:5553`'s server — the launcher stops any server
  on the dir by design; relaunched clean and re-verified after.
  Never share a data dir between two servers.)
