(idle — nothing in flight)

M0127-P3.4 is CLOSED and committed. **S3 continues; P3.5 is next.**

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P3.5` (EXPLAIN `Batches:`/memory lines +
forced-spill identity test; S3 EXIT evidence = Q21 SF1 completes capped,
artefact `analysis/leftdeep-joins/…-s3-spill.txt`).
Bar: Q21 SF1 (capped) + DS05 zero-delta + RACE.**

Carry-over facts a next loop should not re-derive:

- **P3.4 landed a MEASURED perf regression on the TPC-H spotcheck**:
  15.7 s → 28.3 s query phase (rows still 2/35). Three-arm A/B on one
  host: HEAD Q12 11.58 / Q13 4.14; shared-decline alone Q12 15.84 /
  Q13 3.89; full P3.4 Q12 16.39 / Q13 11.89. Q12 = N private builds
  replacing one shared; Q13 = its LEFT join honouring work_mem on a
  1.5M-row build. Both are the design's mandate and are LEDGERED
  (3 rows `2026-08-03 M0127-P3.4`) — do NOT "fix" them as a bug; they
  are input to the S1/S5 acceptance measurement and to P5.7's
  `hashJoinCost` nbatch I/O term.
- **goopg's `work_mem` BootVal is 512MB** (`internal/config/defaults.go:686`),
  128× PG's 4MB — relevant whenever "goopg spills more than PG" is
  suspected. It doesn't.
- **`joinBatchEligible` == `hashJoinIsPartialCapable`** (planner/parallel.go)
  by construction now: INNER/SEMI/ANTI/LEFT-!BuildLeft. Keep them in step.
- **`buildGeometry` (operators_join_agg.go) is the ONE sizing derivation** —
  presize, batch state and the shared-build decline all read it.
- **P3.5's EXPLAIN counters** live on `joinOp.batches`: `nbatch`,
  `origNBatch`, `peakSpace`, `innerSpilled`, `outerSpilled` (ledger row
  `2026-08-03 M0127-P3.2`, resume point `operators_explain.go`).
- **Repo gofmt baseline is go1.25; local gofmt is 1.26** — never `gofmt -w`.
- **Do NOT `git stash`** in this tree (9+ unrelated entries). To A/B against
  HEAD, copy the changed files to /tmp, `git checkout --` them, measure,
  copy back — that worked cleanly this loop.
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is NEVER modified
  except its IMPLEMENTATION-TODO checkboxes.

Gates run this loop: UNITS PASS; RACE PASS (`make race-gate`, all packages);
DS05 slices 1-50 / 51-75 / 76-99 MISMATCH=0 CKMISMATCH=0 ERROR=0 over all 99
(Q72 TIMEOUT pre-existing); SPOT PASS on rows (Q12=2 / Q13=35) with the timing
regression above; SMOKE via the commit hook; `make ralph-state-guard` OK.
PLAN not run — no plan surface touched.

In-flight: none.
