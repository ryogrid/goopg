# M0125-0002 commit 3 — `visitColumnRefs`, measured

2026-08-03, LOADED host (the nightly `ci/batch` TPC-DS stage was live on
:65435 throughout — EXPLAIN-only instruments were chosen partly for that
reason; nothing here is a timing).
Design: `docs/design/0125-0002-walker-conversion-and-mhj-composition-risk.md`
D2 row 3 / D4, execution record §"Commit 3 of 8".

## What was measured

D2 row 3 said commit 3 "changes which refs get re-resolved by name" at the
three rebind call sites (`reresolveExprByName`, `reresolveJoinByName`'s
`predRebind`, `nl_index_join.go`'s leftover rebind). The conversion admits
~10 newly-visited same-scope shapes (refs under `IS NULL`, casts, row
constructors, `IN`-list elements, subquery-node PARAM_EXEC `Args`, …).

| instrument | arms | result |
|---|---|---|
| TPC-H plan snapshot, 22 queries (`:65433`, fresh capped server per arm) | `plan_snapshots/m0125-0002-c3-before.txt` vs `-after.txt` | **byte-identical**, and both == `post-mhj-retire` (the 2026-08-02 baseline) |
| TPC-DS SF0.5 `EXPLAIN`, 96 queries (`:65437`, fresh capped server per arm) | `head/` vs `c3/` | **96/96 byte-identical** |
| divergence probe, both benchmarks, 118 planned queries | `probe` arm | **0 `C3DELTA` lines** |

## Why the probe exists — EXPLAIN is blind to exactly this commit

M0125-0042 established that goopg's EXPLAIN prints a predicate's **Name over
its (possibly wrong) Index**. This commit's only behavioural surface is
`ColumnRef.Index` mutation, so byte-identical EXPLAIN output cannot by itself
excuse the answer sweep — that was commit 2's lesson generalised.

The probe closes the hole: a measurement-only binary (built in a throwaway
worktree, never committed) runs BOTH implementations inside
`visitColumnRefs` — the old 7-arm switch and the new `walkExprRefs` driver —
and logs `C3DELTA` to the server log whenever the visited `*ColumnRef` sets
differ (pointer-for-pointer, in order). All three call sites run at plan
time, so planning all 118 benchmark queries exercises the full rebind
surface. Zero deltas ⇒ identical visit sets ⇒ identical Index mutations ⇒
the executed plans are identical, not merely identically printed.

## Gate disposition for a zero-hunk commit

- D4 item 3 (timed 22-query TPC-H): **not executed** — byte-identical plans
  plus a zero-delta probe mean any number would be host noise (commit 2's
  reasoning, strengthened; ledger row 2026-08-03). Owed again at the first
  commit with a non-empty diff.
- D4 item 4 (SF0.5 answer sweep with checksums): owed on "first/last/any-hunk
  commit"; commit 3 has zero hunks and, unlike commit 2 (whose old arms
  *rebuilt* nodes and dropped type metadata), the conversion is read-only —
  the probe shows the callback stream itself is unchanged. Not run.
- `make plan-diff` label note: `m0125-0005-relsize-default-stage2` (commit
  2's retarget) is now itself stale — `e85e5347` (M0126-0011) retired MHJ
  packing and re-baselined to `post-mhj-retire`. The A/B above diffs the two
  arms directly, which no label staleness can contaminate; `post-mhj-retire`
  is the label later commits should name.

## Also in this loop (separate commit `4fb87456`)

`TestMHJParallelNoDuplicates` had been red at HEAD since `e85e5347` — that
commit updated the three planner-side MHJ tests to opt in via
`SetMHJPackingEnabled(true)` but missed `internal/executor/parallel_mhj_test.go`,
breaking the units pre-commit gate for every subsequent commit. Both tests in
the file (the identity test had gone silently vacuous — green while planning
no MHJ) now opt in.
