Task: **M0125-0042** — landed and committed this loop. See "In-flight" below for
the one process note the NEXT loop must act on.

Files: `internal/planner/exists_to_any.go` (`fixInExprOperandIndex` +
`resolveHostColumnIdx`, wired into `rewriteExistsToAnyNode`),
`internal/planner/exists_to_any_test.go` (4 new pins),
`docs/design/0125-0042-in-sublink-operand-stale-index.md` (status: FIXED),
`docs/design/README.md`, `.ralph/fix_plan.md`, `.ralph/deferral_ledger.md`.

Findings — do NOT re-derive: fix is landed and gate-verified (see design doc
`## Bar for the fix — MET`). `probe35g.sql`→1294, `pAA.sql`→377, both = PG.
TPC-DS SF0.5 full sweep: PASS=89 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=6
SKIP=4. Timeout class is now **Q30 Q64 Q65 Q72 Q78 Q81** (6 — Q72 newly
confirmed this loop, add it to any future -0037/-0038 scoping).
`make plan-diff LABEL=m0125-0005-relsize-default-stage2` shows 22/22 DIFFER
— confirmed PRE-EXISTING (byte-identical with/without this change, via
`git stash` A/B on the same cluster): an ANALYZE-stats drift on the current
`bench/tpch` cluster (`Seq Scan … (stats) rows=1500000` vs the baseline's
no-stats estimate), NOT a regression. Two adjacent gaps remain open in the
ledger, NOT fixed this loop: (1) `remapByPosMap` has no `*InExpr` arm, so a
*correlated* `IN (subquery)`'s `OuterColumnRef`s are never posMap-translated;
(2) EXPLAIN prints a ColumnRef's Name even when its Index has drifted, so
this whole defect class is invisible to plan-reading triage; (3, filed this
loop) `rewriteExistsToAnyNode` never walks `*Project`/`*Aggregate` target
lists, so a hand-written IN in a SELECT-list position is outside this fix's
reach.

**Process note (read before selecting anything):** this loop opened by
resuming the PREVIOUS loop's "Next step" (implement the M0125-0042 fix)
without re-reading the Current Priority banner first. Commit `da882af6`
(chore: file M0125-0043), already at HEAD before this loop started, had
amended the banner so **`M0125-0043` outranks `M0125-0042`/`-0041`/`-0034`
inside M0125** — see `.ralph/fix_plan.md` line ~1545 "⚡ SELECT THIS FIRST
WITHIN M0125". The -0042 fix was kept and landed anyway because it was
already root-caused, implemented, and fully gate-verified (silent-wrong-
answer correctness fix; discarding verified work would have been worse than
the ordering slip) — but this is a one-time exception, not a precedent.
**Next loop: re-read the banner FIRST, every time**, then select
`M0125-0043` (benchmark-name hardcoding: `internal/executor/operators_ddl.go`
+ `internal/initdb/open.go`, `SmallDimension` name tag on literal "region"/
"nation"). Its design doc does NOT exist yet — `docs/design/0125-0043-smalldimension-name-tag-extinction.md`
must be created in the same loop that starts it, and must list the affected
TPC-H query numbers. Full item body: `.ralph/fix_plan.md` search
`M0125-0043`.

Gates run this loop (all PASS): `go build`/`go vet ./internal/planner/...`;
`go test ./internal/planner/...`; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh`; `scripts/tpch-spotcheck.sh` (Q12=2/Q13=35);
`make plan-diff LABEL=m0125-0005-relsize-default-stage2` (pre-existing
divergence confirmed via A/B, not a regression); full 99-query TPC-DS SF0.5
sweep via `scripts/tpcds-sf05-regression.sh sweep`.

In-flight: none. All benchmark clusters started this loop were stopped
afterward: TPC-H bench goopg (:65433, `bench/tpch/stop_goopg.sh`), TPC-DS
sf05 goopg (:65437, stopped itself at the end of the sweep). PG oracle
(:65438) was already UP from a prior loop and is left UP, unchanged. TPC-DS
sf1 (:65436) stayed down throughout.
