(idle — nothing in flight)

Last landed: DU-002 slice 146 (loop #111) — COMMENT ON {MATERIALIZED VIEW,TYPE,
DOMAIN} now round-trips through pg_dump. parseCommentOnTail had no branch for
these kinds, so each fell through to the unsupported default branch and the
server's COMMENT fallback silently swallowed them (nothing reached
pg_description → pg_dump re-emitted nothing). Fix: (1) parser
parseCommentOnTail (internal/parser/parser.go) gains three acceptIdentKeyword
branches — `materialized` (+ optional VIEW), `type`, `domain` (none is a lexer
keyword); (2) execCommentOn (internal/executor/operators_ddl.go ~L8404) folds
`materialized view` into the table/view/sequence case (matview is a pg_class
relation, classoid 1259, shared LookupTable path — pg_dump picks MATERIALIZED
VIEW from relkind='m') and adds `type`/`domain` cases → im.LookupEnum /
im.LookupDomain, keyed under classoid=pg_type (1247); pg_dump picks TYPE vs
DOMAIN from typtype. No catalog-schema change.
Key symbols: parser.parseCommentOnTail, ddlOp.execCommentOn,
InMemory.LookupTable, InMemory.LookupEnum, InMemory.LookupDomain,
InMemory.SetComment.
Files: internal/parser/parser.go, internal/executor/operators_ddl.go,
internal/testport/pgdump_connsetup_test.go,
docs/design/0110-0001-pg-dump-tap-port.md, .ralph/fix_plan.md.
Verified: gofmt/build/vet OK; parser+catalog+executor suites PASS;
TestPort_PgDumpConnectionSetup PASS (2.84s, 3 new asserts: foo_mv MATERIALIZED
VIEW, mood TYPE, zipcode DOMAIN — verified vs real pg_dump 18.3).
Committed + pushed.

Next direction (slice 147): a fresh pg_dump catalog-surface gap. Candidates:
COMMENT ON {FUNCTION, COLLATION, EXTENSION} round-trip (none handled by
parseCommentOnTail yet). Or the deferred-check EXECUTION spike (validate at
COMMIT, not per-row) — a larger txn-machinery milestone, separate from
dump-fidelity.
