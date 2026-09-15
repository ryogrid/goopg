Task: M0141-S2a-fix2 (banner's TOP-PRIORITY group, "M0141-S2a-fix and
M0139-0007 — costing-order unblock"). **ATTEMPTED, MEASURED, and DECLINED
this loop** (`1ed4c023e`, pushed). Code implemented, tested green, measured
cleanly on private clones, then REVERTED after measurement showed no net
gain (TPC-DS regression, TPC-H lateral). This is a closed decision, not
unfinished work.

Files: `docs/design/0100-0149/m0141-s2a-fix2-hashaggentrysize-currency-attempt.md`
(new, full derivation + both measurements + HOLD decision),
`docs/design/README.md` (+index row), `.ralph/fix_plan.md` (M0141-S2a-fix2
checked off `[x]` with the HOLD result, NOT a production change),
`.ralph/deferral_ledger.md` (+row, task-id `m0141-s2a-fix2`),
`analysis/m0141/m0141-s2a-fix2-{tpch,tpcds}*` (4 committed measurement
artefacts). `internal/optimizer/cost_funcs.go` and its 4 test-file
companions were edited, measured, THEN `git checkout --`'d back to HEAD —
the tree carries NO `costAgg` diff from this task; do not expect to find
one.

Key symbols: `costAgg`'s SPILL ARM (`cost_funcs.go:431-542`, unchanged at
HEAD), `hashAggEntrySize` (unchanged), `hashsize.EntryBytes` (the
PG-equivalent full-row currency that WAS tried as the arm's `tupleWidth`
input instead of bare `inAvgVarBytes` — declined).

Findings: fix2 restores "Arm C" (fixed-width/avgVar=0 aggregate inputs
pricing a real spill footprint) exactly as R120 once did. Measured with a
same-PG-reference control (TPC-H, to strip PG-side ANALYZE sampling noise
between independent captures — the naive same-run diff showed spurious
multi-category movement that `shape-delta.sh` proved was NOT a shape
change) and matching-stats-epoch comparison (TPC-DS): TPC-H match held at 8,
Q18 shape-changes LATERALLY (+1 sort-strategy, -1 rendering, still
SHAPE-DIFF); TPC-DS match held at 2, Q31 shape-changes and REGRESSES 3
categories (aggregation-strategy/sort-strategy/parallelism, +1 each) back
to byte-identical with the PRE-fix1 baseline — exactly cancelling fix1's
one TPC-DS gain. No MATCH lost anywhere (hard floor safe), but zero net
category gain, reproducing R124 §7's identical net-neutral verdict for the
same currency correction a SECOND time, now with fix1's live-at-cost-time
`inNcols` preview the task hoped would change the outcome. It did not.
**Treat this currency as closed** — do not re-attempt the same substitution
without new evidence (different mechanism or different quantity).

In-flight: none. All private artefacts (binary, TPC-H/TPC-DS clone dirs,
server logs, cgroup scopes) removed after use; shared clusters
(:65432/:65433/:65437/:65438) read-only, never restarted.

Next step: re-read the `## Current Priority` banner fresh (check date/content
match before trusting this note). If unchanged, the top-priority group's
M0141-S2a-fix/M0139-0007 line is now FULLY EXHAUSTED as scoped (fix1 landed,
fix2 attempted-and-declined) — move to the next items the banner names inside
the SAME top-priority group: **M0139-0007a** (measure/adopt-or-hold the two
already-built R108/R113 absorption arms — `GOOPG_PG_HASH_TUPLE_SPILL_COST` and
`GOOPG_PG_SORT_RELATION_BYTES_COST`, both default-off, never measured against
the post-M0137–M0142 corpus) or **M0139-0007b** (port Memoize's entry-byte
currency via `pgRelationByteSide`/`ExecEstimateCacheEntryOverheadBytes`).
Neither depends on this loop's outcome. If the banner has since moved the
top-priority group past M0141-S2a/M0139-0007 entirely, follow the banner
instead — it is the sole ordering authority per PROMPT.md's precedence rule.

Gates run: `go build ./...` clean (both with the attempted change and after
revert). `go test ./internal/optimizer/...` all pass (both states). Live
TPC-H/TPC-DS parity measurement on private clones (see design doc for full
numbers). `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`:
FAILS on `internal/parser` only — this is the PRE-EXISTING, already-tracked
`RangeVar.GroupedJoinUnaliased` AST-drift regression (nightly
AI-20260914-235643-001/003, and this file's own "Manually discovered"
section, dated 2026-09-15, predates this loop) — unrelated to this task's
diff (confirmed: this loop touches zero files under `internal/`).
`internal/optimizer` itself reports `ok` (cached) inside that same run.
Pre-commit pgbench smoke PASS (hook-enforced; commit succeeded). `make
ralph-state-guard` clean after one self-repair (same recurring benign
stale-clean-exit-marker pattern noted by the last several loops).
