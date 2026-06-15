Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 16
(pg_foreign_data_wrapper virtual view) COMPLETE this loop. NOTHING in flight;
next loop starts on slice 17 (pg_options_to_table FROM-clause SRF).

=== DONE (loop #39) — DU-002 slice 16 ===
pg_dump's getForeignDataWrappers ran `SELECT … FROM pg_foreign_data_wrapper`;
aborted with `relation "pg_foreign_data_wrapper" does not exist`. Fix:
- internal/catalog/catalog.go (beside pg_ts_config): added empty
  pg_foreign_data_wrapper virtual view OID 2328, schema from
  pg_foreign_data_wrapper.h: oid, fdwname name, fdwowner oid, fdwhandler oid,
  fdwvalidator oid, fdwacl aclitem[], fdwoptions text[]. VirtualRows returns nil.
  EMPTY correct: goopg has no FDWs; only user FDWs dumped.
- pgdump_connsetup_test.go: added slice-16 to landed list; rewrote next-blocker
  comment to pg_options_to_table (the REAL new blocker — see below).
- design doc 0110-0001 slice-16 block + fix_plan loop #39 entry.
Gates run: go build ./... OK; gofmt clean; go vet catalog OK; catalog + initdb
unit suites PASS; TestPort_PgDumpConnectionSetup PASS. tpch-spotcheck N/A
(additive empty virtual view; zero query-path/row-count risk).

=== NEXT STEP — DU-002 slice 17 (pg_options_to_table FROM-clause SRF) ===
After slice 16 the relation resolves but the query advances to a NEW empirically-
confirmed blocker: `column "option_name" does not exist`. The getForeignDataWrappers
ARRAY subquery is:
  array_to_string(ARRAY(SELECT quote_ident(option_name) || ' ' ||
  quote_literal(option_value) FROM pg_options_to_table(fdwoptions) ORDER BY
  option_name), E',\n    ') AS fdwoptions
pg_options_to_table(text[]) is an SRF with OUT cols (option_name text,
option_value text). goopg SEEDS it in pg_proc (OID 2289,
internal/initdb/pg_proc_seed_data.go:1473) but does NOT implement it as an
executable FROM-clause SRF — so the subquery's column refs are unresolvable at
PLAN time even though the outer view is empty (goopg resolves subquery columns
during planning regardless of outer emptiness; it is NOT lazy).
Slice 17 = implement pg_options_to_table as a FROM-clause SRF: parse a text[] of
"name=value" (or "name" → value NULL) option strings into rows of
(option_name, option_value). Look at how existing FROM-clause SRFs are wired:
parser FROM-clause SRF name switch (internal/parser/select.go), planner SRF
plan node + planX (internal/planner/planner.go, plan.go), executor op (e.g.
operators_pg_available_wal_summaries.go is the M0095-0002 precedent pattern).
PG impl: src/backend/foreign/foreign.c pg_options_to_table / deconstruct via
untransformRelOptions. RUN TestPort_PgDumpConnectionSetup after to find REAL next
blocker (predicted then: getForeignServers/pg_foreign_server,
getUserMappings/pg_user_mappings — but VERIFY empirically, the prediction at
slice 15 was wrong).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
