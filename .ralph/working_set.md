(idle — nothing in flight)

M0127-P5.6-f-ii is DONE, gated and committed.

Two things the next loop should NOT re-derive:

1. **A `joinEdge`'s key `ColumnRef.Index` is in GLOBAL FROM-list coordinates**,
   not table-relative. Q5's `c_nationkey` arrives as `Index: 16` against an
   8-column `customer`. This loop fixed it at the READER (`edgeColName` now
   prefers the NAME, `accurateKeyDistinct` resolves via `tableColumnIndex`);
   the WRITER still emits the global index and nothing marks it. Ledger row
   dated 2026-08-05 — the next reader to index a per-column slice with it
   reproduces the defect silently.
2. **The plan-shape baseline was re-pinned** to
   `plan_snapshots/m0127-p56fii.txt` (19/22 diverged from `m0127-p21-hashkeys`
   by design; every divergence verified neutral-or-faster by wall time). Any
   plan-gate diff against an older snapshot is comparing across planners.
   P5.9's §4 ratchet baseline supersedes it at the flag flip.

Estimate-audit baseline for the next loop:
`analysis/leftdeep-joins/2026-08-05-p56fii.txt` (+ `-README.md`). The rejected
half-fix is kept beside it as `-halfway.txt` and is the argument for why a
partially truthful estimator is a new defect rather than half a fix.

Next in the M0127 order (the banner selects, not this note): **P5.6-g** —
`eqjoinsel_semi`'s MCV arm + the `(1 - nullfrac1)` factor, which owns both
remaining audit violations (Q18 SEMI 42837× over, Q21 ANTI 4003× under).
P5.6-f-iii (the DS05 TIMEOUT attribution) is still open and still needs two
sweeps, not a code fix — note that Q47 stayed the victim this sweep.

Gates run: build + vet + gofmt-clean; planner `go test` PASS; UNITS PASS
(`/tmp/units_p56fii.log`); SPOT PASS Q12=2 Q13=35 (`/tmp/spot_p56fii.log`);
DS05 PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1 (Q47) SKIP=4
(`/tmp/ds05_p56fii.log`); PLAN 22/22 MATCH after re-pin; estimate audit
(violations 2 → 2, Q9 measured); pgbench smoke via the commit hook.

Nightly triage: the same 17 `AI-20260804-005028-*` subjects, all already filed
under M-NIGHTLY. Nothing new to file.

In-flight: none.
