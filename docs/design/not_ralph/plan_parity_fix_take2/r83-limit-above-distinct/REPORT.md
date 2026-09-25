# R83 — Report: LIMIT above DISTINCT (Q41)

*Scan-type/ordering axis. Design: `SCOPE.md` (reviewed
APPROVE-WITH-NOTES, notes applied; scope commit `66b8fb7`).*

## Change

`internal/optimizer/planner.go` (deferred-LIMIT wrap +
`selectSrfPending`/`ps` in-scope verification),
`internal/optimizer/tuplefraction.go`
(`limitBoundMovable`: IntegerConst-only allowlist, fail-closed),
`internal/executor/distinct_limit_test.go` (new P2 pin).

`SELECT DISTINCT … ORDER BY … LIMIT n` now plans
`Limit → Sort → Unique → …` (PG's shape) instead of
`Sort → Unique → Limit → …`. Decline preconditions keep
today's order for WithTies, DISTINCT ON, SRF expansion,
and non-constant bounds.

## What it fixed

- **Values-correctness:** Limit-below-Unique truncates
  pre-distinct rows (wrong whenever duplicates exceed the
  limit; masked on current data by 78 < 100). Pinned by a
  synthetic 150×'a'+50×'b' DISTINCT+LIMIT 100 test returning
  `{a,b}` — verified FAIL-before / PASS-after via stash.
- **TPC-DS Q41:** `Sort→Unique→Limit→Sort` →
  `Limit→Sort→Unique→Sort` (P1 shape exact). Remaining gaps
  are the NAMED follow-ups (top M0097-0046 Sort, `$0`
  display); categories unchanged (honest miss on the 3→1–2
  metric prediction — the placement moved, the metric
  didn't, because the leftovers each hold a category).

## Gates (all 2026-09-12)

- Units: ralph-precommit scope green except pre-existing
  `bak/` debris (untracked, not mine — fails identically
  without my diff); optimizer + executor full suites fresh
  green (testcache cleaned).
- TPC-H spotcheck Q12/Q13 PASS.
- SF0.25 sweep: `PASS=96 MISMATCH=0 CKMISMATCH=0`;
  plan-shape channel 98 same / changed=Q41 (intended).
- DS A/B (same clone): only Q41 moves (+ PID-noise error
  sections); Q6/Q38/Q54 byte-identical (STOP condition
  held).
- TPC-H A/B: 22/22 identical vs a TRUE HEAD worktree
  binary — an initial 18-query diff against stale
  `tmp/goopg-oc-base` was binary-provenance drift, caught
  and re-done correctly (inode verification alone is
  insufficient — lesson recorded).

## Evidence
- `/tmp/pp2/r83/ds-r83.txt`, `/tmp/pp2/r83/tpch-r83.txt`,
  `/tmp/pp2/r83/tpch-truebase.txt` (captures)
- Sweep: `bench/tpcds/runtime_goopg/tpcds-results-sf025/sweep-20260912-042808.txt`
- Servers `:5557` (DS), `:5554` (TPC-H); peer `:5533`
  and PG refs untouched.

## Follow-ups (from implementation review, APPROVE-WITH-NOTES)

- `ParamRef` allowlist in `limitBoundMovable`: param-LIMIT +
  DISTINCT keeps today's (wrong) order — fail-closed, zero
  corpus impact (all corpus LIMITs are integer literals), but
  the values bug persists for that case. Position-independent
  like IntegerConst; extend or file separately.
- M0097-0046 outer-Sort elimination (top Sort extra vs PG).
- `$0` correlated display (PARAM_EXEC vs outer-var).
