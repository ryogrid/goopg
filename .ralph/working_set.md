(idle — nothing in flight)

Loop #39 landed and committed clean: M0119-0004 DU-002 slice 413 (curated
builtin-operator catalog + op_class opckeytype fix). Closes the loop
#36/#37/#38 "builtin-operator catalog" finding for the exact upstream
`op_class` pg_dump fixture (bigint btree opclass whose OPERATOR entries
name real builtin int8 comparison operators, not user-defined ones).

What landed:
- `catalog.BuiltinOperator`/`builtinOperatorsByKey`/`builtinOperatorsByOID`/
  `LookupBuiltinOperator`/`LookupBuiltinOperatorByOID` (internal/catalog/
  catalog.go, mirrors the pre-existing `builtinProcsByName` curated-set
  pattern — NOT a full pg_operator.dat port; that data already exists via
  internal/initdb/pg_operator_seed_data.go but that package isn't
  importable from catalog/executor). Curated: 5 int8 btree comparison ops
  (oids 410/412/413/414/415) + btint8cmp (oid 842).
- `resolveOpClassOperator` (executor/operators_ddl.go) typed branch and
  `RegoperatorNameAndSchema`/bare-name `regoper` CastExpr (executor/expr.go)
  gain a builtin fallback.
- Second bug found via live-PG-18.3 diff: `execCreateOpClass`'s keyTypeOID
  never reset to InvalidOid when STORAGE == FOR TYPE (real PG does per
  opclasscmds.c DefineOpClass) — fixed; corrected the stale
  TestCreateOperatorClassPopulatesOpclassRow assertion.
- Verified byte-identical against a live, freshly-built PG 18.3 instance
  (postgres/local_install) for the op_class/op_class_empty fixture pair.
- All gates green: build/vet, catalog+executor+parser+server+planner
  suites, TestPort_PgDumpConnectionSetup, TPC-H spotcheck Q12=2/Q13=33,
  pgbench smoke pre-commit. Pushed to origin/align-data-structure-with-pg.

Next candidates (backlog, per the M0119-0004 ledger's open rows):
(1) FOR ORDER BY sort-family resolution (small, well-isolated) —
`parseCreateOpClassTail`'s FOR ORDER BY branch is parsed-and-discarded;
needs `OpClassMember.SortFamilySchema/Name` + resolve via
`LookupUserOperatorFamily` (same AM) + `AmOpMember.SortFamilyOID`/
amoppurpose='o'. Not blocking any fixture currently in scope. (2) Extend
the builtin-operator catalog further as new fixtures need different
builtin operators (curate incrementally) OR generate a full leaf-package
index via a new `-names` mode on cmd/gen-pg-operator-data (mirrors
cmd/gen-pg-proc-data's `pg_proc_names_generated.go` split) — large,
standalone. (3) M0119-0005/0006/0007 (pg_waldump/pg_amcheck/pg_basebackup
server tiers). (4) M0119-0002 (CLOG store swap Part B) — flagged highest
blast radius, needs dedicated full-gate session. (5) datacl (pg_database
ACL) — permanently deferred. (6) `op_class_custom` ordering fixture
(range-type subtype_opclass binding) — untested.

Recommendation for next loop: (1) FOR ORDER BY is the smallest,
well-isolated next step if a quick loop is preferred (no fixture blocks on
it yet, so lower urgency than prior picks). (2)'s incremental-curation path
is the lowest-friction way to keep unblocking pg_dump fixtures one at a
time as they're ported; the full-generator path is a bigger investment
worth its own design-doc loop if M0110-0001's pg_dump port work continues
toward GiST/GIN/hash opclass fixtures.
