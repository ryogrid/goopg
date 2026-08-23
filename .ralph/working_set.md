(idle — nothing in flight)

Task just completed: M0134-0089 (alter_operator.sql). Sized live against the
PG 18.3 oracle (scripts/pg-regress-runner.sh --verbose alter_operator, plus
manual throwaway-server probing via /tmp/zzop-goopg on port 5533). Landed two
real engine-wide fixes, both confirmed live:
(1) catalog.InMemory.PGDependRowsForDBOid (internal/catalog/catalog.go) had
NO code path for c.userOperators at all — every CREATE OPERATOR reported
ZERO pg_depend rows. Fixed by adding the namespace/left-type/right-type/
result-type/oprcode/oprrest/oprjoin dependency loop PG's
makeOperatorDependencies (pg_operator.c:853-937) performs, including its
pinned-object skip (IsPinnedObject: OID<12000 pinned EXCEPT the public
namespace). New catalog.InMemory.LookupUserOperatorByNameAndTypeOIDs added.
(2) The CastExpr regoperator branch (internal/executor/expr.go) only ever
handled a KindInt (OID) input; a KindString (name) input like
'===(bool,bool)'::regoperator fell through UNCHANGED as raw text, so every
WHERE objid = '...'::regoperator comparison against an oid column silently
evaluated false, independent of bug (1). Fixed with a new
regoperatorNameAndArgs parser (internal/executor/reg_identifier.go) mirroring
the regclass CastExpr arm's existing string->OID pattern (returns a bare
NewIntDatum, not a rendered name).
Landed internal/executor/create_operator_depend_test.go
(TestCreateOperatorPopulatesPGDepend, TestRegoperatorCastResolvesToOID).
Committed decd8e3e and pushed. Tests: full units gate PASSING, pgbench smoke
PASS (pre-commit hook). Ledger row appended 2026-08-24 M0134-0089. CSV row
flipped not-tried -> failed (genuinely failing, not stale) via
make regen-testport. fix_plan.md M0134-0089 marked PARKED.

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0090 (amutils.sql,
status `not-tried`)**. Size it live against ./postgres oracle via
scripts/pg-regress-runner.sh --verbose amutils (background, generous
timeout — setup alone takes ~2-3 min; clear tmp/regress-goopg-data first if a
prior run left it non-empty, `rm -rf tmp/regress-goopg-data` — the runner
errors "Directory not empty" on a stale leftover from an interrupted run).
CAUTION carried forward from M0134-0086: watch `ps -o rss` on the goopg PID
while any regress file runs (some cases drove RSS to 20+ GB in <2 min); kill
-KILL promptly (never bare pkill -f) if RSS climbs unbounded before deciding
whether it's worth fixing first. Deferred buckets left in M0134-0089 for a
future resume (see ledger for full detail): (A) pg_describe_object is
ENTIRELY UNIMPLEMENTED (zero non-test hits in internal/executor/*.go) — this
is now alter_operator.sql's immediate next blocker and is a real multi-day
own-milestone feature (PG's version dispatches over ~40 catalog classes,
postgres/src/backend/utils/adt/objectaddress.c); (B) several builtin
selectivity-estimator functions (contsel, contjoinsel, _int_contsel,
_int_contjoinsel) aren't in goopg's curated builtin-proc set; (C) ALTER
OPERATOR SET with a quoted/non-lowercase option name ("Restrict" = ...) is a
goopg PARSER syntax error where PG accepts the syntax and raises a semantic
error instead; (D) ALTER OPERATOR SET has no ownership permission check at
all (same class as M0134-0088 Bucket C); (E) the "cannot change an
already-set COMMUTATOR/NEGATOR via SET" collision guard is missing for the
reverse-collision case (@=(real,boolean) SET (COMMUTATOR = ===) when ====
already holds it).

Gates run: `go build ./...` clean; targeted go test -run
'TestCreateOperatorPopulatesPGDepend|TestRegoperatorCastResolvesToOID|TestPgDepend|TestCreateOperator|Regoperator|TestCreateOperatorClass'
./internal/executor/ PASS (no regressions in sibling pg_depend/operator
tests); RALPH_PRECOMMIT_SCOPE=units ralph-precommit-test.sh PASS (full suite,
~450s dominated by internal/initdb); make check-testport-inventory PASS; make
regen-testport ran clean; make ralph-state-guard PASS (self-repaired a stale
progress.json completed-marker, same pattern as the M0134-0088 loop);
pre-commit hook's pgbench smoke PASS (331/608/12301 TPS across the 3
pgbench transaction types).

In-flight: none.
