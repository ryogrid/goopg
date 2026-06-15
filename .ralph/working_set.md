Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 2 (acldefault())
COMMITTED + pushed this loop (5cea8880). Next: slice 3 = tableoid output-column
naming bug (planner). NOTHING in flight; next loop starts clean on slice 3.

=== DONE (loop #25) ===
Committed + pushed the already-verified slice-2 work from loop #24:
acldefault() executor builtin (evalAclDefault + aclPrivString + aclRoleNameForOID
in internal/executor/expr.go) so pg_dump's getNamespaces query runs. Commit
5cea8880. Build/vet/gofmt clean; TestEvalAclDefault PASS.

=== NEXT STEP — DU-002 slice 3 (planner output-column naming) ===
Symptom: pg_dump SEGFAULTs (exit 139) in "reading schemas". getNamespaces'
first projected column `n.tableoid` returns in RowDescription labelled
`?column?` instead of `tableoid`. Value is CORRECT (2615); only the field NAME
is wrong. Reproduces on EVERY table: `SELECT tableoid FROM public.foo` also
labels it `?column?`. pg_dump's PQfnumber(res,"tableoid") -> -1 ->
PQgetvalue(...,-1) out of range 0..5 -> SIGSEGV.

Investigation so far (loop #25):
- The name-derivation twins BOTH return x.Column for a *parser.ColumnRef:
  - executor: deriveTargetName  (internal/executor/operators_ddl.go:2292)
  - analyzer: deriveAnalyzerTargetName (internal/analyzer/analyzer.go:2119)
  So if `tableoid` flowed through the normal ColumnRef-target naming path the
  label WOULD be "tableoid". It isn't -> the system column tableoid is NOT
  taking the plain-ColumnRef projection-naming path.
- Hypothesis: system columns (tableoid/ctid/xmin/xmax/cmin/cmax/oid) are
  injected into the projection/output schema WITHOUT a Name, or resolved as a
  non-ColumnRef node, so naming falls back to ?column?. Files touching tableoid
  system-column handling: internal/executor/operators_storage.go,
  internal/planner/planner.go + plan.go, internal/catalog/catalog.go.
  Existing test: internal/server/tableoid_test.go (value path) — extend it to
  assert the RowDescription field NAME, then fix the schema Name for system cols.
- SIBLING-PATH WARNING: fix executor schema naming AND analyzer/planner schema
  naming together (twin functions above); a unit test on one passes while the
  other is wrong.
- GATE: this is a planner/executor change -> run scripts/tpch-spotcheck.sh
  (Q12=2/Q13=35) before committing.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
