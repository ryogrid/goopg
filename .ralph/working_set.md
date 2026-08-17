# Working set — M-NIGHTLY AI-20260817-011734-002/-003 (LANDED `6536bdf5`)

**Task:** M-NIGHTLY nightly triage, items -002/-003
(`TestE2E_PG{Cold,Crash}StartOnGoopgDataDir`). Selected per the Current Priority
banner: M-NIGHTLY outranks M0134 and had 6 unchecked items.

**Landed:** a live **data-corruption** fix, not an E2E-harness fix. Both tests
died on the goopg side before PG was ever attached. S4's Filter-drop (`aa40caa6`,
M0134-0001) made `tryRangeIndexScan` return a **bare** `*IndexScan`;
`extractScan` (`internal/executor/operators_storage.go:3211`) degrades an
`*IndexScan` DML child to `SeqScan + indexScanPredicate(ix)`, and that function
handled only `Key != nil`, returning `nil` otherwise — safe only while a `Filter`
always wrapped the scan. With no Filter, `scanMatching` ran with **no predicate**,
so `DELETE`/`UPDATE ... WHERE <indexed_col> <range-op> <const>` hit EVERY row
(verified: `UPDATE ... WHERE id > 15` updated 20 of 20). Fix: reconstruct the
bound for every shape — `Key`, `Keys` (multi-column probe, same latent hazard),
`LowKey`/`HighKey` honoring `LowOp`/`HighOp` — via a new `indexScanColumnRef`
helper. Planner deliberately untouched.

**Invariant (do not regress):** `indexScanPredicate` returning `nil` means
"match every row". Any new `IndexScan` field that restricts rows (backward scan,
partial-index predicate, skip prefix) MUST be reconstructed there.

**Files:** `internal/executor/operators_storage.go`,
`internal/executor/range_dml_predicate_test.go` (new),
`docs/design/0134-0001-p2-explain-format.md` (new §"S4 follow-up"),
`.ralph/fix_plan.md`, `.ralph/deferral_ledger.md`.

**Gates run:** `go build ./...` PASS; `internal/executor` + `internal/optimizer`
PASS; new tests FAIL-pre / PASS-post (RowsAffected 10/10 before);
both E2E repros PASS; `RALPH_PRECOMMIT_SCOPE=units` PASS; `scripts/tpch-spotcheck.sh`
PASS (Q12=2, Q13=35); pre-commit pgbench smoke PASS.

**Deferral ledger:** 1 row (2026-08-17) — the degrade-to-SeqScan contract itself:
DML never uses the btree for access (PG's `nodeModifyTable.c` has no predicate
reconstruction step at all) and the reconstruction is a hand-maintained mirror of
the planner's bound fields. Resume: drive `deleteOp`/`updateOp` from the child
plan's own operator, as `updateOp.updateViaIndex` already does for equality.

**Next step:** M-NIGHTLY still has 4 unchecked items from run `20260817-011734`
(banner keeps M-NIGHTLY first). Take **AI-20260817-011734-004**
(`TestPort_IsolationIndexOnlyBitmapscan`) next — same previously-unattributable
set, and index-adjacent, so re-run its repro at HEAD first: it may already be
fixed by `6536bdf5`. Item -001 (race/internal/initdb) is still expected stale
(run predates `83dd7ae8`); verify with
`make race-gate RACE_TIMEOUT=45m RACE_SHARD_ONLY=1` when a ~20 min slot is free.

**Delegation:** `tmp/ralph-handoffs/m-nightly-20260817-e2e-pgstart/` — tester
triage (DONE), researcher root-cause (DONE), implementer (DONE, 1 round), tester
gate round (DONE). All rounds converged first try.

**In-flight:** none.
