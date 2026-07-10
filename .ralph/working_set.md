(idle — nothing in flight)

## Loop summary (2026-07-10, loop #14)

**Outcome: fixed a sibling-path divergence in the live `pg_index` renderer —
`indexprs`/`indpred`/`indcoloptions` were hardcoded to `""` (a non-NULL empty
string for a `text` column) instead of the `VirtualNull` sentinel / the
partial-index predicate text. Real feature/bug fix, gated, committed.**

- Nightly triage: sole action item AI-20260710-011513-001 (build failure)
  already had a closed M-NIGHTLY task (fix_plan line 6123, `[x]`) and
  `go build ./...` was clean at loop start — no new M-NIGHTLY work.
- Picked working-set candidate: unimplemented_feat #135 (pg_get_expr). Found
  pg_get_expr's pass-through is architecturally CORRECT for goopg (all
  populated pg_node_tree columns — adbin/conbin/relpartbound — store
  pre-formatted deparsed SQL text, not serialized node trees). But the live
  renderer `catalog.InMemory.PGIndexRowsForDBOid`
  (`internal/catalog/catalog.go`) hardcoded `indexprs`/`indpred`/
  `indcoloptions` to `""`. For a `text` column, `""` reads as a non-NULL
  empty string (SQL NULL needs `catalog.VirtualNull`), so:
    - `indpred IS NOT NULL` matched EVERY index (canonical partial-index probe).
    - `pg_get_expr(indpred, indrelid)` returned `''` not the WHERE predicate on
      a partial index, and `''` not NULL on a plain one.
  The heap-row twin `buildUserPGIndexRow`
  (`internal/executor/pg18_user_catalog_rows.go`) was ALREADY correct — a
  live-vs-heap divergence for the same query.
- Fix: `indpred` = `idx.PredicateString` when `idx.HasPredicate` else
  `VirtualNull`; `indexprs`/`indcoloptions` = `VirtualNull`; same for the
  synthetic TOAST-index rows. Mirrors the heap twin exactly.
- Tests: `internal/executor/pg_index_indpred_test.go` —
  `TestPgIndexIndpredPartialVsPlain` (E2E through pg_get_expr),
  `TestPgIndexRowsIndprIndexprsNullSentinel` (direct row-cell guard).
- Bookkeeping: `unimplemented_feat.json` #135 code_audit narrowed (surgical
  Edit, JSON validated, no full rewrite); deferral-ledger row appended for the
  one remaining open slice (expression-index `indexprs` never populated from
  `Index.ColExprs`); fix_plan.md entry added; new design doc
  `docs/design/0122-0019-pg-index-node-tree-columns.md` + README index row.
- Gates (foreground, all PASS): `go build ./...`, `go vet` (catalog+executor);
  `go test -count=1 ./internal/catalog/... ./internal/executor/...`;
  `scripts/tpch-spotcheck.sh` (Q12=2/Q13=33);
  `RALPH_PRECOMMIT_SCOPE=smoke bash scripts/ralph-precommit-test.sh` (0 failed,
  all 3 workloads); JSON validity check; `make ralph-state-guard`.

**Next natural work:** the deferred slice — deparse `Index.ColExprs` into
`indexprs` in BOTH `PGIndexRowsForDBOid` and `buildUserPGIndexRow` so
`pg_get_expr(indexprs)` works on expression indexes. OR continue the
`unimplemented_feat.json` survey (#67 pg_get_expr node-tree — now covered;
next candidates unexplored). OR M0122-0007's remaining index/typed-table
TEMPLATE-copy scope (large, multi-loop).

In-flight: none
