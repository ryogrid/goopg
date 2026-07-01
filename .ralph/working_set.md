(idle — nothing in flight)

Loop #63 landed + committed (this commit): general regproc/regprocedure
OID→name resolution at query output (M0119-0004, design
`0119-0004-regproc-oid-name-resolution.md`) — resolves the loop #61/#62
ledger row's resume point: "goopg has no general OID→name resolution for
`regproc`-typed columns at query-output time in EITHER protocol". New leaf
`internal/catalog.RegprocName(oid) (string, bool)` backed by generated
`internal/catalog/pg_proc_names_generated.go` (`cmd/gen-pg-proc-data -names`,
name-only duplicate of `internal/initdb/pg_proc_seed_data.go`'s 3397-row
table — `internal/catalog` is a true leaf both `internal/executor` and
`internal/initdb`/`internal/server` can import without the cycle that blocks
`internal/executor` importing `internal/initdb` directly). Wired at two
sites: (1) `internal/server/dispatch.go`'s shared `appendTypedCellText` gains
a `regproc`/`regprocedure` case (0→"-", else RegprocName then
`Routines().LookupByOID` for user-defined functions) — `pg_type.typinput`/
`typoutput`, `pg_operator.oprcode`/`oprrest`/`oprjoin`, `pg_am.amproc` now
render function names on a direct SELECT, both wire protocols (post-loop-#62
unification); (2) `internal/executor/expr.go`'s `::regproc`/`::regprocedure`
CastExpr OID-input branch — previously a silent no-op returning the input
datum unchanged for non-zero OIDs — now resolves the same way, matching the
sibling `::regclass` cast. New tests: `TestRegprocName` (catalog),
`TestAppendTypedCellTextRegprocRendersName` (server),
`TestRegprocOIDCastResolvesName` (executor). Gates: build/vet clean;
internal/catalog+executor+server+initdb+planner suites PASS; TPC-H
spotcheck Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook (runs live on
commit). Design doc + README index added (0119-0004bj). Ledger row appended
(resolved). fix_plan.md intentionally NOT edited (driver-churn — see memory:
record progress in ledger + working_set only).

Next candidates (from the loop #63 ledger row + fix_plan.md M0119 tail):
(1) `regoper`/`regoperator` OID→name resolution — no current column is
typed `regoper` so no observable gap yet, deferred until one exists;
(2) `regprocedure` argument-type-list disambiguation for overloaded
functions (renders bare name today, like `regproc`; PG's regprocedureout
appends `(argtypes)` — `Routines().LookupByOID` already has ArgTypes, so
this is a `appendTypedCellText`/`expr.go` rendering-only change once a
fixture needs it); (3) M0119-0005 (pg_waldump server tier), M0119-0006
(pg_amcheck server tier), M0119-0007 (pg_basebackup recvlogical, blocked on
logical decoding) — see fix_plan.md; (4) M0119-0002 (CLOG store swap Part B)
also still open; (5) `datacl` (pg_database ACL) stays permanently deferred
(--create-only, untestable under the connsetup harness) — not actionable.
