(idle — nothing in flight)

Last loop (#7, 2026-07-30 16:42–19:0x): **M0125-0002 commit 2 of 8 is DONE and
committed** — `cloneExprShiftIdx` re-based onto `cloneExprRefs`/`exprChildSlots`.

Files: `internal/planner/nl_index_join.go` (`cloneExprShiftIdx`),
`internal/planner/nli_shift_arms_test.go` (NEW, 7 pins),
`internal/planner/exprwalk_inventory_test.go` (pin demoted),
`scripts/tpcds-sf05-regression.sh` (stale `unset(off)` label → `unset(2)`),
`docs/design/0125-0002-…md` (Commit-2 execution record),
`.ralph/fix_plan.md`, `.ralph/deferral_ledger.md` (2 rows),
`analysis/m0125-0002-c2-sf05-plans-20260730/`.

## Facts the next loop should NOT re-derive

- **D2 row 2 ("commit 2 does move plans") is REFUTED by measurement.** TPC-H
  22/22 MATCH in `MODE=strict-text` (byte-identical); TPC-DS SF0.5 96/96
  byte-identical `EXPLAIN`. The 20 newly admitted kinds never reach that site.
- SF0.5 answer sweep ran anyway and had to: the old arms dropped
  `BinaryOp.ResultType` / `FuncCall.Variadic` / `FuncCall.ReturnType` on every
  hoisted conjunct, which `EXPLAIN` cannot show. **PASS 83 / TIMEOUT 12 /
  MISMATCH 0 / CKMISMATCH 0 / ERROR 0**, all 50 cks equal to baseline.
- **`Q72 TIMEOUT 307 s → PASS 313 s` is a CAP FLAP, not a rescue** — slower run,
  still over 300 s, byte-identical plan. Do not cite it as a win.
- Commit 1's owed SF0.5 arm was already discharged: §D8's sweep ran at
  `e29faca9`, which contains `da6d2c0c`.
- **Use `LABEL=m0125-0005-relsize-default-stage2`** for every remaining commit
  in this series. `tpcds-round2-head` predates the relsize flip; bare
  `plan-gate` picks newest-by-mtime.
- Timed 22-query TPC-H run NOT executed (byte-identical plans); ledger row says
  it is mandatory again at the first commit with a non-empty plan diff.

## NEXT (banner order)

**A USER DIRECTIVE 2026-07-30(b) landed MID-LOOP** (a concurrent `ralph_loop.sh`
was live) and reorders the queue: **`M0125-0028` → `-0029` → `-0030`** (the
warm-statistics programme) come **before** `M0125-0002` commit 3 and the -0003
four-arm study. `-0028` is small and host-independent. Read the directive block
in `.ralph/fix_plan.md` (around line 240) before selecting.

Gates run: units precommit PASS; `go build ./...` + `go vet` clean; census gate
PASS; 7 new pins PASS **and proved to fail before the change in both
directions**; `make plan-diff` structural AND strict-text 22/22 MATCH; SF0.5
`EXPLAIN` 96/96 identical; SF0.5 answer sweep 99/99; `make ralph-state-guard`
OK; pgbench smoke via the commit hook.

In-flight: none.
