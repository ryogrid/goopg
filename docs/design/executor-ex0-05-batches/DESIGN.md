# EX0-05 Design — Batch/width counter verification + witness record

Item: `TODO_EXECUTOR.md` EX0-05 (gate: counters report on witnesses; pin
`changed=0`). Status: design for review. This item is VERIFY-AND-RECORD,
not build: the hash `Batches:` line exists and matches PG text verbatim
(`formatHashJoinInfoLine`, `operators_explain.go:211-233`, PG
`explain.c:3375-3460` incl. the either-moved rule and kB round-up);
worker max-merge closed under EX0-03; Agg/Sort/Gather batch reporting is
at parity-with-PG nil gap (no spill batches in either engine's line,
goopg agg cannot spill). Known divergences (JSON twin absent, line hangs
off Join not Hash, `Build Time:` extra) are ledgered and out of gate.

## Work (single run, no code unless a witness is silent)

1. Fresh capped server, TPC-H SF=1, S-cold, GOGC=100, `work_mem=64MB`
   (already the cluster default via P0-12 — `setup_goopg.sh:71` writes
   it; no override, but record `SHOW work_mem` +
   `effective_cache_size` in the header per protocol): `EXPLAIN
   (ANALYZE)` Q9 in TWO arms — suite-default parallel AND serial
   control (`max_parallel_workers_per_gather = 0`, 09 §6) — so the
   P4-01 exit diff (8→1) has a regime-matched BEFORE on both arms.
   Capture every hash build's `Batches:` component; NAME the witness
   build (take2's ~97 MB top-level build) so the 8→1 diff has an
   anchor. Tripwire: a witness build rendering no `Batches:` component
   (even a `Build Time:`-only line — always-set `BuildTimeNs` renders
   while PG would print nothing) FAILS the item into build scope.
   (All batch lines render under ANALYZE only — sole call site is the
   ANALYZE walk; plain EXPLAIN lacks the line in both... in goopg only,
   PG prints it whenever nbatch>0. Qualified, out of gate.)
2. Record narrowed widths beside it, PP separate-column style (take3
   09 §5 P4 row): per-build `width=` from the same EXPLAIN; expect
   ≈100 (the TODO/09 "≈100 not 6" phrase — flag any width≈6
   degenerate, the index-only-scan trap shape).
3. Publish as `analysis/executor-refactor/ex0-05-20260903/README.md`
   with the §2 header (EX0-02 protocol): this is the BEFORE record the
   P4-01 exit (`Batches:` 8→1) diffs against.
4. Pin: `make plan-gate` 22/22 MATCH against the stated baseline
   (warm-stats-base Aug-2026 pin / P0-08 re-pin) + `git diff
   --stat` showing docs/analysis-only. Sanity expectation only: 8
   batches (take2-side source, NOT EX0-02 — re-confirm shape, the
   BEFORE is whatever HEAD produces; do not confuse with the 4 MB
   "128 batches" figure).

## Build trigger (only if step 1 finds silence)

If any Q9 witness hash build renders no batch line (unrun-build gate or
missing `publish()` call), the item expands to plumbing the missing
counter through `HashJoinStats` with a golden test — same MAX-merge rule,
TEXT-only (established escape clause). That expansion is a second commit,
not scope creep: the gate ("counters report on witnesses") demands it.
