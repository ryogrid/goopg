(idle — nothing in flight)

Last loop (#45): M0119-0004 **CREATE POLICY round-trip in pg_dump** (DU-002
slice 323) — LANDED, committed.

The per-policy half of RLS (the relrowsecurity ENABLE flag landed slice 322).
pg_dump's getPolicies reads pg_policy and dumpPolicy re-emits CREATE POLICY.
goopg had NO CREATE POLICY (parse error) + an empty pg_policy stub, so a policy
was silently lost on dump. Feasible because slice 322's rls_t already proves
getPolicies executes (0 rows) and a PUBLIC policy (polroles='{0}') short-circuits
the lazy CASE before the risky pg_roles ARRAY subquery; pg_get_expr is a
pass-through.

Fix (dump-fidelity only — goopg enforces NO RLS):
- ast.go: CreatePolicyStmt / DropPolicyStmt nodes.
- ddl.go: parseCreatePolicyTail (USING/WITH CHECK via general parseExpr →
  proper AST → idempotent re-dump) + parseDropPolicyTail; dispatch in
  parseCreate/parseDrop.
- catalog.go: PolicyInfo struct + Table.Policies; formatExprForAttrdef ColumnRef
  case (was missing → Go pointer string); pg_policy polqual/polwithcheck retyped
  text→pg_node_tree (empty cell → SQL NULL); VirtualRows projects Table.Policies
  (fully-parenthesized polqual/polwithcheck → USING ((a > 0))).
- operators_ddl.go: execCreatePolicy (dup→42710, named roles→0A000, OID via
  AllocOID, no heap sync — pg_policy virtual) + execDropPolicy; dispatch.
- planner.go: CreatePolicyStmt/DropPolicyStmt added to DDL passthrough.

Files: internal/parser/{ast.go,ddl.go,policy_test.go},
internal/catalog/catalog.go, internal/executor/{operators_ddl.go,
storage_ddl_test.go}, internal/planner/planner.go,
internal/testport/pgdump_connsetup_test.go (slice 323 fixture+assert),
docs/design/0119-0004-create-policy-rls.md (+README 0119-0004z).

Gates: parser/catalog/planner/executor suites PASS; TestPort_PgDumpConnectionSetup
PASS (real pg_dump 18.3, all 3 CREATE POLICY forms byte-identical); go build clean.

DEFERRED (ledgered): named-role `TO role` policies (no per-role OID registry;
the pg_roles ARRAY-subquery path is unverified — PUBLIC short-circuits it).

NEXT loop — remaining M0119-0004 pg_dump getter-battery gaps: GRANT/ACL (relacl +
dumpACL — needs real ACL storage; GRANT is a CompatNoopStmt today), CREATE RULE
(pg_rewrite + pg_get_ruledef — full no-op today), or named-role policies (needs a
role-OID subsystem). Pick one and scope a contained first slice.
