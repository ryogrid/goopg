(idle — nothing in flight)

## Loop summary (2026-07-11, loop #57)

**Nightly triage:** action-items batch `20260711-011536` — all 3 AI items already
`[x]` in fix_plan.md (same batch triaged loop #56, co-load timing flakes). No new
nightly work.

**Task — M0122-0004: RANGE value-offset window frame bounds** (last window-frame
gap, follow-up to commit 794872f4 which did RANGE non-offset bounds).
Implemented `RANGE BETWEEN n PRECEDING AND m FOLLOWING`: a row is in-frame when its
ORDER BY value is within `current±offset` (PostgreSQL in_range).

Files:
- internal/analyzer/analyzer.go: validateWindowFrame requires exactly one ORDER BY
  column for a RANGE value offset (42P20) instead of rejecting 0A000.
- internal/executor/operators_window.go: frameBounds now returns error + dispatches
  RANGE value offsets to new frameBoundsRange (scans partition for head/tail per
  nodeWindowAgg.c in_range semantics; NULL handling matches PG). Helpers:
  rangeStartReached/rangeEndReached/inRange (reuse evalBinary + compareDatum),
  resolveRangeOffset (22004 null / 22013 negative), rangeOffsetNegative, rangePos,
  frameHasValueOffset. Added frameStartOffDatum/frameEndOffDatum fields. Updated the
  4 frameBounds call sites for the new error return.
- Tests: analyzer_test.go TestAnalyzeWindowFrameRangeOffsetOrderByCount (was
  ...OffsetRejected); window_compat_test.go TestCompatWindowRangeValueOffset{,
  PeersAndFrameFuncs,Nulls}, TestWindowRangeOffsetNegative.
- Docs: docs/design/0122-0004-range-offset-window-frame.md (+README row); ledger row;
  unimplemented_feat.json two window-frame entries open→resolved (96 resolved/85 open).

Gates: analyzer + executor + planner suites PASS; go build ./... clean; go vet clean;
6-case cross-check vs live PG 18.3 all byte-for-byte match; tpch-spotcheck PASS
(Q12=2/Q13=33). gofmt flags only pre-existing version-mismatch noise (compareSortDatums
comment), not my code — not touched per goopg_gofmt_version_mismatch_no_w.

Next loop: M0122 has ~19 other open sub-items; unimplemented_feat.json still 85 open
(e.g. Planner GUC stubs wiring, LANGUAGE C stubs, pg_collation_for's broader
collation-tracking scope, FilterOp/SeqScanOp vectorization pair).

In-flight: none
