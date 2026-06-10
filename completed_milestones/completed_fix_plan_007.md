# Completed Fix Plan Archive 007

Archived 2026-06-11 from `.ralph/fix_plan.md` (fully-completed sections moved verbatim).
Earlier archives: `completed_fix_plan_001.md` .. `completed_fix_plan_006.md`.

## Maintenance Fixes

- [x] Fix `TestFoundationSeqScanFilterJoin` test 7 stale expectation (2026-05-04).
  rows[0][0] was expected to be "alpha" but alpha's t3.qty=100 is filtered
  by WHERE t3.qty>150; correct first row is [beta 200]. Stale from before
  M0039/M0041 fixed ColumnRef alignment for ≥3-table joins. Row-count check
  promoted from t.Logf to t.Fatalf. File: `internal/testutil/tpch/foundation_test.go`.

- [x] Silence `tmp/` build errors under `go test ./...` (2026-05-04).
  tmp/ utility scripts (find_wal_record.go, tuple_size.go, walprobe_main.go)
  all declared `package main`, causing "main redeclared" errors. Added
  `//go:build ignore` to each. (Note: tmp/ is in .gitignore; change is local.)


## Completed

- [x] Project initialization (Ralph harness wired up).
