(idle — nothing in flight)

Loop #36 landed + committed: `::regprocedure` (and a directly
regprocedure-typed column) now renders PG's real
`format_procedure`/`regprocedureout` shape `name(argtype1,argtype2)`
instead of the bare function name `regproc` shares (M0119-0004, DU-002
slice 410).

Context: while scoping loop #34/#35's deferred `pg_amop`/`pg_amproc` +
`pg_depend` member-store follow-up (needed for `ALTER OPERATOR FAMILY ...
ADD` and CREATE OPERATOR CLASS's OPERATOR/FUNCTION AS-list entries), found
that `dumpOpclass`/`dumpOpfamily` (pg_dump.c) cast the function-OID column
specifically via `::pg_catalog.regprocedure`, and goopg's regprocedure
handling was previously IDENTICAL to regproc (bare name, no arg-type
list) — a real, standalone bug independent of the member store, and a
hard blocker for it (building the member store first would have rendered
every FUNCTION entry's dumped signature wrong). Fixed this prerequisite
first, standalone.

Files: `cmd/gen-pg-proc-data/main.go` (new `parseArgTypeNames` +
`emitNamesOnly` extension emitting `pgProcArgTypeNamesByOID`);
`internal/catalog/pg_proc_names_generated.go` (regenerated, gofmt -w'd —
whole-file regen, not hand-edited unrelated code); `internal/catalog/catalog.go`
(new `RegprocedureName`/`pgArgTypeDisplayAlias`/`formatProcedureSignature`,
next to `RegprocName`); `internal/executor/expr.go` (CastExpr regprocedure
branch now calls `RegprocedureName`, regproc unchanged);
`internal/server/dispatch.go` (`appendTypedCellText`'s combined
regproc/regprocedure case split in two, same fix). Tests:
`TestRegprocedureName` (catalog, new); `TestRegprocOIDCastResolvesName`
(executor, updated expectation) + new user-function regprocedure case;
`TestAppendTypedCellTextRegprocRendersName` (server, updated expectation).

Key symbols: `catalog.RegprocedureName(oid, routines)`,
`catalog.pgArgTypeDisplayAlias`, generator's `pgProcArgTypeNamesByOID`.

Gates run this loop: go build ./... clean; go vet catalog/executor/server/
parser/initdb/planner/cmd clean; internal/catalog+internal/executor+
internal/server+internal/parser+internal/initdb+internal/planner suites
PASS; TestPort_PgDumpConnectionSetup PASS (zero pg_dump regression);
gofmt -l flags only the same pre-existing go1.25/1.26-drift files as loop
#35 (verified via git stash) — my new code is gofmt-clean (confirmed via
targeted gofmt -d greps); TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke
= pre-commit hook (runs on `git commit`); make ralph-state-guard
auto-repaired the same recurring stale-progress-marker pattern as every
prior loop. Design docs updated: `0119-0004-regproc-oid-name-resolution.md`
(new "Follow-up" section closing its own "Scope / non-goals" flag) +
`0119-0004-create-operator-roundtrip.md` (loop #36 addendum to "Still open")
+ README index (both rows' one-liners appended). New deferral ledger row
(status `-`, open, slice 410): the member store itself is STILL not
implemented; a SEPARATE, confirmed-larger prerequisite also surfaced —
goopg has NO builtin-operator catalog at all (`pg_operator.VirtualRows`
renders only user-defined operators), blocking `regoper`/`regoperator`
resolution and, transitively, the exact upstream `op_family`/`op_class`
fixtures (which use real builtin cross-type btree operators).

Next candidates (backlog, updated):
(1) The `pg_amop`/`pg_amproc` + synthetic `pg_depend` member-store itself
(loop #34/#35's actual follow-up) — now unblocked on the regprocedure
side; SCOPE TO USER-DEFINED operators/functions first (fully resolvable
today via `LookupUserOperator`/`Routines().LookupByName`), ledger the
builtin-operator-catalog gap separately rather than attempting the exact
upstream fixture (which needs real builtin cross-type operators) in the
same slice. Design sketch already drafted this loop (see conversation):
`AmOpMember`/`AmProcMember` structs keyed by family OID + an attribution
pair (RefClassID=2616 opclass / 2753 opfamily, RefObjID), new
`ALTER OPERATOR FAMILY ... ADD` parser path (`opclass_item_list` shared
with CREATE OPERATOR CLASS's AS-list, both currently parsed-and-discarded
beyond FUNCTION 2), pg_depend rows (classid=2602/2603). (2) Builtin
operator catalog (`pg_operator` rows for builtins, keyed by name+left/right
type for `regoper`/`regoperator` resolution) — large, standalone feature,
prerequisite for byte-exact op_family/op_class fixture porting. (3) M0119-0005
(pg_waldump server tier), M0119-0006 (pg_amcheck server tier), M0119-0007
(pg_basebackup recvlogical, blocked on logical decoding). (4) M0119-0002
(CLOG store swap Part B) still open — flagged highest blast radius, needs
a dedicated full-gate session. (5) datacl (pg_database ACL) stays
permanently deferred (untestable under the connsetup harness).
