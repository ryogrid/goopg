(idle — nothing in flight)

Last landed: DU-002 slice 261 (loop #28) — a dedicated MINVALUE/MAXVALUE keyword
AST node (`PartitionRangeBoundKeyword`) disambiguates an unbounded RANGE-partition
edge from a quoted text literal `'MINVALUE'`. Picked up the previous loop's
usage-limit-interrupted WIP (was uncommitted in catalog.go/operators_ddl*.go/
ddl.go/expr.go), gofmt-cleaned it, added the missing tests, doc, fix_plan, landed.

Mechanism:
- AST (expr.go): PartitionRangeBoundKeyword{pos, IsMax} + Keyword(); distinct from
  StringConst so bare MINVALUE/MAXVALUE ≠ quoted 'MINVALUE'.
- Parser (parsePartitionBoundValues, ddl.go): bare keyword → new node; quoted → StringConst.
- Catalog (catalog.go): parallel []bool flags From/ToUnbounded[Max] on PartitionBound;
  compareKeyToRangeBound (replaces compareRangeBoundStr) treats KEY as concrete always,
  reads flags for the bound edge; boundElemUnbounded[Max] fall back to legacy string
  sentinel for pre-slice-261 bounds. rangeStrTupleGE/LT take the flag slices.
- Executor (operators_ddl.go / operators_ddl_partition.go): exprToString /
  boundExprToSQLLiteral / rangeBoundExprToSQLLiteral handle the new node;
  rangeBoundUnboundedFlags populates flags in execCreatePartitionChild + execAlterTable.
- No pg_dump-side change (FromValueLiterals still uppercase keyword → same dump).

Files: internal/parser/expr.go, internal/parser/ddl.go, internal/catalog/catalog.go,
internal/executor/operators_ddl.go, internal/executor/operators_ddl_partition.go,
internal/parser/partition_range_bound_keyword_test.go (new),
internal/catalog/catalog_test.go (TestCompareKeyToRangeBoundDisambiguation),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 261), .ralph/fix_plan.md.

Gates: gofmt clean; go build ./... clean; parser+catalog+executor suites PASS;
TestPort_PgDumpConnectionSetup PASS (3.7s, prange_am MINVALUE fixture still byte-matches);
pgbench pre-commit smoke runs on commit.

Next (slice 262+): multi-level / INHERITS partition-tree dump fidelity, or per-element
open-edge multi-column RANGE bound routing (FROM (a, MINVALUE) TO (b, MAXVALUE)).
