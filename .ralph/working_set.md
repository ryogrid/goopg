Task: M0110-0003 (AC-003 pg_amcheck) — loop #9. LANDED blocker #2:
bt_index_check schema-qualified dispatch. Committed on align-data-structure-with-pg.

=== WHAT LANDED (this loop) ===
pg_amcheck calls the amcheck builtins qualified by the *install schema*
(`"public".bt_index_check(index := $1::regclass, heapallindexed := $2, …)`),
NOT pg_catalog. evalFuncCall (internal/executor/expr.go ~L5286) stripped only
the `pg_catalog.` prefix, so `public.bt_index_check` → 42883 "function
public.bt_index_check does not exist". Any healthy table with a dependent index
failed its index check.

Fix: internal/executor/expr.go — after the pg_catalog strip, if the name has a
`.`, match the suffix against {bt_index_check, bt_index_parent_check,
verify_heapam} and strip the schema for those only (same-named user funcs
unaffected). Mirrors the FROM-clause SRF schema-strip for verify_heapam
(parser/select.go gap #5). Scalar parser already accepts `:=` named args (S5).

Files: internal/executor/expr.go (fix),
internal/executor/operators_bt_index_check_test.go (NEW
TestBtIndexCheck_SchemaQualifiedDispatch — positional + `:=` named-arg shapes),
internal/testport/pgamcheck_btree_port_test.go (NEW
TestPort_PgAmcheckBtreeIndexCheck — real pg_amcheck over a healthy indexed user
table checks clean e2e), docs/design/0110-0003 (blocker #2 → FIXED + resume),
docs/test-port CSV+md (AC-003 rationale).

Gates: bt_index_check unit tests PASS; all 4 pg_amcheck port tests PASS
(001/002/004 + new btree); executor+parser+planner suites PASS; build ./... +
gofmt + vet clean; ralph-state-guard OK (self-repair). TPC-H spotcheck SKIPPED
(no data dir; change is amcheck-dispatch-only, zero TPC-H surface).

=== NEXT STEP (resume) — AC-003 remainder, blocker #3 ===
System-catalog heap resolution for the 003_check whole-db pre-corruption clean
run: `verify_heapam` on pg_type/pg_attribute/pg_class reports "could not open
relation" because verifyHeapamResolveTable / LookupTableByOID
(internal/executor/operators_verify_heapam.go) does not resolve catalog
relations to on-disk heap pages. Larger parity effort. After that:
005_opclass_damage = CREATE OPERATOR CLASS + pg_amproc parity (UPDATE pg_amproc
to inject breaking sort-order) — large. AC-003 stays `defer` until 003 and 005.
