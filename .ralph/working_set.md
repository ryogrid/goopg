(idle — nothing in flight)

Last loop (#47): M0119-0004 **ALTER TABLE … {ENABLE|DISABLE} [REPLICA|ALWAYS]
RULE round-trip in pg_dump** (DU-002 slice 325) — LANDED, committed.

pg_dump's dumpRule emits a separate `ALTER TABLE <t> {ENABLE ALWAYS|ENABLE
REPLICA|DISABLE} RULE <name>;` for any rule whose pg_rewrite.ev_enabled != 'O'.
Slice 324 round-tripped DO-NOTHING CREATE RULE but hard-coded ev_enabled='O' and
consumed `ALTER TABLE … RULE` through the generic ENABLE/DISABLE no-op arm, so a
disabled/replica/always rule restored as plain-enabled. Fix (dump-fidelity only —
goopg implements no query rewrite):
- ast.go/ddl.go: new AlterTableEnableDisableRule kind + RuleName/RuleEnabledState
  fields; token-value lookahead branch BEFORE the generic no-op arm maps
  ENABLE→'O', DISABLE→'D', ENABLE REPLICA→'R', ENABLE ALWAYS→'A'. DISABLE TRIGGER
  still falls through (no RULE lookahead match).
- catalog.go: RuleInfo.Enabled byte + EvEnabled() (zero→'O'); pg_rewrite.VirtualRows
  emits string(r.EvEnabled()). No heap sync — pg_rewrite is fully virtual.
- operators_ddl.go: new action case sets tbl.Rules[i].Enabled; unknown rule → 42704.

Files: internal/parser/{ast.go,ddl.go,alter_test.go},
internal/catalog/catalog.go, internal/executor/{operators_ddl.go,storage_ddl_test.go},
internal/testport/pgdump_connsetup_test.go (slice 325 fixture+assert),
docs/design/0119-0004-alter-rule-enable-disable.md (+README 0119-0004ab).

Gates: parser/catalog/executor suites PASS; TestPort_PgDumpConnectionSetup PASS
(real pg_dump 18.3 — DISABLE RULE / ENABLE ALWAYS RULE byte-identical, origin rule
emits no clause); go build clean; pgbench smoke via pre-commit hook.

NEXT loop — remaining M0119-0004 pg_dump getter-battery gaps. The next big enabler
is a **per-role OID registry** (roles map[string]struct{} → name→OID, surface in
pg_roles VirtualRows with OIDs), which unblocks BOTH named-role CREATE POLICY (TO
role) AND GRANT/ACL (relacl + dumpACL). BUT both also need an ARRAY(SELECT…) /
array_to_string / quote_ident / ANY(oid[]) query stack that goopg does NOT have
(confirmed grep: zero matches) — getPolicies' named-role ELSE branch and dumpACL
both depend on it. So either (a) build that query stack first (Effort-L planner/
executor work), or (b) pick another contained getter gap. Conditional/action
CREATE RULE needs the query reverse-compiler (Effort-L too).
