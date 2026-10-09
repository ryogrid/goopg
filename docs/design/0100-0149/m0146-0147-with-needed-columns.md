# M0146-0147 — a WITH list no longer makes the needed-column set unknown

Status: done 2026-10-10 (89616dcd9). Parent: M0146-0014b.

## Problem

TPC-DS Q95 differs from PG only in `[scan-type]` at both scales.

- **PG** probes `web_returns_pkey` with an Index Only Scan on the
  `ws_wh ⋈ web_returns` hash join's inner side.
- **goopg** probed it with a plain Index Scan.

goopg's own Q94 has the identical probe without a WITH clause, and there
goopg prints the Index Only Scan.

## Cause

Index-only paths need the set of columns the statement reads
(`neededColumnNames`, `outputColumnNames`, pathindexonlyneed.go), as does
the scan narrowing that goes with them. Both collectors declined any
statement with a WITH clause. `neededColsKnown` was then false, and no
index-only path was offered anywhere in the statement.

## PG behaviour

`check_index_only` (postgres/src/backend/optimizer/path/indxpath.c:2229)
collects the attributes the query uses from the rel's target list and its
restriction clauses. A WITH clause plays no part: each CTE body is its own
query level, planned separately.

## Change

`collectWithBodyColumnNames` walks every CTE body into the set, in needed
mode. Both collectors call it in place of the decline, because they are
twins and must agree (Hard-won rule 2).

- **Over-inclusion is the safe direction.** A body may read an enclosing
  level's columns as outer references, for example a WITH inside an
  EXISTS body reading the EXISTS's outer query. An inlined body is also
  pulled into the referencing scope. Collecting every body's names can
  therefore only keep more columns needed.
- **Still declined:** a data-modifying CTE, and a body the walker cannot
  model (a recursive UNION).

## Verification

- **Tests.**
  - `TestColumnNameCollectorsWalkCTEBodies` checks:
    - a WITH statement's needed set is known and has its own columns;
    - the above-tree set is known too;
    - a CTE inside an EXISTS body contributes the outer column it
      correlates on.
    It fails on HEAD.
  - `TestOutputColumnNamesDeclinesLikeNeeded` now uses a recursive CTE and
    a DML CTE as its still-declining WITH shapes.
- **TPC-DS fire set.** Q95 → MATCH at both scales: SF0.25 matches 55 → 56,
  SF1 41 → 42, scan-type −1 at each. Q2, Q59 and Q78 also fire, but only
  their printed widths change (narrowing now applies). The sweep verifies
  all 99 queries' values.
- **TPC-H.** Plans are byte-identical; the acceptance arm matches on
  values.
- **Regress A/B** (32 cases, `with` included): no change.
- **Gates.** Units, TPC-H spotcheck, SF0.25 sweep (PASS=99) and
  ea-ratchet (1) all PASS.

## Not covered (ledgered)

- **A recursive CTE still makes the set unknown.** The walker declines a
  UNION body. Walking each arm in needed mode would over-include safely.
- **A DML CTE still declines.** RETURNING lists and WHERE clauses are not
  walked.
