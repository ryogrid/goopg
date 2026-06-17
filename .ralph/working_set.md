(idle — nothing in flight)

Last landed: DU-002 slice 168 (loop #135) — LIST + HASH partition bounds now
round-trip through pg_dump. REAL DIVERGENCE FIXED.

Root cause: a partition's bound values are stored via exprToString (the RAW
unquoted form — 'a'→a), which is correct/required for value routing
(FindPartitionForValue compares row keys against pb.InValues verbatim). But
FormatPartitionBound reused those raw strings for relpartbound, so a TEXT LIST
partition dumped the restore-breaking `FOR VALUES IN (a, b)` instead of
`FOR VALUES IN ('a', 'b')`. The raw strings can't be re-quoted at format time
(catalog no longer knows the column type: is "1" int 1 or text '1'?).

Fix (catalog-metadata + capture-at-creation, zero routing risk):
PartitionBound gains a parallel InValueLiterals []string holding the SQL-literal
rendering, captured at partition-creation time from the bound's parser.Expr via
the existing boundExprToSQLLiteral (quotes/escapes strings, passes ints through).
Both LIST creation sites populate it (execCreatePartitionChild + ATTACH PARTITION
path). FormatPartitionBound prefers InValueLiterals, falls back to InValues when
absent (int keys render the same) → fixes both sibling consumers
(buildUserPGClassRow heap row + catalog.go VirtualRows) at once. HASH already
correct; locked by fixture.

Files: internal/catalog/catalog.go (InValueLiterals field + FormatPartitionBound
+ TestFormatPartitionBoundListLiterals), internal/executor/operators_ddl.go
(2 sites populate InValueLiterals), internal/testport/pgdump_connsetup_test.go
(LIST plist/plist_ab + HASH phash/phash_0 fixtures + quoted-bound assertions),
docs/design/0110-0001-pg-dump-tap-port.md (slice 168), .ralph/fix_plan.md (#135),
.ralph/deferral_ledger.md (RANGE-on-text follow-up).
Gates: gofmt OK; go build ./internal/... OK; go vet ./internal/testport/ clean;
TestFormatPartitionBoundListLiterals PASS; TestPort_PgDumpConnectionSetup PASS
(2.78s, not skipped); catalog + full executor suites PASS; pgbench pre-commit
smoke on commit.

Next (slice 169): RANGE-on-text bounds have the SAME raw-vs-literal bug
(FromValues/ToValues stored unquoted via exprToString) — a FOR VALUES FROM ('a')
TO ('m') partition dumps invalid FROM (a) TO (m). Add FromValueLiterals/
ToValueLiterals captured from poc.FromValues/ToValues parser.Exprs, render in
FormatPartitionBound's RANGE branch, add a text-keyed RANGE fixture. See ledger.
Other directions: table inheritance (INHERITS), multi-level partition trees,
column-level STORAGE/COMPRESSION (needs parser keywords).
