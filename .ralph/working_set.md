Task: M0097-0150 (fix_plan M0122-0007 bucket) — implemented `ALTER
FUNCTION/PROCEDURE/ROUTINE ... OWNER TO` and `... SET SCHEMA` (both were
parse-only no-ops; RENAME TO already worked). COMPLETE and committed this
loop.

Files: internal/catalog/routines.go (new `Routine.Owner uint32` +
`OwnerOrDefault()`; new `Routines.SetSchema(r, newSchema)` re-keys
byKey/byName exactly like RenameRoutine, just on Schema; new
`SetSchemaByOIDDuringRecovery`/`SetOwnerByOIDDuringRecovery` OID-based
recovery counterparts), internal/parser/ast.go (AlterFunctionStmt gains
NewOwner/NewSchema fields), internal/parser/ddl.go (OWNER TO branch now
captures the resolved owner instead of discarding it; SET SCHEMA branch
now captures the new schema — AND fixed a real pre-existing bug: the
attribute loop's SET-clause detection matched TokenIdent value "set", but
SET lexes as the real keyword KwSet, so it never matched and EVERY `ALTER
FUNCTION ... SET ...` form was a syntax error before this loop, not the
documented no-op; also fixed the inner "FROM" check the same way),
internal/executor/operators_ddl.go (execAlterFunction gains OWNER
TO/SET SCHEMA early-return branches mirroring RenameTo's shape and
execAlterAggregateOwner/execAlterCollation's owner-resolution/error-code
pattern), internal/initdb/pg_proc_view.go (proowner for user routines now
r.OwnerOrDefault() instead of hardcoded "10"), internal/wal/recovery.go
(new RecordKindAlterFunctionOwner=121/RecordKindAlterFunctionSetSchema=122
+ Encode/Decode pairs), internal/initdb/function_ddl_recovery.go (replay
cases for both new record kinds), unimplemented_feat.json (M0097-0150
entry status open->resolved, surgical edit only — did NOT run
json.load+dump), docs/design/0015-0002-pg-proc-catalog-and-routine-registry.md
+ docs/design/README.md (new section/row), .ralph/fix_plan.md (M0122-0007
write-up + "Still open" trimmed to ~11), .ralph/deferral_ledger.md (new
row: generic ALTER FUNCTION `SET config=value`/`RESET` remains broken —
two more isolated small parser bugs, see below). New test files:
internal/parser/alter_function_owner_schema_test.go; new tests appended
to internal/executor/operators_function_test.go,
internal/initdb/pg_proc_view_test.go,
internal/initdb/function_ddl_recovery_test.go,
internal/wal/function_ddl_test.go.

Key symbols: execAlterFunction (internal/executor/operators_ddl.go) —
the testable core, now has OWNER TO/SET SCHEMA branches right after the
existing RENAME TO branch. Routines.SetSchema/RenameRoutine
(internal/catalog/routines.go) — the two re-keying mutators (schema and
name are both part of both byKey/byName map keys).

Findings: (1) `catalog.Routine` had NO Owner field at all — unlike
UserAggregate/UserCollation/UserOperator/etc which all already have the
"Owner uint32, 0=unset->bootstrap superuser" pattern — confirming
unimplemented_feat.json's M0097-0150 entry was a real, correctly-scoped
gap, not stale. (2) Discovered mid-implementation that the SET-clause
detection bug made ALL `ALTER FUNCTION ... SET ...` forms syntax errors
(not just SET SCHEMA) — confirmed via `postgres/src/backend/parser/gram.y`
that `common_func_opt_item: FunctionSetResetClause` makes generic `SET
name = value`/`RESET` legitimate, combinable-with-VOLATILE grammar (unlike
OWNER TO/RENAME TO/SET SCHEMA, which gram.y's AlterOwnerStmt/RenameStmt
show are separate exclusive top-level forms — matches this codebase's
existing early-return-per-form precedent, e.g. RenameTo). (3) Fixing the
outer KwSet gate was NOT enough to make the generic SET-value form work:
`p.acceptSymbol("=")` only matches TokenSymbol but `=` lexes as
TokenOperator in this lexer (confirmed internal/parser/lexer.go), and the
"FROM CURRENT" branch checks for a literal "from" token immediately after
SET instead of parsing the config-name first (real grammar is `var_name
FROM CURRENT`, name first) — both pre-existing, both left deferred (see
ledger row) since fixing them wasn't needed to close M0097-0150's own
named scope (OWNER TO/RENAME TO/SET SCHEMA) and RESET already worked
fine on its own. Verified all of this against real PG 18.3 gram.y, not
guessed.

Next step: pick the next task. M-NIGHTLY re-verified clean at this loop's
start (all 8 AI-20260707-000712-* items already [x] in fix_plan.md,
unchanged since last loop — re-verify again next loop per the standing
rule). Candidates carried forward: (a) the just-recorded ALTER FUNCTION
generic SET/RESET parser fix (small, well-isolated — see deferral ledger
row appended today for the exact 2-line-ish fix + test additions needed);
(b) M0122-0006's opclass/collation OID resolution gap (indclass/
indcollation real OID resolution, live AND heap-restore paths — flagged
"defer indefinitely" 2026-06-15, re-read that row before committing);
(c) M0122-0007's remaining ~11 items: CREATE/DROP DATABASE full DDL
(architecturally large — goopg is single-database today), REINDEX
physical rebuild (validation/locking already real, just no-op rebuild),
tablespaces, `ALTER TABLE ... ALTER COLUMN RESET (...)` (confirmed-open,
unrelated to today's function-OWNER work), planner/jit GUC stubs; (d)
M0122-0008 (SASLprep/channel binding/scram_iterations; RBAC mostly done,
view's-own-ACL gap remains); (e) M0119-0004/0005/0006/0007 per the
Current Priority banner (M0119-0005 hash/gin/gist/spgist/brin AM gap,
M0119-0006 pg_amproc dispatch gap — check overlap with candidate (b)'s
opclass work before picking both).

Gates run: go build ./... clean; go vet ./... clean; go test
./internal/parser/... ./internal/catalog/... ./internal/executor/...
./internal/wal/... ./internal/initdb/... ./internal/planner/... ALL
PASS. scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33).
RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh PASS (0
failed, all 3 workloads). Live e2e: real cmd/goopg binary on
127.0.0.1:65499 — CREATE ROLE/CREATE FUNCTION/ALTER FUNCTION OWNER TO/SET
SCHEMA, confirmed pg_proc.proowner/pronamespace changed to the real
role/schema OIDs, function still callable under its new schema
(app.add_one(41)=42), 42704/42883 error paths verified, then `goopg
restart`'d the same data dir and confirmed both survived. make
ralph-state-guard: 2 benign issues auto-repaired (identical pattern to
every prior loop).

In-flight: none. Manual verification data dir (/tmp/goopg-alterfunc-verify)
and scratch binary (/tmp/goopg-bin) were fully torn down (server stopped,
files removed) before this loop ended.
