# M0146-0085 — a WITH clause may lead MERGE

Status: landed 2026-10-12.
Parent: M0146-0081 (item 2a rank via the M0146-0055 pin).

## Problem

`WITH s AS (SELECT 8 AS a) MERGE INTO t USING s ON t.a = s.a WHEN MATCHED
THEN UPDATE SET a = 80` failed with `syntax error at or near "MERGE"`.
PostgreSQL 18.3 runs it (`MERGE 1`).

gram.y's `MergeStmt` starts with `opt_with_clause`. goopg's `merge_stmt`
rule (goopg\_ext.y, P5.4) did not, and `parser.MergeStmt` had no field to
carry the clause. A parser guard, `TestRoutingModifiersAndRecursive`,
pinned the rejection. Its comment claimed gram.y's MergeStmt had no WITH
clause either, but that was the legacy parser's gap, not PostgreSQL's grammar.

## Design

- **AST.** `MergeStmt.With *WithClause` was added.
- **Grammar.** A second `merge_stmt` alternative:
  `with_clause MERGE opt_INTO_kw base_table_ref USING base_table_ref ON
  a_expr merge_when_list opt_returning`.
  - It is anchored at the MERGE keyword (`$<p>2`), as `with_clause DELETE_P`
    anchors at DELETE (playbook §12.3: an embedded or WITH-led DML statement
    carries its keyword's offset).
  - `make gen-parser` reports the known 60 shift/reduce conflicts and no new
    ones.
- **Planner.** `planMerge` runs `preplanWithClause(s.With, …)` before
  resolving the target and source, with the restore deferred, and wraps the
  `Merge` node with `wrapDMLCTEPrefix`, exactly as `planDelete` does.
  - The CTEs are visible to the USING source and to the ON and WHEN clauses,
    including sublinks there.
  - A data-modifying CTE runs first. Its rows are invisible to MERGE's own
    scan of the target, because the statement shares one snapshot.
- **Parser tests.**
  - The guard now pins acceptance (`assertParity`).
  - `TestParseWithMerge` checks the CTE and the MERGE-keyword position.
  - The goldens diff is the new `With=∅` field on the five MERGE records,
    plus that one record flipping from a syntax error to an AST.

## Verification

- **Probe against PG 18.3** (`tmp/m0085/probe.sql`): a CTE source, a CTE
  sublink in ON, a data-modifying CTE source, and WITH … MERGE … RETURNING
  merge\_action(). It is byte-identical to PG.
- **`TestWithMerge`** (postmaster) fails on HEAD with a syntax error at
  MERGE. Its data-modifying-CTE arm's expectation (`100:cte` and
  `100:inserted`) was confirmed on PG.
- **Regress.** `merge` 1586 → 1572 lines, `with` 2997 → 2969,
  `triggers` 2471 → 2462. `updatable_views` and `returning` are identical.
- **Gates.** Units (parser goldens included); tpch-spotcheck; the
  acceptance arm (values identical); sf025 PASS=99; the TPC-DS fire-set gate
  (no plan changed).

## Residuals

None found by the probe. `WITH RECURSIVE … MERGE` goes through the same
`with_clause` and is not separately exercised.
