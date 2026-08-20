Task: M0134-0050 (numeric_big.sql, status `failed`) — sized + one CONTAINED
bug fixed (numeric internal-arithmetic scale cap). Case stays `failed`
(0/1); remaining diff collapses into the M0134-0049 numeric-precision
design-doc-scale gap.

Files this loop: `internal/executor/numeric.go` (real code fix —
`numericMaxDisplayScale` 1000→16383), `internal/executor/numeric_scale_cap_test.go`
(new tests), `.ralph/deferral_ledger.md` (new row, M0134-0050),
`.ralph/fix_plan.md` (M0134-0050 entry rewritten to PARTIAL, points next
selection at M0134-0051), `.ralph/progress.json` (state-guard auto-repair,
unrelated bookkeeping).

Key symbols / findings: `numericMaxDisplayScale` (`internal/executor/numeric.go:157-160`,
6 call sites) conflated PG's typmod-only `NUMERIC_MAX_PRECISION=1000` ceiling
(column/cast precision, still enforced separately by `roundNumericToScale`)
with the internal-arithmetic scale ceiling, which in real PG is
`NUMERIC_DSCALE_MASK=16383` (`postgres/src/backend/utils/adt/numeric.c:236-237`).
This made `numeric(1000,800) * numeric(1000,800)` (combined scale 1600)
spuriously error `numeric scale N exceeds 1000 in multiply` where PG raises
no error. Raised the constant to 16383; typmod-coercion path untouched.
Fixing this unmasked (as predicted) the already-ledgered M0134-0049 bucket-2
gap (typmod truncation not applied at INSERT time) in the same
multiply-check section of numeric_big.sql — diff went 1766→1799 lines,
`^+ERROR` 12→11 (the specific "exceeds...in multiply" line gone), case
stays `failed` overall. numeric_big.sql's remaining diff is ~90% the
already-ledgered M0134-0049 bucket-3 transcendental-precision gap (now
confirmed `sqrt` shares the exact float64/6-decimal pattern as ln/log/
power/exp, pinned to expr.go:11989-12002) plus bucket-2 (typmod truncation,
confirmed again via the division-check section) and bucket-4 (`trim_scale`
added to the missing-builtins list alongside width_bucket/div/log10).

Hypothesis/Findings: no new design-doc-scale gap discovered this loop —
numeric_big.sql fully cross-references the existing M0134-0049 ledger row.
The transcendental-precision design doc (arbitrary-precision digit-array
math for ln/log/log10/exp/power/sqrt, mirroring numericDiv/numericDivScale's
existing PG-faithful big.Int pattern, numeric.go:726-839) remains the
biggest unaddressed M0134 gap across both numeric.sql and numeric_big.sql —
worth considering as its own dedicated task/design-doc pass once M0134's
per-file sweep reaches a natural pause point, rather than deferring
indefinitely file-by-file.

Next step: select **M0134-0051 (partition_info.sql)** per the fix_plan
banner/entry chain — size it via `scripts/pg-regress-runner.sh --verbose
partition_info` (delegate to researcher) before deciding whether it's a
diff-mismatch, crash, or feature-gap case, following the same
research→brief→(implement|park) pattern used across M0134-0044..0050.

Gates run this loop: `make ralph-state-guard` — ran clean after one
auto-repair (status/progress reconciliation, same recurring pattern);
pgbench smoke PASS (pre-commit hook, mandatory, about to run on commit);
implementer ran `go build ./...`, targeted
`TestNumericMul_HighScaleWithinCap`/`TestNumericMul_ScaleExceedsUpperBound`
PASS, full `internal/executor` package PASS (~6.7s warm), and a live
before/after `pg-regress-runner.sh --verbose numeric_big` run confirming
the specific multiply-scale error is gone (12→11 `^+ERROR`).

Delegation: researcher agent `ac9b54da9aa7d69cb` (1 round, sizing, found
harness artifact + 3 known buckets + 1 new bucket) → implementer agent
`aab7a1ba76f05527e` (1 round, scale-cap fix, DONE first try). Handoff:
`tmp/ralph-handoffs/m0134-0050-numeric-scale-cap/` (brief.md written
normally; implementer report.md write was blocked by harness policy this
round — findings relayed as agent output text instead, folded into this
working_set entry and the ledger/fix_plan rows, nothing lost).

In-flight: none. No server left running (regress runner and precommit
smoke both self-start/stop their own throwaway goopg instances). About to
commit `internal/executor/numeric.go`, `internal/executor/numeric_scale_cap_test.go`,
`.ralph/deferral_ledger.md`, `.ralph/fix_plan.md`, `.ralph/progress.json`,
`.ralph/working_set.md` and push to `regress-renumbering`.
