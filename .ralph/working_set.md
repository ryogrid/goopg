(idle — nothing in flight)

Loop #34 landed + committed: `CREATE OPERATOR FAMILY name USING method`
(M0119-0004, DU-002 slice 408) — a new object family. Before this loop
CREATE OPERATOR FAMILY had NO parse path at all (only `ALTER OPERATOR
FAMILY ... OWNER TO` fell into parseAlter's generic compat-stub no-op), so
`pg_opfamily.VirtualRows` was hardcoded `nil` and pg_dump's `getOpfamilies`
always read 0 rows. Matches upstream's own bare `002_pg_dump.pl op_family`
fixture — PG's grammar has no AS clause here (unlike CREATE OPERATOR
CLASS); the family starts empty, members added later via a separate ALTER
OPERATOR FAMILY ... ADD (not implemented, deferred).

Parser: new `parseCreateOpFamilyTail` (internal/parser/ddl.go), branch
checked right after the existing CLASS check inside the "operator" case
(before falling into the bare CREATE OPERATOR symbol-name parse). Parses
`[schema.]name USING method` via p.parseObjectName(), stashes method on new
`CompatNoopStmt.OpFamilyMethod` (ast.go).
Catalog: new `UserOperatorFamily` (OID/Name/NamespaceOID/Method/Owner,
OwnerOrDefault/NamespaceOIDOrDefault mirroring UserOperator) +
`RegisterUserOperatorFamily`/`DropUserOperatorFamily`/
`ListUserOperatorFamilies`, keyed `"<schema>.<name>/<method-oid>"` (PG
scopes opfamily uniqueness per namespace+access-method). New package-level
`AccessMethodOIDByName` (btree/hash/gist/gin/spgist/brin/heap → pg_am.oid)
mirrors the existing LanguageNameToOID pattern. pg_opfamily.VirtualRows now
renders ListUserOperatorFamilies() instead of hardcoded nil.
Executor: new `case "operator family":` in execCompatNoop
(operators_ddl.go) resolves method via AccessMethodOIDByName (42704 if
unrecognized, mirroring get_index_am_oid) + namespace, then calls
RegisterUserOperatorFamily. No planner change needed (CompatNoopStmt
already DDL-passthrough from CREATE OPERATOR's own landing).

Verified byte-identical vs real pg_dump 18.3: `CREATE OPERATOR FAMILY
public.op_family USING btree;` plus the generic-archiver `ALTER OPERATOR
FAMILY public.op_family USING btree OWNER TO ...;` line, with an explicit
negative assertion that no spurious `ALTER OPERATOR FAMILY ... ADD` line
appears (goopg registers no pg_amop/pg_amproc/pg_depend rows for the
family, so dumpOpfamily's loose-member queries correctly return 0 rows).

Gates run this loop: go build ./... clean; go vet
parser/catalog/executor/planner clean; internal/parser+internal/catalog+
internal/executor+internal/planner suites PASS; TestPort_PgDumpConnectionSetup
PASS (DU-002 slice 408 assertions); gofmt -l flags only the same
pre-existing go1.25/1.26-drift files as loop #33 (verified via git stash);
TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook; make
ralph-state-guard auto-repaired the same recurring stale-progress-marker
pattern as every prior loop. Design doc
0119-0004-create-operator-roundtrip.md ("Loop #34" section) + README index
updated in this commit. New deferral ledger row (status `-`, open): ALTER
OPERATOR FAMILY ... ADD (loose OPERATOR/FUNCTION members) not implemented;
full CREATE OPERATOR CLASS round-trip (still the pre-existing M0097-0027
minimal stub — does not populate pg_opclass at all, parser only recognizes
OPERATOR/FUNCTION entries in the AS list, not e.g. bare STORAGE) + the
op_class_custom ordering fixture (range-type subtype_opclass binding)
remain a separate, larger follow-up.

Next candidates (backlog):
(1) ALTER OPERATOR FAMILY ... ADD (OPERATOR n op [, FUNCTION n
func(args)], ...) — needs a catalog-level opfamily-member store + synthetic
pg_depend/pg_amop/pg_amproc rows so dumpOpfamily's loose-member queries
pick it up; (2) full CREATE OPERATOR CLASS round-trip — extend
execCreateOpClass/parseCreateOpClassTail to populate pg_opclass for real
(opcmethod/opcname/opcnamespace/opcowner/opcfamily/opcintype/opcdefault/
opckeytype) + STORAGE-entry parsing, then port op_class/op_class_custom/
op_class_empty fixtures (op_class_custom additionally needs a range-type
subtype_opclass reference); (3) regoper/regoperator OID->name resolution —
still no observable gap (no column typed regoper yet); (4) regprocedure
argument-type-list disambiguation for overloaded functions; (5) M0119-0005
(pg_waldump server tier), M0119-0006 (pg_amcheck server tier), M0119-0007
(pg_basebackup recvlogical, blocked on logical decoding); (6) M0119-0002
(CLOG store swap Part B) still open; (7) datacl (pg_database ACL) stays
permanently deferred (untestable under the connsetup harness).
