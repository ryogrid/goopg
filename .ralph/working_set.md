Loop #18 implemented + committed + pushed M0119-0004 pg_dump parity slice 437
(commit 8c799450, pushed to origin/align-data-structure-with-pg — clean).

Task: continued the `M0119-0004` pg_dump catalog-view/dump-fidelity parity
slice series (guard `TestPort_PgDumpConnectionSetup`,
`internal/testport/pgdump_connsetup_test.go`). Landed slice 437: `CREATE
TEXT SEARCH DICTIONARY name (TEMPLATE = tmpl [, key = value, ...])` now
actually persists to the catalog and round-trips through pg_dump — this was
a genuine engine gap (unlike slice 436's "no bug found"): the statement was
previously a parsed-and-discarded `CompatNoopStmt`, and all four `pg_ts_*`
virtual views (`pg_ts_parser/template/dict/config`) unconditionally
returned `nil`. Scope: DICTIONARY only (the common real-world TS object
kind); CONFIGURATION/PARSER/TEMPLATE remain untouched compat no-ops (each
needs its own real-PG probe, non-trivially larger — CONFIGURATION has its
own `pg_ts_config_map` catalog, TEMPLATE is a C-function-loading feature
with no analog here).

Files (all committed, see 8c799450): `internal/parser/ast.go` (new
`TSDictOption` type + `CompatNoopStmt.TSDictTemplate`/`TSDictOptions`
fields), `internal/parser/ddl.go` (the `CREATE TEXT SEARCH ...` case now
scans the paren option-list for the DICTIONARY kind, mirroring the existing
CREATE OPERATOR option scanner), `internal/catalog/catalog.go`
(`BuiltinTSTemplateOID` map seeding the 4 real built-in template OIDs from
upstream `pg_ts_template.dat`; `UserTSDict` type +
`CreateTSDict`/`DropTSDict`/`ListUserTSDicts` mirroring `UserConversion`;
`pg_ts_template.VirtualRows`/`pg_ts_dict.VirtualRows` now return real rows),
`internal/executor/operators_ddl.go` (new `"text search dictionary"` CREATE
case + `serializeTSDictOptions` helper porting PG's `serialize_deflist`; DROP
fallthrough gained a `DropTSDict` call), `internal/testport/
pgdump_connsetup_test.go` (fixture `CREATE TEXT SEARCH DICTIONARY
public.simple_dict (TEMPLATE = pg_catalog.simple, STOPWORDS = english)` +
exact-block + OWNER-TO assertions, verified byte-for-byte against a real PG
18.3 cluster), `.ralph/deferral_ledger.md` (new open row: restart/WAL
persistence, `verify_dictoptions` validation, and ALTER TEXT SEARCH
DICTIONARY are NOT implemented this slice), `docs/design/
0110-0001-pg-dump-tap-port.md` (new "Slice 437" section). `.ralph/
fix_plan.md` intentionally untouched (M0119-0004 is a living slice-by-slice
item).

Key symbols: `catalog.BuiltinTSTemplateOID`/`catalog.UserTSDict`/
`InMemory.CreateTSDict` (internal/catalog/catalog.go, ~2895-2950 and
~10965-11020), `pg_ts_template`/`pg_ts_dict` VirtualRows (same file,
~8378-8470), `serializeTSDictOptions` + `case "text search dictionary"`
(internal/executor/operators_ddl.go, ~14656 and ~15532).

Next step: pick the next untested pg_dump construct for slice 438. Two
candidates surfaced but NOT bundled into 437 (each needs its own real-PG
probe first): (1) `GRANT ... WITH GRANT OPTION GRANTED BY <role>` grantor
round-trip fidelity on an object-privilege GRANT (note: role-MEMBERSHIP
GRANTED BY is already wired via `RoleMembershipChange.GrantedBy`/
`operators_ddl_role_membership.go` — but GRANT role TO role is dumped by
pg_dumpall --roles, NOT pg_dump itself, so verify first whether this is
even a pg_dump-scoped gap at all before spending a slice on it); (2)
`CREATE TEXT SEARCH CONFIGURATION` (needs `pg_ts_config_map` modeling for
the `ALTER ... ADD MAPPING FOR ... WITH ...` clause — bigger than slice 437,
decompose further if picked up). Otherwise keep following the established
pattern: grep `Slice [0-9]+` in the test file's doc comment for already-
covered ground, spin up real local PG 18.3 (`postgres/local_install/bin`,
remember `LD_LIBRARY_PATH=postgres/local_install/lib` or psql/pg_dump hit
`undefined symbol: PQsendPipelineSync`) to get verified ground-truth output
before assuming any divergence is a bug, land ONE slice per loop.

Gates run (this loop): `go build ./...` clean; `gofmt -d` on all 4 touched
non-test files showed zero diff in the new code (pre-existing unrelated
drift elsewhere in catalog.go/operators_ddl.go/ast.go — NOT touched, per the
known go1.25-vs-go1.26 gofmt version-mismatch rule, do not gofmt -w); `go
test -count=1 -run TestPort_PgDumpConnectionSetup ./internal/testport/ -v`
PASS; `go test -count=1 ./internal/catalog/... ./internal/parser/...
./internal/executor/...` PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/
Q13=33); pre-commit pgbench smoke PASS at commit time (TPC-B ~181-243 tps,
select-only ~13.9k tps, 0 failed across all three); `make ralph-state-guard`
OK (auto-repaired the usual stale completed-marker pattern, same as every
prior loop).

Note: an untracked `postgres` directory shows build-artifact content
(GNUmakefile, config.log, etc.) — pre-existing, not touched or committed
(carried forward from prior loops). `.ralph/progress.json` shows as
modified (driver-owned state file, not mine to stage/commit).
