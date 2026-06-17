(idle — nothing in flight)

Last landed: DU-002 slice 169 (loop #136) — RANGE partition bounds now
round-trip through pg_dump. REAL DIVERGENCE FIXED (text quoting + MINVALUE
semantic corruption).

Root cause: FormatPartitionBound's RANGE branch rendered the raw
FromValues/ToValues (stored via exprToString, needed for routing), so a TEXT
RANGE bound dumped restore-breaking `FROM (a) TO (m)` not `FROM ('a') TO ('m')`.
Worse: the parser encodes MINVALUE/MAXVALUE as a sentinel
StringConst{Value:"MINVALUE"|"MAXVALUE"}; the generic literal renderer quoted it
('MINVALUE') → restores as a TEXT bound, not an unbounded edge (silent semantic
corruption, not just invalid SQL).

Fix (same shape as slice 168, zero routing risk): PartitionBound gains parallel
From/ToValueLiterals []string captured at creation by rangeBoundLiterals. The
per-element rangeBoundExprToSQLLiteral delegates to boundExprToSQLLiteral for
constants (quotes strings, passes ints) but emits the BARE keyword for the
MINVALUE/MAXVALUE sentinel StringConsts. Helper returns nil for the whole tuple
if any element can't render, so FormatPartitionBound falls back to raw
FromValues/ToValues. Both RANGE creation sites populate it (execCreatePartitionChild
+ ATTACH path). Routing untouched (still compares raw FromValues/ToValues).

Files: internal/catalog/catalog.go (From/ToValueLiterals fields +
FormatPartitionBound RANGE branch + RANGE test cases in catalog_test.go),
internal/executor/operators_ddl_partition.go (rangeBoundExprToSQLLiteral +
rangeBoundLiterals), internal/executor/operators_ddl.go (2 sites),
internal/testport/pgdump_connsetup_test.go (prange/prange_am FROM (MINVALUE) TO
('m') fixture + assertions), docs/design/0110-0001-pg-dump-tap-port.md (slice
169), .ralph/fix_plan.md (#136), .ralph/deferral_ledger.md (keyword-node + INHERITS).
Gates: gofmt OK; go build ./internal/... OK; go vet ./internal/testport/ clean;
TestFormatPartitionBoundListLiterals PASS; TestPort_PgDumpConnectionSetup PASS
(2.64s, not skipped); catalog + full executor suites PASS; pgbench pre-commit
smoke on commit.

Next (slice 170 candidates): (1) dedicated MINVALUE/MAXVALUE keyword AST node —
parser collapses keyword `MINVALUE` and literal `'MINVALUE'` to the same
StringConst, affecting routing too (latent correctness, pathologically rare).
(2) table inheritance (INHERITS) dump fidelity. (3) multi-level partition trees.
(4) column-level STORAGE/COMPRESSION (needs parser keywords). See ledger.
