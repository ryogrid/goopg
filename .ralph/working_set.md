Task: M0110-0003 (AC-002 pg_amcheck 002_nonesuch promotion) — loop #22. Landed
AC-002 gap #5 (schema-qualified SRF in FROM clause). A NEW gap #6 surfaced and
was isolated — that is the next loop.

=== WHAT LANDED (this loop) ===
Worktree `.claude/worktrees/m0110-amcheck-sql` branch `m0110-0003-amcheck-sql-surface`,
commit d8d03c7b (on top of b542aeba, off clean HEAD b8dd6403). Parser fix:
- gap #5: a schema-qualified function call in FROM (`"public".verify_heapam(...)`)
  was rejected. parseRangeVar only dispatched an SRF when the name was unqualified
  or pg_catalog-qualified (`obj.Schema=="" || EqualFold(obj.Schema,"pg_catalog")`);
  a user-schema qualifier (public) fell through to the derived-subquery branch →
  `expected ')' after subquery in FROM (got ()`. Fix: restructure the gate so in
  FROM-clause context a schema-qualified `name(args)` is also accepted — schema
  qualifier discarded, dispatch by bare name (builtins by lowercased canonical
  name so executor's name switch matches; else user-defined SRF). Unqualified /
  pg_catalog path preserved exactly. (internal/parser/select.go ~line 1257)
Regression: parser.TestParseSchemaQualifiedFromSRF (quoted + bare schema forms),
sibling to TestParseNamedArgColonEqualFromSRF.
Updated the 002 test header + flipped the gap-#5 preflight probe to a gap-#6 probe;
design 0110-0008 + deferral ledger updated. Also gofmt'd 3 pre-existing
violations in select.go (HEAD was already gofmt-dirty there).

Files (all in worktree): internal/parser/select.go,
internal/parser/named_arg_colon_equal_test.go (new test),
internal/testport/pgamcheck002_port_test.go (header + gap-#6 probe),
docs/design/0110-0008-amcheck-sql-surface-plan.md.

Key symbols: parser.parseRangeVar (srfFuncName gate, isKnownBuiltin switch);
analyzer.resolveTable + planner LATERAL path (next loop's targets).

Gates run: go test ./internal/{parser,analyzer,planner,executor,server} PASS;
TestPort_PgAmcheck002Nonesuch now SKIPs cleanly on gap #6 (was FAIL); go build PASS;
gofmt/vet clean. TPC-H spotcheck SKIP (worktree no data dir; parser-only,
row-count-neutral).

=== NEXT STEP (resume point) — AC-002 gap #6, its OWN bounded loop ===
pg_amcheck builds each per-relation heap check as an implicit-LATERAL comma-join:
  ... FROM pg_catalog.pg_class c, "public".verify_heapam(relation := c.oid, …) v
The query now PARSES and reaches the executor (gap #5 fixed), but the correlated
`c.oid` inside the SRF's ARGUMENT list does not resolve against the sibling
`pg_class c` range-table entry, so verify_heapam errors `column "oid" does not
exist`. Fix = LATERAL/correlated-reference resolution for a FROM-clause SRF whose
args reference an outer/sibling relation (planner + executor, plan/exec time).
CAUTION: a prior full LATERAL/Q9-rebind attempt HUNG (M0072-0002) — BOUND the
change and verify incrementally. Continue in the SAME worktree branch off its tip
(d8d03c7b). When it lands, 002_nonesuch's gap-#6 preflight probe clears → run
TestPort_PgAmcheck002Nonesuch; expect the NEXT gap (clog XidStatusFunc wiring for
the clog-dependent verify_heapam tier, then AC-002..AC-005 CSV flip).

=== CONTEXT (unchanged) ===
Foreign gen-column WIP in the MAIN tree — do NOT commit engine code in the MAIN
tree. ALL new engine work lands in worktrees off clean HEAD b8dd6403
(worktree_isolation_escapes_foreign_wip_block). Merge of the worktree chains
(M0110 amcheck-sql now at d8d03c7b + amcheck-pagedel + M0117 0001->0008) awaits a
HUMAN clearing the foreign WIP. CAUTION: .ralph/fix_plan.md is churned by the
driver (md5 changes mid-loop; line numbers shift) — fix_plan progress for this
loop is recorded in the deferral ledger + this working_set instead.
