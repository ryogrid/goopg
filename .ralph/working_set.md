# Working set — M0134-0001 P2 S7 (single-relation GROUP BY pruning)

**Task:** M0134-0001 aggregates.sql — `remove_useless_groupby_columns` single-rel arm
landed: `buildAggregateStage` drops `GROUP BY` cols redundant under a PK/unique index.

**Status:** LANDED + committed + pushed. Code commit (planner.go) + docs/ledger commit.
Aggregates.diff **1474→1390**; hunks d447/d554/d588 CLOSED, d538 (p_t1) SHRUNK, d508
(t1c) must-NOT unchanged. Q12=2/Q13=35 PASS.

**Files (this loop):** `internal/planner/planner.go` (`pruneUselessGroupByColumns`
helper + `aggregateSurface.originalGroupInputCols`/`prunedInputCols` +
`buildAggregateStage` prune/remap + `resolveExprAfterAggregate`/
`resolveTargetsAfterAggregate` passthrough + `isColumnFunctionallyDetermined`
coverage), `docs/design/0134-0001-p2-explain-format.md` (S7 Slice 1 + S6 Slice 3f
d-render-reverted note), `.ralph/deferral_ledger.md` (partitioned-unique-index DDL gap).

**Key symbols:** `pruneUselessGroupByColumns` (planner.go), `buildAggregateStage`
(~6273), `isColumnFunctionallyDetermined` (~13436), `resolveExprAfterAggregate`
(~6896), `resolveTargetsAfterAggregate` (~7314); `catalog.Index.Unique/Primary/
NullsNotDistinct/Deferrable/HasPredicate/ColExprs`.

**Findings:**
- **S6 (d) scalar-subquery is a DEAD END standalone (design-doc §S6 Slice 3f):** a
  d-render attempt (register skipped-Project target-list sublinks) grew the diff
  1462→1474 — the divergence is architectural (goopg `ExecParamRef`→`$0` vs PG outer-Var
  `int4_tbl.f1`; `Aggregate→IndexScan` vs `InitPlan→Limit→IOC`; numbering), not a
  render gap. REVERTED. Closing blocks 8/17 needs a coupled d-rewrite (correlation gate
  + deparse + numbering) — do NOT retry d-render alone.
- **S7 prune is PG-faithful via `prunedInputCols`, NOT a unique-index func-dep
  extension** (reviewer CONFIRMED: `check_functional_grouping` pg_constraint.c:1740 is
  PK-only; extending would flip functional_deps 42803).
- **Reviewer finding-1 fixed:** partitioned parent whose unique key omits a partition
  col is not globally unique (goopg lacks indexcmds.c:1093 0A000) → prune fails closed
  when `len(PartitionKey)>0` and any partition col ∉ bestKey. DDL gap → ledger.

**Next step — S7 residuals (in order):** (1) multi-relation arm of initsplan.c:412
(d463/d1234 — iterate every RTE_RELATION; the EC/pathkey pass); then (2) S7 GROUPING()
guard is stricter than PG (fail-closed miss, unexercised — low priority); (3) index
tie-break name-sorted vs PG OID order (plan-shape nit). After S7: S10a balk ERROR
(d1323 `no combine rule for aggregate "balk"`, parallel_agg_combine.go:145 — bounded
3-line correctness fix + test re-pin), then S4 op-preserve Index Cond, S8/S9 cost-model.

**Gates run (this loop):** `go test ./internal/planner/ ./internal/executor/` PASS;
`scripts/pg-regress-runner.sh aggregates` 1474→1390; `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=35); `make plan-diff` SKIP (no goopg on 65433); pre-commit pgbench smoke on
commit (0 failed). Behavioral probe (CASE A/B/C) confirmed partition guard + p_t1 prune.

**Delegation:** researcher `0134-0001-s6-next-slice-inventory` DONE (recommended S7
single-rel prune). implementer `0134-0001-s7-groupby-pruning` DONE (2 rounds: initial +
reviewer finding-1 fix). reviewer `0134-0001-s7-groupby-pruning` DONE (REQUEST-CHANGES
→ finding 1 fixed; deviation CONFIRMED correct). implementer
`0134-0001-s6-s3f-scalar-subquery-render` DONE but REVERTED (d-render net-negative).

**In-flight:** none.
