(idle — nothing in flight)

Loop #35 landed + committed + pushed: `CREATE OPERATOR CLASS` now populates
a real `pg_opclass` row (M0119-0004, DU-002 slice 409). Bounded to
upstream's own `op_class_empty` `002_pg_dump.pl` fixture (`FOR TYPE bigint
USING btree FAMILY dump_test.op_family AS STORAGE bigint` — a class with a
STORAGE clause but no OPERATOR/FUNCTION members). Before this loop CREATE
OPERATOR CLASS was still the M0097-0027 minimal stub (only tracked FUNCTION
2 hash-extended support func + a bare schema association for DROP SCHEMA
CASCADE text) — `pg_opclass.VirtualRows` was hardcoded nil so pg_dump's
getOpclasses always read 0 rows.

Parser (`internal/parser/ddl.go` `parseCreateOpClassTail`): name now
schema-qualified via `p.parseObjectName()`; DEFAULT and USING-method are
now captured (`CreateOpClassStmt.IsDefault`/`Method`, previously
discarded); new optional `FAMILY family_name` clause (FamilySchema/
FamilyName) recognized right after the method; new `STORAGE type` AS-list
entry (StorageType) alongside the pre-existing OPERATOR/FUNCTION
recognition (still accepted-and-discarded beyond the FUNCTION-2 hash-func
capture).
Catalog (`internal/catalog/catalog.go`): new `UserOperatorClass`
(OID/Name/NamespaceOID/Owner/Method/FamilyOID/InTypeOID/IsDefault/
KeyTypeOID) + `RegisterUserOperatorClass`/`DropUserOperatorClass`/
`ListUserOperatorClasses`, keyed `"<schema>.<name>/<method-oid>"` mirroring
`userOpFamilyKey`; new `LookupUserOperatorFamily` resolves an explicit
FAMILY clause. `pg_opclass.VirtualRows` now renders the registry.
Executor (`internal/executor/operators_ddl.go` `execCreateOpClass`):
resolves method (42704 if unrecognized)/namespace/intype/storage-type OIDs;
explicit FAMILY must already exist (42704 if not, mirrors
opfamilycmds.c); omitted FAMILY auto-creates an anonymous family sharing
the class's schema+name (PG's DefineOpClass — opcfamily is NOT NULL),
reusing the idempotent RegisterUserOperatorFamily. DROP OPERATOR CLASS now
also calls DropUserOperatorClass (best-effort cleanup).

Verified byte-identical vs a freshly-built, live PG 18.3 instance
(postgres/local_install) started manually this loop: `CREATE OPERATOR CLASS
public.op_class_empty\n    FOR TYPE bigint USING btree FAMILY
public.op_family AS\n    STORAGE bigint;` plus the generic-archiver `ALTER
OPERATOR CLASS ... OWNER TO ...;` line (no goopg-side code needed for that
— derived from pg_dump's own generic owner-emission machinery reading
opcowner, same mechanism slice 406/408 already depend on).

Gates run this loop: go build ./... clean; go vet parser/catalog/executor/
planner/testport clean; internal/parser+internal/catalog+internal/executor+
internal/planner suites PASS; TestPort_PgDumpConnectionSetup PASS (slice
409 assertion); gofmt -l flags only the same pre-existing go1.25/1.26-drift
files as loop #34 (verified via git stash); TPC-H spotcheck Q12=2/Q13=33
PASS; pgbench smoke = pre-commit hook (ran + PASS, ~224-14000 tps across
the 3 pgbench transaction types); make ralph-state-guard auto-repaired the
same recurring stale-progress-marker pattern as every prior loop. Design
doc 0119-0004-create-operator-roundtrip.md ("Loop #35" section) + README
index updated in this commit. New deferral ledger row (status `-`, open):
OPERATOR/FUNCTION class members still not tied to a pg_amop/pg_amproc
member store — the SAME underlying gap as the still-open loop #34 "ALTER
OPERATOR FAMILY ... ADD" resume point, since dumpOpclass/dumpOpfamily read
the identical pg_amop/pg_amproc-via-pg_depend shape (now explicitly framed
as one combined follow-up). op_class_custom additionally needs a range-type
subtype_opclass binding. KeyTypeOID==0 -> "-" regtype rendering is
unverified (no fixture omits STORAGE yet).

Next candidates (backlog):
(1) Combined pg_amop/pg_amproc + synthetic pg_depend member-store feature
— needed by BOTH `ALTER OPERATOR FAMILY ... ADD` (loop #34 resume (a)) and
CREATE OPERATOR CLASS's own OPERATOR/FUNCTION AS-list entries (this loop's
deferral); keyed by family OID (pg_amop/pg_amproc attach to the FAMILY not
the class per PG semantics), with pg_depend rows distinguishing
refclassid=pg_opclass (attributed to the class's own CREATE statement) vs
refclassid=pg_opfamily (attributed to a later ALTER FAMILY ADD) — this
class/family attribution split is the trickiest part (mirrors real
dumpOpclass's own two-query-with-pg_depend-join approach); once landed,
port the `op_class`/`op_class_custom`/`op_class_empty`(already done)
fixtures; op_class_custom additionally needs a range-type subtype_opclass
binding (CREATE TYPE ... AS RANGE (subtype_opclass = ...)); (2) regoper/
regoperator OID->name resolution — still no observable gap (no column
typed regoper yet); (3) regprocedure argument-type-list disambiguation for
overloaded functions; (4) M0119-0005 (pg_waldump server tier), M0119-0006
(pg_amcheck server tier), M0119-0007 (pg_basebackup recvlogical, blocked on
logical decoding); (5) M0119-0002 (CLOG store swap Part B) still open —
flagged highest blast radius, needs a dedicated full-gate session; (6)
datacl (pg_database ACL) stays permanently deferred (untestable under the
connsetup harness).
