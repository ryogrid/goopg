(idle — nothing in flight)

Last landed: DU-002 slice 141 (loop #106) — DEFERRABLE on the INLINE-column
UNIQUE form (anonymous `a int UNIQUE DEFERRABLE INITIALLY DEFERRED` + named
`a int CONSTRAINT cudef UNIQUE DEFERRABLE …`) now round-trips through pg_dump.
Fixed a HARD PARSE ERROR: the inline column UNIQUE parser case parsed only the
optional NULLS [NOT] DISTINCT clause; a trailing DEFERRABLE fell through to the
column-constraint loop's default arm (returns) → unconsumed token → whole CREATE
TABLE failed. 3 sites: parser — both inline UNIQUE cases call a NEW shared
`parseUniqueDeferrable(p)` helper, capturing `[NOT] DEFERRABLE [INITIALLY
DEFERRED|IMMEDIATE]` (+ bare INITIALLY DEFERRED) into new
ColumnDef.UniqueDeferrable/UniqueInitiallyDeferred; executor per-column UNIQUE
loop threads both onto the backing index beside slice-136 NullsNotDistinct;
deparse + pg_constraint UNCHANGED from slice 139 (shared, read from the index).
Scope: pure dump-fidelity — deferred CHECKING not implemented (per-row enforce).
Files: internal/parser/ddl.go, internal/parser/ast.go, internal/parser/ddl_test.go,
internal/executor/operators_ddl.go, internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md.
Verified: TestParseColumnUniqueDeferrable PASS; TestPort_PgDumpConnectionSetup
PASS (2.43s); parser/catalog/executor suites green; gofmt/build/vet OK. Committed.

Next direction (slice 142): DEFERRABLE on the PRIMARY KEY forms — all three still
DISCARD the flag: anonymous table-level (`PRIMARY KEY (a) DEFERRABLE`), named
table-level (`CONSTRAINT pk PRIMARY KEY (a) DEFERRABLE`), and inline column
(`a int PRIMARY KEY DEFERRABLE`). Parser sites at ddl.go ~1920 (anon PK trailer
already `_ = p.acceptKeyword(KwDeferrable)` — discarded) and the inline PK case
(~2597, no DEFERRABLE slot). OR an exclusion-constraint (`EXCLUDE USING gist`)
dump surface. Deferred-check EXECUTION remains a separate txn-machinery milestone.
